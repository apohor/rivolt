package api

import (
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/google/uuid"
)

func TestMapsUserLimiterIsPerUser(t *testing.T) {
	t.Parallel()
	h := mapsUserLimiter(2).Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	do := func(uid uuid.UUID, addr string) int {
		r := httptest.NewRequest("POST", "/route", nil).WithContext(auth.WithUser(httptest.NewRequest("GET", "/", nil).Context(), uid))
		r.RemoteAddr = addr
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	a, b := uuid.New(), uuid.New()
	for i := 0; i < 2; i++ {
		if c := do(a, "1.1.1.1:1"); c != 204 {
			t.Fatalf("burst %d = %d", i, c)
		}
	}
	// Same user from a new IP is still limited; another user is not.
	if c := do(a, "2.2.2.2:1"); c != 429 {
		t.Fatalf("user a from new IP = %d, want 429", c)
	}
	if c := do(b, "1.1.1.1:1"); c != 204 {
		t.Fatalf("user b = %d, want 204", c)
	}
}

// The mounts are exercised with the public paths the SPA calls, through chi
// like api.go does, against a stub upstream. (The allowlist once ran before the
// prefix was stripped and 404ed every map request.)
func TestMapMountsServeThePublicPaths(t *testing.T) {
	t.Parallel()
	var got []string
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.WriteHeader(200)
	})
	r := chi.NewRouter()
	r.Mount("/api/maps/valhalla", valhallaMount(up))
	r.Mount("/api/maps/tiles", tilesMount(up))
	do := func(method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Range", "bytes=0-16383")
		req = req.WithContext(auth.WithUser(req.Context(), uuid.New()))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/maps/valhalla/route", 200},
		{"POST", "/api/maps/valhalla/trace_route", 200},
		{"GET", "/api/maps/tiles/us.pmtiles", 200},
		{"GET", "/api/maps/tiles/chargers.pmtiles", 200},
		{"HEAD", "/api/maps/tiles/us.pmtiles", 200},
		{"GET", "/api/maps/tiles/a/us.pmtiles", 404},
		{"GET", "/api/maps/tiles/", 404},
		{"GET", "/api/maps/valhalla/status", 404},
		{"POST", "/api/maps/valhalla/isochrone", 404},
		{"GET", "/api/maps/valhalla/route", 404},
		{"POST", "/api/maps/valhalla/sources_to_targets", 404},
		{"GET", "/api/maps/tiles/index.html", 404},
		{"POST", "/api/maps/tiles/us.pmtiles", 404},
	} {
		if code := do(c.method, c.path); code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, code, c.want)
		}
	}
	want := []string{"POST /route", "POST /trace_route", "GET /us.pmtiles", "GET /chargers.pmtiles", "HEAD /us.pmtiles"}
	if len(got) != len(want) {
		t.Fatalf("upstream saw %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("upstream saw %q, want %q", got[i], want[i])
		}
	}
}

// Past the per-user ceiling the mounted route answers 429, per user.
func TestMapMountsLimitPerUser(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	r := chi.NewRouter()
	r.Mount("/api/maps/valhalla", valhallaMount(up))
	call := func(uid uuid.UUID) int {
		req := httptest.NewRequest("POST", "/api/maps/valhalla/route", nil)
		req = req.WithContext(auth.WithUser(req.Context(), uid))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	a := uuid.New()
	for i := 0; i < mapsValhallaPerMin; i++ {
		if c := call(a); c != 200 {
			t.Fatalf("request %d = %d", i+1, c)
		}
	}
	if c := call(a); c != 429 {
		t.Fatalf("request %d = %d, want 429", mapsValhallaPerMin+1, c)
	}
	if c := call(uuid.New()); c != 200 {
		t.Fatalf("another user = %d, want 200", c)
	}
}

func TestChargersAlongRouteIsLimitedPerUser(t *testing.T) {
	t.Parallel()
	if mapsChargersAlongPerMin <= 0 || mapsChargersAlongPerMin > 120 {
		t.Fatalf("chargers-along ceiling %d out of range", mapsChargersAlongPerMin)
	}
	h := mapsUserLimiter(mapsChargersAlongPerMin).Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	uid := uuid.New()
	var last int
	for i := 0; i <= mapsChargersAlongPerMin; i++ {
		r := httptest.NewRequest("POST", "/api/maps/chargers-along", nil)
		r = r.WithContext(auth.WithUser(r.Context(), uid))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		last = w.Code
	}
	if last != 429 {
		t.Fatalf("request %d = %d, want 429", mapsChargersAlongPerMin+1, last)
	}
}
