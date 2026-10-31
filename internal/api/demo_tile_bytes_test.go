package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/maps"
)

func TestDemoRangeLen(t *testing.T) {
	for h, want := range map[string]int{
		"bytes=0-16383": 16384, "bytes=5-5": 1, "bytes=-1024": 1024, "": 0, "bytes=9-1": 0, "items=0-1": 0, "bytes=a-b": 0,
	} {
		if got := demoRangeLen(h); got != want {
			t.Errorf("demoRangeLen(%q) = %d, want %d", h, got, want)
		}
	}
}

func TestDemoTileMaxRangeEnv(t *testing.T) {
	if got := demoTileMaxRange(); got != 512<<10 {
		t.Errorf("default = %d, want 512 KiB", got)
	}
	t.Setenv("RIVOLT_DEMO_TILE_MAX_RANGE_KB", "1024")
	if got := demoTileMaxRange(); got != 1024<<10 {
		t.Errorf("env 1024 = %d", got)
	}
	t.Setenv("RIVOLT_DEMO_TILE_MAX_RANGE_KB", "99999")
	if got := demoTileMaxRange(); got != 2048<<10 {
		t.Errorf("env 99999 = %d, want clamped to 2 MiB", got)
	}
	t.Setenv("RIVOLT_DEMO_TILE_MAX_RANGE_KB", "nope")
	if got := demoTileMaxRange(); got != 512<<10 {
		t.Errorf("bad env = %d, want the default", got)
	}
}

func TestDemoTileByteBudgetDefaults(t *testing.T) {
	c, g := demoTileByteBudgets()
	if c != 200<<20 || g != 3000<<20 {
		t.Errorf("defaults = %d, %d", c, g)
	}
	t.Setenv("RIVOLT_DEMO_TILES_MB_PER_CLIENT_PER_HOUR", "50")
	t.Setenv("RIVOLT_DEMO_TILES_MB_GLOBAL_PER_HOUR", "0")
	if c, g := demoTileByteBudgets(); c != 50<<20 || g != 3000<<20 {
		t.Errorf("env = %d, %d (a bad value must fall back to the default)", c, g)
	}
}

// The byte budget charges each read its range: a client that keeps asking for the
// largest read runs dry long before the request budget, with a Retry-After, while
// small reads and other clients carry on; the install-wide byte cap stops everyone.
func TestDemoTileByteBudgets(t *testing.T) {
	t.Setenv("RIVOLT_DEMO_TILES_MB_PER_CLIENT_PER_HOUR", "2") // four 512 KiB reads
	t.Setenv("RIVOLT_DEMO_TILES_MB_GLOBAL_PER_HOUR", "5")
	h, _ := demoChain(newDemoLimiter(nil, "t"), nil)
	read := func(client, rng string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/maps/tiles/us.pmtiles", nil)
		req.RemoteAddr = client + ":4000"
		req.Header.Set("Range", rng)
		h.ServeHTTP(rec, req)
		return rec
	}
	const big = "bytes=0-524287"
	for i := 0; i < 4; i++ {
		if rec := read("203.0.113.9", big); rec.Code != http.StatusOK {
			t.Fatalf("read %d: %d", i, rec.Code)
		}
	}
	rec := read("203.0.113.9", big)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("fifth big read: %d, want 429", rec.Code)
	}
	if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	// A small read still fits what is left (the bucket refills continuously), and
	// another client is unaffected until the install-wide cap.
	if rec := read("203.0.113.10", big); rec.Code != http.StatusOK {
		t.Errorf("another client: %d", rec.Code)
	}
	// Install-wide: 5 MB in all; 2.5 MB are spent, so single reads from fresh clients
	// (each far under its own budget) are stopped after about five more.
	stopped := 0
	for i := 0; i < 20; i++ {
		if read("203.0.113."+strconv.Itoa(100+i), big).Code == http.StatusTooManyRequests {
			stopped++
		}
	}
	if stopped == 0 || stopped > 16 {
		t.Errorf("%d of 20 fresh clients stopped by the install-wide byte cap, want most after the first few", stopped)
	}
	if rec := read("203.0.113.200", big); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a fresh client after the install-wide cap: %d, want 429", rec.Code)
	}
}

