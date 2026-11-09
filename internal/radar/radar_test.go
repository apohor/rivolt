package radar

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

var t0 = time.Date(2026, 11, 9, 0, 30, 0, 0, time.UTC)

func caps(times ...time.Time) string {
	var ts []string
	for _, t := range times {
		ts = append(ts, t.Format("2006-01-02T15:04:05.000Z"))
	}
	return `<?xml version="1.0"?><WMS_Capabilities><Capability><Layer><Dimension name="time" default="x" units="ISO8601" nearestValue="1">` + strings.Join(ts, ",") + `</Dimension></Layer></Capability></WMS_Capabilities>`
}

// frames returns 60 frames two minutes apart ending at newest.
func frames(newest time.Time) []time.Time {
	var out []time.Time
	for i := 59; i >= 0; i-- {
		out = append(out, newest.Add(-time.Duration(i)*2*time.Minute))
	}
	return out
}

type fakeNOAA struct {
	t        *testing.T
	newest   time.Time
	mu       sync.Mutex
	getMaps  []url.Values
	uas      []string
	capsHits atomic.Int32
	// xmlFor3857: answer the Web Mercator request with a service exception (as if the server stopped reprojecting).
	xmlFor3857 atomic.Bool
	down       atomic.Bool
	delay      time.Duration
}

func (f *fakeNOAA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.down.Load() {
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	q := r.URL.Query()
	f.mu.Lock()
	f.uas = append(f.uas, r.UserAgent())
	f.mu.Unlock()
	if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Authorization") != "" {
		f.t.Errorf("a request to NOAA carried a cookie, referer or authorization header")
	}
	switch strings.ToLower(q.Get("request")) {
	case "getcapabilities":
		f.capsHits.Add(1)
		_, _ = w.Write([]byte(caps(frames(f.newest)...)))
	case "getmap":
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		f.mu.Lock()
		f.getMaps = append(f.getMaps, q)
		f.mu.Unlock()
		if f.xmlFor3857.Load() && q.Get("crs") == "EPSG:3857" {
			w.Header().Set("Content-Type", "application/vnd.ogc.se_xml")
			_, _ = w.Write([]byte(`<ServiceExceptionReport><ServiceException>InvalidCRS</ServiceException></ServiceExceptionReport>`))
			return
		}
		wd, _ := strconv.Atoi(q.Get("width"))
		ht, _ := strconv.Atoi(q.Get("height"))
		img := image.NewNRGBA(image.Rect(0, 0, wd, ht))
		for y := 0; y < ht; y++ {
			for x := 0; x < wd; x++ {
				img.SetNRGBA(x, y, color.NRGBA{R: uint8(y % 256), G: 200, B: 10, A: 255})
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(buf.Bytes())
	default:
		http.Error(w, "bad", http.StatusBadRequest)
	}
}

func (f *fakeNOAA) maps() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.getMaps...)
}

func newSvc(t *testing.T, f *fakeNOAA, mut func(*Config)) *Service {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := Config{BaseURL: srv.URL, Now: func() time.Time { return t0 }}
	if mut != nil {
		mut(&cfg)
	}
	return New(cfg)
}

// Tile 3/1/3 is over the western US (CONUS); 3/4/2 is over the Atlantic; 3/0/2 is the Pacific off Mexico.
const (
	zC, xC, yC = 3, 1, 3
)

