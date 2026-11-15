package radar

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// rampNOAA answers GetMap with a picture that is a smooth ramp of the world x coordinate (1 unit per km), opaque, so tiles that touch must agree at
// their shared edge when the upscaling is right.
type rampNOAA struct {
	fakeNOAA
	widths []float64 // bbox widths asked for, in metres
	wmu    sync.Mutex
}

func (f *rampNOAA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if strings.ToLower(q.Get("request")) != "getmap" {
		f.fakeNOAA.ServeHTTP(w, r)
		return
	}
	var minx, miny, maxx, maxy float64
	parts := strings.Split(q.Get("bbox"), ",")
	vals := make([]float64, 4)
	for i, p := range parts {
		vals[i], _ = strconv.ParseFloat(p, 64)
	}
	minx, miny, maxx, maxy = vals[0], vals[1], vals[2], vals[3]
	_ = miny
	_ = maxy
	f.wmu.Lock()
	f.widths = append(f.widths, maxx-minx)
	f.wmu.Unlock()
	img := image.NewNRGBA(image.Rect(0, 0, TileSize, TileSize))
	for y := 0; y < TileSize; y++ {
		for x := 0; x < TileSize; x++ {
			wx := minx + (float64(x)+0.5)/TileSize*(maxx-minx)
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(int(wx/1000)%200 + 20), G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(buf.Bytes())
}

func newRamp(t *testing.T, mut func(*Config)) (*Service, *rampNOAA) {
	t.Helper()
	f := &rampNOAA{fakeNOAA: fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute)}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := Config{BaseURL: srv.URL, Now: func() time.Time { return t0 }, Smooth: true}
	if mut != nil {
		mut(&cfg)
	}
	return New(cfg), f
}

func decodeTile(t *testing.T, b []byte) *image.NRGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return toNRGBA(img)
}

// A tile of a deep zoom is built from the source zoom only: NOAA is never asked for a finer picture, and neighbouring derived tiles agree at their
// shared edge (the margin read across the tile boundary is what makes that so).
func TestSmoothTilesComeFromTheSourceZoomAndMeetAtTheEdges(t *testing.T) {
	for _, src := range []int{9, 7} {
		for _, blur := range []int{0, 2} {
			s, f := newRamp(t, func(c *Config) { c.SmoothSource = src; c.SmoothBlur = blur })
			z := 12
			x, y := 227<<(z-10)+3, 387<<(z-10)+1 // a tile over the central US and its neighbour to the right
			a, _, err := s.Tile(context.Background(), z, x, y, 0)
			if err != nil {
				t.Fatal(err)
			}
			b, _, err := s.Tile(context.Background(), z, x+1, y, 0)
			if err != nil {
				t.Fatal(err)
			}
			ia, ib := decodeTile(t, a), decodeTile(t, b)
			if ia.Bounds().Dx() != TileSize || ia.Bounds().Dy() != TileSize {
				t.Fatalf("not a 256 tile")
			}
			for row := 0; row < TileSize; row += 17 {
				l, r := int(ia.NRGBAAt(TileSize-1, row).R), int(ib.NRGBAAt(0, row).R)
				if d := l - r; d > 1 || d < -1 {
					t.Fatalf("src %d blur %d: edge %d vs %d at row %d", src, blur, l, r, row)
				}
				if ia.NRGBAAt(5, row).A != 255 {
					t.Fatalf("alpha %d inside an opaque source", ia.NRGBAAt(5, row).A)
				}
			}
			_, _, _, maxX := tileMercator(src, 0, 0)
			want := 2 * maxX / float64(int(1)<<src)
			f.wmu.Lock()
			for _, w := range f.widths {
				if w < want*0.999 || w > want*1.001 {
					t.Errorf("src %d: NOAA asked for a %.0f m wide box, want only the source zoom's %.0f", src, w, want)
				}
			}
			n := len(f.widths)
			f.wmu.Unlock()
			if n == 0 || n > 9 {
				t.Errorf("src %d: %d upstream requests for two deep tiles", src, n)
			}
		}
	}
}

func TestSmoothIsOffUnlessAsked(t *testing.T) {
	s, _ := newRamp(t, func(c *Config) { c.Smooth = false })
	if _, _, err := s.Tile(context.Background(), 11, 300, 700, 0); err == nil {
		t.Errorf("z11 was served with smoothing off")
	}
	if got := s.ClientMaxZoom(); got != ClientMaxZoom {
		t.Errorf("client max zoom %d with smoothing off", got)
	}
	if _, _, _, ok := ParseTilePath("/11/1/1.png"); ok {
		t.Errorf("the plain route parser admitted z11")
	}
	on, _ := newRamp(t, nil)
	if on.ClientMaxZoom() != SmoothMaxZoom {
		t.Errorf("client max zoom %d with smoothing on", on.ClientMaxZoom())
	}
	if _, _, err := on.Tile(context.Background(), SmoothMaxZoom+1, 1, 1, 0); err == nil {
		t.Errorf("served past the max zoom")
	}
}

func TestSmoothKeepsTransparentWhereThereIsNoEcho(t *testing.T) {
	s, _ := newSvcSmoothOutside(t)
	b, _, err := s.Tile(context.Background(), 11, 0, 0, 0) // far outside every mosaic
	if err != nil {
		t.Fatal(err)
	}
	img := decodeTile(t, b)
	for _, p := range []image.Point{{0, 0}, {128, 128}, {255, 255}} {
		if img.NRGBAAt(p.X, p.Y).A != 0 {
			t.Fatalf("opaque pixel at %v outside every mosaic", p)
		}
	}
}

func newSvcSmoothOutside(t *testing.T) (*Service, *rampNOAA) { return newRamp(t, nil) }

func TestSmoothHandlerAdvertisesAndServesDeepTiles(t *testing.T) {
	s, _ := newRamp(t, nil)
	h := s.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/frame", nil))
	if !strings.Contains(rec.Body.String(), `"max_zoom":12`) {
		t.Errorf("frame answer: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/12/911/1549.png", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("deep tile: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/13/1/1.png", nil))
	if rec.Code != 404 {
		t.Errorf("z13: %d", rec.Code)
	}
}