// Through the real router with full URLs, as the public demo runs: anonymous,
// bounded ranges only, and the byte budget answers 429 + Retry-After.
func TestDemoTileByteBudgetThroughTheRouter(t *testing.T) {
	demoOnlyEnv(t)
	t.Setenv("RIVOLT_DEMO_TILES_MB_PER_CLIENT_PER_HOUR", "1") // two 512 KiB reads
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true})
	read := func(rng string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "https://demo.rivolt.dev/api/maps/tiles/us.pmtiles", nil)
		req.RemoteAddr = "203.0.113.77:4000"
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := read(""); rec.Code != http.StatusBadRequest {
		t.Errorf("no range: %d, want 400", rec.Code)
	}
	if rec := read("bytes=0-524288"); rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("one byte over 512 KiB: %d, want 416", rec.Code)
	}
	for i := 0; i < 2; i++ {
		if code := read("bytes=0-524287").Code; code == http.StatusTooManyRequests || code == http.StatusUnauthorized || code == http.StatusBadRequest {
			t.Fatalf("read %d: %d, want it let through", i, code)
		}
	}
	rec := read("bytes=0-524287")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("third big read: %d Retry-After=%q, want 429 with one", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// DA's bypass, through the real router with a real reverse proxy in front of a tile
// server that behaves like a static file server (a mismatched If-Range is a 200 of
// the whole file): the public demo's anonymous read is still the 206 of exactly the
// bytes asked for, and an upstream that answers a range with a 200 is refused.
func TestDemoTileReadsCannotBeTurnedIntoTheWholeFile(t *testing.T) {
	demoOnlyEnv(t)
	file := bytes.Repeat([]byte("0123456789"), 500_000) // 5 MB
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		if rng == "" || r.Header.Get("If-Range") != "" || r.Header.Get("If-Match") != "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(file)))
			_, _ = w.Write(file)
			return
		}
		var a, b int
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &a, &b); err != nil || a > b || b >= len(file) {
			http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(file)))
		w.Header().Set("Content-Length", strconv.Itoa(b-a+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(file[a : b+1])
	}))
	defer upstream.Close()
	proxy, err := maps.NewProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, TilesProxy: proxy})
	read := func(hdr map[string]string) (int, int) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "https://demo.rivolt.dev/api/maps/tiles/us.pmtiles", nil)
		req.RemoteAddr = "203.0.113.88:4000"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		router.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Body)
		return rec.Code, len(body)
	}
	if code, n := read(map[string]string{"Range": "bytes=0-9"}); code != http.StatusPartialContent || n != 10 {
		t.Fatalf("a plain range read: %d with %d bytes, want a 206 of 10", code, n)
	}
	for _, h := range []string{"If-Range", "If-Match"} {
		if code, n := read(map[string]string{"Range": "bytes=0-9", h: `"bogus"`}); code != http.StatusPartialContent || n != 10 {
			t.Errorf("%s: %d with %d bytes, want a 206 of 10, not the whole file", h, code, n)
		}
	}
}

// An upstream that answers a range read with a 200 (whatever the reason) is refused,
// and none of its body reaches the visitor.
func TestDemoTileUpstream200IsRefused(t *testing.T) {
	demoOnlyEnv(t)
	whole := bytes.Repeat([]byte("x"), 3<<20)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(whole)))
		_, _ = w.Write(whole)
	}))
	defer upstream.Close()
	proxy, err := maps.NewProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, TilesProxy: proxy})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "https://demo.rivolt.dev/api/maps/tiles/us.pmtiles", nil)
	req.RemoteAddr = "203.0.113.89:4000"
	req.Header.Set("Range", "bytes=0-9")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || rec.Body.Len() > 100 {
		t.Errorf("an upstream 200: %d with %d body bytes, want a 502 with none of the file", rec.Code, rec.Body.Len())
	}
}
