package radar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tileOf is the Web Mercator tile and the pixel inside it that a point falls in.
func tileOf(lat, lon float64, z int) (x, y, px, py int) {
	n := math.Pow(2, float64(z))
	fx := (lon + 180) / 360 * n
	fy := (1 - math.Asinh(math.Tan(lat*math.Pi/180))/math.Pi) / 2 * n
	return int(fx), int(fy), int((fx - math.Floor(fx)) * 256), int((fy - math.Floor(fy)) * 256)
}

// The projection is checked against GDAL elsewhere; here a known cell lands on the known pixel, in the colour the live legend gives its dBZ.
func TestTileGoldenPixel(t *testing.T) {
	g := synth(t, func(dbz, snow []byte) {
		dbz[10*20+10] = enc(40) // the cell under (38.5, -97.5): yellow band (35 to 45)
		dbz[10*20+11] = enc(5)  // dry: below the light edge, transparent
		dbz[10*20+9] = NoData   // no value: transparent, not a dry colour
		dbz[9*20+10] = enc(55)  // red band
	})
	const z = 7
	// The centre of cell (col 10, row 10): 1.5 km east and 1.5 km south of the projection origin.
	lat, lon := g.proj.inverse(1500, -1500)
	x, y, px, py := tileOf(lat, lon, z)
	b, opaque := g.renderTile(z, x, y)
	if !opaque {
		t.Fatal("expected drawn pixels")
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if c := img.(*image.NRGBA).NRGBAAt(px, py); c.R != 0xff || c.G != 0xff || c.B != 0 || c.A == 0 {
		t.Errorf("pixel (%d,%d) = %+v, want yellow", px, py, c)
	}
	// The cell to the east is dry (5 dBZ): 3 km is about 2.5 pixels at z7, so 6 pixels east is inside it.
	if _, _, _, a := img.At(px+6, py).RGBA(); a != 0 {
		t.Errorf("a dry cell must be transparent, alpha %d", a)
	}
	// The cell to the west has no data: transparent too, never a "dry" colour.
	if _, _, _, a := img.At(px-6, py).RGBA(); a != 0 {
		t.Errorf("a no-data cell must be transparent, alpha %d", a)
	}
}

func TestTileOutsideTheGridIsTransparent(t *testing.T) {
	g := synth(t, func(dbz, snow []byte) {
		for i := range dbz {
			dbz[i] = enc(50)
		}
	})
	// A tile over the Atlantic at z3 is outside the 60 km synthetic grid.
	b, opaque := g.renderTile(3, 3, 3)
	if opaque {
		t.Error("a tile outside the grid drew pixels")
	}
	img, _ := png.Decode(bytes.NewReader(b))
	if _, _, _, a := img.At(128, 128).RGBA(); a != 0 {
		t.Error("outside must be transparent")
	}
}

func TestInverseProjectionRoundTrips(t *testing.T) {
	p, _ := newLCC(testProj)
	for _, c := range [][2]float64{{40, -100}, {41.88, -87.63}, {47.6, -122.3}, {30, -90}, {25.5, -80.2}} {
		x, y := p.forward(c[0], c[1])
		lat, lon := p.inverse(x, y)
		if math.Abs(lat-c[0]) > 1e-6 || math.Abs(lon-c[1]) > 1e-6 {
			t.Errorf("(%v,%v) -> (%v,%v)", c[0], c[1], lat, lon)
		}
	}
}

func forecastServer(t *testing.T, ct time.Time, hours []int, hits *atomic.Int32) *httptest.Server {
	file := synthFile(t, func(d, s []byte) { d[10*20+10] = enc(40) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest.json" {
			hj := ""
			for i, h := range hours {
				if i > 0 {
					hj += ","
				}
				hj += fmt.Sprint(h)
			}
			fmt.Fprintf(w, `{"v":1,"cycles":[{"cycle":"2026110818","cycle_time":%q,"hours":[%s]}]}`, ct.Format(time.RFC3339), hj)
			return
		}
		hits.Add(1)
		_, _ = w.Write(file)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestForecastHoursAndTile(t *testing.T) {
	ct := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	var hits atomic.Int32
	srv := forecastServer(t, ct, []int{2, 3, 4}, &hits)
	s := NewStore(srv.URL, nil)
	now := ct.Add(3 * time.Hour)
	s.now = func() time.Time { return now }
	var mu sync.Mutex
	outcomes := map[string]int{}
	s.OnTile = func(o string) { mu.Lock(); outcomes[o]++; mu.Unlock() }
	ctx := context.Background()

	hs, err := s.ForecastHours(ctx)
	if err != nil || hs.Cycle != "2026110818" || hs.Credit != "NOAA RRFS" || len(hs.Hours) != 3 {
		t.Fatalf("hours: %+v %v", hs, err)
	}
	if hs.Hours[0].FH != 2 || hs.Hours[0].ValidUnix != ct.Add(2*time.Hour).Unix() || hs.HorizonUnix != ct.Add(4*time.Hour).Unix() {
		t.Errorf("hours/horizon: %+v", hs)
	}
	// The bounds hold the grid's centre and are a small box (60 km synthetic grid).
	if b := hs.Bounds; !(b[0] < -97.5 && -97.5 < b[2] && b[1] < 38.5 && 38.5 < b[3]) || b[2]-b[0] > 2 {
		t.Errorf("bounds %v", hs.Bounds)
	}

	x, y, _, _ := tileOf(38.5, -97.5, 7)
	b1, err := s.Tile(ctx, "2026110818", 3, 7, x, y)
	if err != nil || len(b1) == 0 {
		t.Fatalf("tile: %v", err)
	}
	b2, _ := s.Tile(ctx, "2026110818", 3, 7, x, y)
	if !bytes.Equal(b1, b2) {
		t.Error("a second read differs")
	}
	if outcomes["render"] != 1 || outcomes["hit"] != 1 {
		t.Errorf("outcomes %v", outcomes)
	}
	if s.TileCachedBytes() < len(b1) {
		t.Error("cache gauge")
	}
	// A tile with nothing in it is counted as absent.
	if _, err := s.Tile(ctx, "2026110818", 3, 3, 3, 3); err != nil {
		t.Fatal(err)
	}
	if outcomes["absent"] != 1 {
		t.Errorf("outcomes %v", outcomes)
	}

	// Not offered: an hour past the horizon, another cycle, an old cycle.
	for _, c := range []struct {
		cycle string
		fh    int
	}{{"2026110818", 5}, {"2026110818", 0}, {"2026110812", 3}} {
		if _, err := s.Tile(ctx, c.cycle, c.fh, 7, x, y); !errors.Is(err, ErrNoForecast) {
			t.Errorf("%v: err %v, want ErrNoForecast", c, err)
		}
	}
	now = ct.Add(MaxCycleAge + time.Minute)
	s.manAt = time.Time{} // let the manifest reload at the new "now"
	if _, err := s.ForecastHours(ctx); !errors.Is(err, ErrNoForecast) {
		t.Errorf("stale cycle: %v", err)
	}
	if _, err := s.Tile(ctx, "2026110818", 3, 7, x, y); !errors.Is(err, ErrNoForecast) {
		t.Errorf("stale cycle tile: %v", err)
	}
	// Bad addresses are refused.
	for _, z := range []int{2, 8} {
		if _, err := s.Tile(ctx, "2026110818", 3, z, 0, 0); err == nil || errors.Is(err, ErrNoForecast) {
			t.Errorf("z%d: %v", z, err)
		}
	}
}

func TestForecastNilStoreAndConcurrentRender(t *testing.T) {
	var nilStore *Store
	if _, err := nilStore.ForecastHours(context.Background()); !errors.Is(err, ErrNoForecast) {
		t.Error("nil store hours")
	}
	if _, err := nilStore.Tile(context.Background(), "2026110818", 1, 7, 0, 0); !errors.Is(err, ErrNoForecast) {
		t.Error("nil store tile")
	}
	ct := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	var hits atomic.Int32
	srv := forecastServer(t, ct, []int{3}, &hits)
	s := NewStore(srv.URL, nil)
	s.now = func() time.Time { return ct.Add(3 * time.Hour) }
	x, y, _, _ := tileOf(38.5, -97.5, 7)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Tile(context.Background(), "2026110818", 3, 7, x, y); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Errorf("grid fetched %d times for one hour", hits.Load())
	}
}

func BenchmarkRenderTile(b *testing.B) {
	g := synth(&testing.T{}, func(dbz, snow []byte) {
		for i := range dbz {
			dbz[i] = enc(30)
		}
	})
	x, y, _, _ := tileOf(38.5, -97.5, 7)
	for i := 0; i < b.N; i++ {
		g.renderTile(7, x, y)
	}
}

// A map cancels its tile requests on every pan, and every tile of an hour shares one grid load: the request that started the load going away must
// not fail the tiles still waiting on it.
func TestCancelledLeaderDoesNotFailTheOtherTiles(t *testing.T) {
	ct := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	file := synthFile(t, func(d, s []byte) { d[10*20+10] = enc(40) })
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest.json" {
			fmt.Fprintf(w, `{"v":1,"cycles":[{"cycle":"2026110818","cycle_time":%q,"hours":[3]}]}`, ct.Format(time.RFC3339))
			return
		}
		once.Do(func() { close(started) })
		<-release
		_, _ = w.Write(file)
	}))
	defer srv.Close()
	s := NewStore(srv.URL, nil)
	s.now = func() time.Time { return ct.Add(3 * time.Hour) }
	x, y, _, _ := tileOf(38.5, -97.5, 7)

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() { _, err := s.Tile(leaderCtx, "2026110818", 3, 7, x, y); leaderErr <- err }()
	<-started
	type res struct {
		b   []byte
		err error
	}
	second := make(chan res, 1)
	go func() { b, err := s.Tile(context.Background(), "2026110818", 3, 7, x+1, y); second <- res{b, err} }()
	time.Sleep(50 * time.Millisecond) // the second tile is now waiting on the shared grid load
	cancel()
	close(release)
	r := <-second
	if r.err != nil || len(r.b) == 0 {
		t.Fatalf("the second tile failed with the leader's cancellation: %v", r.err)
	}
	_ = <-leaderErr
}

// A grid that cannot be read is "try again" (ErrFeedDown), not "the hour is out of reach" (ErrNoForecast).
func TestUnreadableGridIsFeedDownNotNoForecast(t *testing.T) {
	ct := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest.json" {
			fmt.Fprintf(w, `{"v":1,"cycles":[{"cycle":"2026110818","cycle_time":%q,"hours":[3]}]}`, ct.Format(time.RFC3339))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := NewStore(srv.URL, nil)
	s.now = func() time.Time { return ct.Add(3 * time.Hour) }
	if _, err := s.Tile(context.Background(), "2026110818", 3, 7, 20, 45); !errors.Is(err, ErrFeedDown) || errors.Is(err, ErrNoForecast) {
		t.Fatalf("grid 500: %v", err)
	}
	srv.Close()
	s2 := NewStore(srv.URL, nil)
	if _, err := s2.Tile(context.Background(), "2026110818", 3, 7, 20, 45); !errors.Is(err, ErrFeedDown) {
		t.Fatalf("service down: %v", err)
	}
}
