package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
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
	h, _ := demoChain(newDemoLimiter(), nil)
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