func TestParseFrames(t *testing.T) {
	f, err := ParseFrames([]byte(caps(frames(t0.Add(-4*time.Minute))...)), t0)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Latest.Equal(t0.Add(-4 * time.Minute)) {
		t.Errorf("latest %v", f.Latest)
	}
	if len(f.Recent) == 0 || len(f.Recent) > maxFrames || !f.Recent[len(f.Recent)-1].Equal(f.Latest) {
		t.Fatalf("recent %v", f.Recent)
	}
	for i := 1; i < len(f.Recent); i++ {
		if d := f.Recent[i].Sub(f.Recent[i-1]); d < frameSpacing {
			t.Errorf("frames %d and %d are only %v apart", i-1, i, d)
		}
	}
	// The newest frame is too old: the feed is down.
	if _, err := ParseFrames([]byte(caps(frames(t0.Add(-time.Hour))...)), t0); err == nil {
		t.Error("a stale feed was accepted")
	}
	for _, bad := range []string{"", "<x/>", caps()} {
		if _, err := ParseFrames([]byte(bad), t0); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTileGeometry(t *testing.T) {
	w, s, e, n := tileLonLat(0, 0, 0)
	if w != -180 || e != 180 || n < 85.05 || n > 85.06 || s > -85.05 || s < -85.06 {
		t.Errorf("z0: %v %v %v %v", w, s, e, n)
	}
	minx, miny, maxx, maxy := tileMercator(1, 1, 0)
	if minx != 0 || maxx < 20037508 || miny != 0 || maxy < 20037508 {
		t.Errorf("z1 x1 y0: %v %v %v %v", minx, miny, maxx, maxy)
	}
	// Tiles meet: the box of x and x+1 share an edge.
	_, _, e0, _ := tileLonLat(5, 7, 9)
	w1, _, _, _ := tileLonLat(5, 8, 9)
	if e0 != w1 {
		t.Errorf("neighbours do not meet: %v %v", e0, w1)
	}
	if got := regionsFor(zC, xC, yC); len(got) != 1 || got[0].name != "conus" {
		t.Errorf("regions for the western US tile: %v", got)
	}
	if got := regionsFor(3, 4, 2); len(got) != 0 {
		t.Errorf("the Atlantic tile is in %v", got)
	}
	// Florida straits: CONUS and the Caribbean overlap.
	if got := regionsFor(6, 17, 27); len(got) < 1 {
		t.Errorf("a south Florida tile is in no region: %v", got)
	}
}

func TestTileFetchRequestAndCache(t *testing.T) {
	f := &fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute)}
	s := newSvc(t, f, nil)
	b, frame, err := s.Tile(context.Background(), zC, xC, yC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !frame.Equal(f.newest) {
		t.Errorf("frame %v, want the newest %v", frame, f.newest)
	}
	if img, err := png.Decode(bytes.NewReader(b)); err != nil || img.Bounds().Dx() != TileSize || img.Bounds().Dy() != TileSize {
		t.Fatalf("not a 256 tile: %v", err)
	}
	m := f.maps()
	if len(m) != 1 {
		t.Fatalf("%d GetMap requests, want 1", len(m))
	}
	q := m[0]
	minx, miny, maxx, maxy := tileMercator(zC, xC, yC)
	if q.Get("crs") != "EPSG:3857" || q.Get("layers") != "conus_bref_qcd" || q.Get("transparent") != "true" || q.Get("width") != "256" || q.Get("height") != "256" ||
		q.Get("time") != f.newest.Format("2006-01-02T15:04:05.000Z") || q.Get("bbox") != fmt.Sprintf("%.4f,%.4f,%.4f,%.4f", minx, miny, maxx, maxy) {
		t.Errorf("GetMap parameters: %v", q)
	}
	if len(f.uas) == 0 || !strings.Contains(f.uas[0], "Rivolt") {
		t.Errorf("user agent %q", f.uas)
	}
	// The second ask is a cache hit: no more upstream requests.
	if _, _, err := s.Tile(context.Background(), zC, xC, yC, 0); err != nil || len(f.maps()) != 1 {
		t.Errorf("second tile: err=%v, %d GetMap requests, want still 1", err, len(f.maps()))
	}
	// A frame the client names must be one of the recent ones; anything else is the newest, so t is not an open relay into NOAA's history.
	_, frame2, err := s.Tile(context.Background(), zC, xC, yC, 12345)
	if err != nil || !frame2.Equal(f.newest) {
		t.Errorf("an unknown frame: %v %v, want the newest", frame2, err)
	}
	fr, _ := s.Frames(context.Background())
	older := fr.Recent[0]
	_, frame3, err := s.Tile(context.Background(), zC, xC, yC, older.Unix())
	if err != nil || !frame3.Equal(older) || len(f.maps()) != 2 {
		t.Errorf("a recent frame: %v %v, %d GetMap requests, want that frame and 2 requests", frame3, err, len(f.maps()))
	}
}

func TestTileOutsideCoverageIsTransparentAndFree(t *testing.T) {
	f := &fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute)}
	s := newSvc(t, f, nil)
	b, _, err := s.Tile(context.Background(), 3, 4, 2, 0)
	if err != nil || !bytes.Equal(b, transparentPNG) {
		t.Fatalf("outside coverage: err=%v transparent=%v", err, bytes.Equal(b, transparentPNG))
	}
	if len(f.maps()) != 0 {
		t.Errorf("a tile outside every mosaic made %d upstream requests", len(f.maps()))
	}
	img, _ := png.Decode(bytes.NewReader(b))
	if _, _, _, a := img.At(10, 10).RGBA(); a != 0 {
		t.Error("the transparent tile is not transparent")
	}
}

