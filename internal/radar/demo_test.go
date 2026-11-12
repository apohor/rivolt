package radar

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// A box over southern Nevada and the Mojave: a handful of tiles at z4..7.
var testBox = Box{W: -116, S: 35, E: -114.5, N: 36.5}

func newDemoOver(t *testing.T, f *fakeNOAA, redisAddr string) (*Demo, *map[string]int) {
	t.Helper()
	counts := map[string]int{}
	svc := newSvc(t, f, func(c *Config) {
		if redisAddr != "" {
			rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
			t.Cleanup(func() { _ = rdb.Close() })
			c.Redis, c.Prefix = rdb, "rivolt"
		}
	})
	return NewDemo(svc, []Box{testBox}, func(r string) { counts[r]++ }, nil), &counts
}

func demoGet(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestDemoTileSetIsFixedAndCapped(t *testing.T) {
	d, _ := newDemoOver(t, &fakeNOAA{t: t, newest: t0}, "")
	if d.Len() == 0 || d.Len() > DemoMaxTiles {
		t.Fatalf("tile set %d", d.Len())
	}
	z, x, y := 5, 0, 0
	for tl := range d.set {
		if tl.z < DemoMinZoom || tl.z > DemoMaxZoom {
			t.Errorf("tile %v outside the zoom range", tl)
		}
		z, x, y = tl.z, tl.x, tl.y
	}
	if !d.In(z, x, y) || d.In(5, 0, 0) {
		t.Error("In")
	}
	// A box covering the world is capped, not honored.
	big := NewDemo(d.svc, []Box{{W: -179, S: -80, E: 179, N: 80}}, nil, nil)
	if big.Len() != DemoMaxTiles {
		t.Errorf("world box gave %d tiles, want the cap %d", big.Len(), DemoMaxTiles)
	}
}

// Before anything is warmed a request is an empty tile and costs no capabilities request, no render and no upstream tile: the visitor cannot
// make this server call NOAA.
func TestDemoMissIsEmptyAndFree(t *testing.T) {
	f := &fakeNOAA{t: t, newest: t0}
	d, counts := newDemoOver(t, f, "")
	var tile demoTile
	for tl := range d.set {
		tile = tl
		break
	}
	h := d.Handler()
	path := "/" + itoa(tile.z) + "/" + itoa(tile.x) + "/" + itoa(tile.y) + ".png"
	rec := demoGet(h, http.MethodGet, path+"?t=1")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() != len(transparentPNG) {
		t.Fatalf("miss: %d %q %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if !strings.HasPrefix(rec.Header().Get("Cache-Control"), "public, max-age=") || rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("headers: %v", rec.Header())
	}
	if rec := demoGet(h, http.MethodGet, "/frame"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("frame before warm: %d", rec.Code)
	}
	if len(f.maps()) != 0 || f.capsHits.Load() != 0 {
		t.Errorf("a miss reached NOAA: %d tiles, %d capabilities", len(f.maps()), f.capsHits.Load())
	}
	if (*counts)["miss"] != 1 || (*counts)["hit"] != 0 {
		t.Errorf("counts %v", *counts)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestDemoWarmThenServe(t *testing.T) {
	for _, withRedis := range []bool{false, true} {
		addr := ""
		if withRedis {
			addr = miniredis.RunT(t).Addr()
		}
		f := &fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute)}
		d, counts := newDemoOver(t, f, addr)
		if n := d.Warm(context.Background()); n != d.Len() {
			t.Fatalf("redis=%v: warmed %d of %d", withRedis, n, d.Len())
		}
		fetched := len(f.maps())
		h := d.Handler()
		var tile demoTile
		for tl := range d.set {
			if len(regionsFor(tl.z, tl.x, tl.y)) > 0 {
				tile = tl
				break
			}
		}
		rec := demoGet(h, http.MethodGet, "/"+itoa(tile.z)+"/"+itoa(tile.x)+"/"+itoa(tile.y)+".png")
		if rec.Code != 200 || rec.Header().Get("X-Radar-Frame") == "" || bytes.Equal(rec.Body.Bytes(), transparentPNG) {
			t.Fatalf("redis=%v: hit: %d frame %q %d bytes", withRedis, rec.Code, rec.Header().Get("X-Radar-Frame"), rec.Body.Len())
		}
		fr := demoGet(h, http.MethodGet, "/frame")
		if fr.Code != 200 || !strings.Contains(fr.Body.String(), `"min_zoom":4`) || !strings.Contains(fr.Body.String(), `"frames_unix"`) {
			t.Errorf("redis=%v: frame: %d %s", withRedis, fr.Code, fr.Body.String())
		}
		if len(f.maps()) != fetched {
			t.Errorf("redis=%v: serving fetched from NOAA", withRedis)
		}
		if (*counts)["hit"] != 1 {
			t.Errorf("redis=%v: counts %v", withRedis, *counts)
		}
	}
}

func TestDemoRefusals(t *testing.T) {
	f := &fakeNOAA{t: t, newest: t0}
	d, _ := newDemoOver(t, f, "")
	h := d.Handler()
	for path, want := range map[string]int{
		"/0/0/0.png":     404, // outside the zoom range
		"/5/0/0.png":     404, // not over a sample trip
		"/nope":          404,
		"/frame?x=1":     400,
		"/frame?t=abc":   400,
		"/frame?t=1&t=2": 400,
	} {
		if rec := demoGet(h, http.MethodGet, path); rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
	if rec := demoGet(h, http.MethodPost, "/frame"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
	if len(f.maps()) != 0 || f.capsHits.Load() != 0 {
		t.Error("a refusal reached NOAA")
	}
}

// Across pods one warms a round, and a frame already stored is not fetched again.
func TestDemoWarmIsOnePodAndOncePerFrame(t *testing.T) {
	mr := miniredis.RunT(t)
	f := &fakeNOAA{t: t, newest: t0.Add(-3 * time.Minute)}
	a, _ := newDemoOver(t, f, mr.Addr())
	b := NewDemo(a.svc, []Box{testBox}, nil, nil)
	n := a.Warm(context.Background())
	if n == 0 {
		t.Fatal("first pod warmed nothing")
	}
	fetched := len(f.maps())
	if got := b.Warm(context.Background()); got != 0 {
		t.Errorf("second pod warmed %d in the same round", got)
	}
	mr.Del(a.svc.rkey("demo", "warm-lock"))
	if got := a.Warm(context.Background()); got != 0 || len(f.maps()) != fetched {
		t.Errorf("same frame fetched again: %d stored, %d new upstream requests", got, len(f.maps())-fetched)
	}
	// The other pod sees the stored frame.
	if _, ok := b.warmedFrame(context.Background()); !ok {
		t.Error("the second pod does not see the warmed frame")
	}
}
