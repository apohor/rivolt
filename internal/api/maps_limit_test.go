package api

import (
	"github.com/go-chi/chi/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/maps"
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
		if r.Method == http.MethodGet && r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusPartialContent) // a range read is a 206; the tile guard refuses a 200
			return
		}
		w.WriteHeader(200)
	})
	r := chi.NewRouter()
	r.Mount("/api/maps/valhalla", valhallaMount(up))
	r.Mount("/api/maps/tiles", tilesMount(up))
	do := func(method, path string) int {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"locations":[]}`)
		}
		req := httptest.NewRequest(method, path, body)
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
		{"GET", "/api/maps/tiles/us.pmtiles", 206},
		{"GET", "/api/maps/tiles/chargers.pmtiles", 206},
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

// The size caps run on the mounted route (public path, through chi): an oversized trace is refused before Valhalla,
// one at the cap reaches it.
func TestMapMountsCapTheTrace(t *testing.T) {
	t.Parallel()
	hits := 0
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; w.WriteHeader(200) })
	r := chi.NewRouter()
	r.Mount("/api/maps/valhalla", valhallaMount(up))
	trace := func(n int) int {
		var b strings.Builder
		b.WriteString(`{"shape":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"lat":32.7767,"lon":-96.797}`)
		}
		b.WriteString(`],"costing":"auto","shape_match":"walk_or_snap"}`)
		req := httptest.NewRequest("POST", "/api/maps/valhalla/trace_route", strings.NewReader(b.String()))
		req = req.WithContext(auth.WithUser(req.Context(), uuid.New()))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := trace(maps.MaxTraceShapePoints + 1); code != http.StatusRequestEntityTooLarge || hits != 0 {
		t.Fatalf("over the cap: %d, upstream hit %d times", code, hits)
	}
	if code := trace(maps.MaxTraceShapePoints); code != 200 || hits != 1 {
		t.Fatalf("at the cap: %d, upstream hit %d times", code, hits)
	}
}

// Past the per-user ceiling the mounted route answers 429, per user.
func TestMapMountsLimitPerUser(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	r := chi.NewRouter()
	r.Mount("/api/maps/valhalla", valhallaMount(up))
	call := func(uid uuid.UUID) int {
		req := httptest.NewRequest("POST", "/api/maps/valhalla/route", strings.NewReader(`{"locations":[]}`))
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

func TestOvernightCandidatesRouteIsLimitedPerUser(t *testing.T) {
	t.Parallel()
	if tripsOvernightPerMin <= 0 || tripsOvernightPerMin > 120 {
		t.Fatalf("overnight-candidates ceiling %d out of range", tripsOvernightPerMin)
	}
	h := mapsUserLimiter(tripsOvernightPerMin).Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	call := func(uid uuid.UUID) int {
		r := httptest.NewRequest("POST", "/api/trips/overnight-candidates", nil)
		r = r.WithContext(auth.WithUser(r.Context(), uid))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	uid := uuid.New()
	for i := 0; i < tripsOvernightPerMin; i++ {
		if c := call(uid); c != 204 {
			t.Fatalf("request %d = %d, want 204", i+1, c)
		}
	}
	if c := call(uid); c != 429 {
		t.Fatalf("request %d = %d, want 429", tripsOvernightPerMin+1, c)
	}
	if c := call(uuid.New()); c != 204 {
		t.Fatalf("another user = %d, want 204", c)
	}
}

// The real proxy behind the real mounts, called with the public URLs: the allowlist
// still lets the SPA's calls through, the path and query reach the upstream
// unchanged after the prefix is stripped, and the upstream gets neither the
// caller's address nor the session credentials. (#705: the proxy moved from the
// deprecated Director to Rewrite, which changes what is forwarded.)
func TestMapProxyThroughTheRouterSendsNoClientAddress(t *testing.T) {
	t.Parallel()
	type seen struct {
		uri    string
		header http.Header
	}
	var calls []seen
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, seen{r.URL.RequestURI(), r.Header.Clone()})
		mu.Unlock()
		if r.Method == http.MethodGet && r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusPartialContent)
			return
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	proxy, err := maps.NewProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Mount("/api/maps/valhalla", valhallaMount(proxy))
	r.Mount("/api/maps/tiles", tilesMount(proxy))
	front := httptest.NewServer(r)
	defer front.Close()

	do := func(method, path string) int {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"locations":[]}`)
		}
		req, _ := http.NewRequest(method, front.URL+path, body)
		req.Header.Set("X-Forwarded-For", "198.51.100.7")
		req.Header.Set("X-Real-Ip", "198.51.100.7")
		req.Header.Set("Cf-Connecting-Ip", "198.51.100.7")
		req.Header.Set("Cookie", "rivolt_session=x")
		req.Header.Set("Authorization", "Bearer x")
		req.Header.Set("Range", "bytes=0-16383")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// No user in the context: the limiter keys on the address, which is fine here.
	if c := do("POST", "/api/maps/valhalla/route"); c != 200 {
		t.Fatalf("route = %d", c)
	}
	// Valhalla reads a whole request from ?json=: a query string is refused before the proxy.
	if c := do("POST", "/api/maps/valhalla/route?json=%7B%7D"); c != 400 {
		t.Fatalf("route with ?json= = %d, want 400", c)
	}
	if c := do("GET", "/api/maps/tiles/us.pmtiles"); c != 206 {
		t.Fatalf("tiles = %d, want the 206 of the range", c)
	}
	if c := do("GET", "/api/maps/tiles/a/us.pmtiles"); c != 404 {
		t.Fatalf("a nested tile path = %d, want 404 from the allowlist", c)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("upstream saw %d calls, want 2 (the refused path must not reach it): %v", len(calls), calls)
	}
	if calls[0].uri != "/route" || calls[1].uri != "/us.pmtiles" {
		t.Errorf("upstream URIs = %q, %q; want the stripped paths", calls[0].uri, calls[1].uri)
	}
	for _, c := range calls {
		for _, k := range []string{"X-Forwarded-For", "X-Real-Ip", "Cf-Connecting-Ip", "Cookie", "Authorization", "Forwarded"} {
			if v := c.header.Get(k); v != "" {
				t.Errorf("%s: upstream received %s = %q", c.uri, k, v)
			}
		}
	}
	if calls[1].header.Get("Range") != "bytes=0-16383" {
		t.Errorf("Range not passed to the tile server: %q", calls[1].header.Get("Range"))
	}
}