// Many users asking for the same tile at once cost NOAA one request.
func TestConcurrentAsksAreOneUpstreamRequest(t *testing.T) {
	f := &fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute), delay: 150 * time.Millisecond}
	s := newSvc(t, f, nil)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Tile(context.Background(), zC, xC, yC, 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(f.maps()); n != 1 {
		t.Errorf("%d upstream requests for 30 concurrent asks of one tile, want 1", n)
	}
	if n := f.capsHits.Load(); n != 1 {
		t.Errorf("%d capabilities requests, want 1", n)
	}
}

// With Redis, replicas share tiles and the frame list: three pods asking at once for the same tile cost NOAA one request.
func TestReplicasShareThroughRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	f := &fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute), delay: 200 * time.Millisecond}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	mk := func() *Service {
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		return New(Config{BaseURL: srv.URL, Redis: rdb, Prefix: "rivolt", Now: func() time.Time { return t0 }})
	}
	pods := []*Service{mk(), mk(), mk()}
	var wg sync.WaitGroup
	for _, p := range pods {
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(p *Service) {
				defer wg.Done()
				if _, _, err := p.Tile(context.Background(), zC, xC, yC, 0); err != nil {
					t.Error(err)
				}
			}(p)
		}
	}
	wg.Wait()
	if n := len(f.maps()); n != 1 {
		t.Errorf("%d upstream tile requests across 3 pods, want 1", n)
	}
	if n := f.capsHits.Load(); n != 1 {
		t.Errorf("%d capabilities requests across 3 pods, want 1", n)
	}
}

// If the server stops reprojecting (answers the Web Mercator request with a service exception) the native EPSG:4326 picture is fetched and
// resampled, and the 4326 path is then used first for a while.
func TestFallsBackToTheNativeProjection(t *testing.T) {
	f := &fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute)}
	f.xmlFor3857.Store(true)
	s := newSvc(t, f, nil)
	b, _, err := s.Tile(context.Background(), zC, xC, yC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if img, err := png.Decode(bytes.NewReader(b)); err != nil || img.Bounds().Dx() != TileSize || img.Bounds().Dy() != TileSize {
		t.Fatalf("resampled tile: %v", err)
	}
	m := f.maps()
	if len(m) != 2 || m[0].Get("crs") != "EPSG:3857" || m[1].Get("srs") != "EPSG:4326" || m[1].Get("height") != strconv.Itoa(nativeHeight) {
		t.Fatalf("requests: %v", m)
	}
	w, south, e, n := tileLonLat(zC, xC, yC)
	if m[1].Get("bbox") != fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", w, south, e, n) {
		t.Errorf("native bbox %q", m[1].Get("bbox"))
	}
	// A different tile now goes straight to 4326.
	if _, _, err := s.Tile(context.Background(), zC, xC+1, yC, 0); err != nil {
		t.Fatal(err)
	}
	m = f.maps()
	if len(m) < 3 {
		t.Fatalf("no request for the next tile: %v", m)
	}
	for _, q := range m[2:] { // this tile meets two mosaics: both are asked, and neither on the Web Mercator path
		if q.Get("srs") != "EPSG:4326" || q.Get("crs") != "" {
			t.Errorf("the next tile asked %v, want only the 4326 path", q)
		}
	}
}

// Each output row of the resample takes the source row at its own latitude, so latitude (non-linear in Mercator) is honoured.
func TestResampleRowsFollowsLatitude(t *testing.T) {
	z, y := 2, 1 // a tile spanning about 66 to 0 degrees: strongly non-linear
	_, south, _, north := tileLonLat(z, 0, y)
	src := image.NewNRGBA(image.Rect(0, 0, TileSize, nativeHeight))
	for r := 0; r < nativeHeight; r++ { // the source row index is the pixel's red channel / 2 (fits a byte)
		for c := 0; c < TileSize; c++ {
			src.SetNRGBA(c, r, color.NRGBA{R: uint8(r / 2), A: 255})
		}
	}
	out := ResampleRows(src, z, y)
	if got := out.NRGBAAt(0, 0).R; got > 1 {
		t.Errorf("top row maps to source row %d, want about 0", int(got)*2)
	}
	if got := out.NRGBAAt(0, TileSize-1).R; got < 254 {
		t.Errorf("bottom row maps to source row %d, want about %d", int(got)*2, nativeHeight-1)
	}
	// The middle output row is the middle of the tile in Mercator, which is NOT the middle latitude: Mercator stretches towards the poles, so the
	// middle of the picture is nearer the equator edge's opposite, the north: it must come from clearly above the middle source row.
	mid := int(out.NRGBAAt(0, TileSize/2).R) * 2
	if mid >= nativeHeight/2-4 {
		t.Errorf("the middle row maps to source row %d, want clearly above the middle (%d): latitude is not linear in Mercator", mid, nativeHeight/2)
	}
	_, _ = north, south
	// Rows never go backwards.
	prev := -1
	for r := 0; r < TileSize; r++ {
		v := int(out.NRGBAAt(0, r).R)
		if v < prev {
			t.Fatalf("row %d maps backwards (%d after %d)", r, v, prev)
		}
		prev = v
	}
}

func TestUpstreamDownIs503AndNotACachedTransparentTile(t *testing.T) {
	f := &fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute)}
	s := newSvc(t, f, nil)
	f.down.Store(true)
	h := s.Handler()
	for _, p := range []string{"/frame", "/3/1/3.png"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s with NOAA down: %d Retry-After %q, want 503 with Retry-After", p, rec.Code, rec.Header().Get("Retry-After"))
		}
	}
	// A stale feed is the same.
	f2 := &fakeNOAA{t: t, newest: t0.Add(-2 * time.Hour)}
	rec := httptest.NewRecorder()
	newSvc(t, f2, nil).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/3/1/3.png", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a stale feed: %d, want 503", rec.Code)
	}
}

func TestHandlerContract(t *testing.T) {
	f := &fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute)}
	s := newSvc(t, f, nil)
	h := s.Handler()
	do := func(method, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
		return rec
	}
	rec := do(http.MethodGet, "/3/1/3.png")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Radar-Frame") != f.newest.Format(time.RFC3339) ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "private") {
		t.Fatalf("tile: %d %v", rec.Code, rec.Header())
	}
	if rec = do(http.MethodGet, "/frame"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"frame_unix":`+strconv.FormatInt(f.newest.Unix(), 10)) ||
		!strings.Contains(rec.Body.String(), `"max_zoom":9`) {
		t.Errorf("frame: %d %s", rec.Code, rec.Body)
	}
	if rec = do(http.MethodHead, "/3/1/3.png"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d body bytes", rec.Code, rec.Body.Len())
	}
	if rec = do(http.MethodPost, "/3/1/3.png"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
	if rec = do(http.MethodGet, "/3/1/3.png?t=abc"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad t: %d", rec.Code)
	}
	for p, want := range map[string]bool{
		"/3/1/3.png": true, "/10/1023/1023.png": true, "/11/0/0.png": false, "/3/8/0.png": false, "/3/1/3.jpg": false, "/3/01/3.png": false,
		"/3/1/3.png/": false, "/../3/1/3.png": false, "/3/1.png": false, "/frame/x": false, "/": false, "/3/-1/3.png": false, "/3/1/3.PNG": false,
	} {
		_, _, _, ok := ParseTilePath(p)
		if ok != want {
			t.Errorf("ParseTilePath(%q) = %v, want %v", p, ok, want)
		}
	}
	if rec = do(http.MethodGet, "/3/8/0.png"); rec.Code != http.StatusNotFound {
		t.Errorf("an out-of-grid tile: %d, want 404", rec.Code)
	}
}
