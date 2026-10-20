package maps

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewProxy_emptyURLReturnsNil(t *testing.T) {
	t.Parallel()
	h, err := NewProxy("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h != nil {
		t.Fatalf("expected nil handler for empty URL")
	}
}

func TestNewProxy_invalidURLRejected(t *testing.T) {
	t.Parallel()
	if _, err := NewProxy("nohost"); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("want ErrInvalidURL, got %v", err)
	}
}

func TestProxy_forwardsAndStripsCreds(t *testing.T) {
	t.Parallel()

	var gotPath, gotCookie, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotCookie = r.Header.Get("Cookie")
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	h, err := NewProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}

	// Mounted at /api/maps/valhalla, path stripped before reaching us.
	mux := http.NewServeMux()
	mux.Handle("/api/maps/valhalla/", http.StripPrefix("/api/maps/valhalla", h))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/api/maps/valhalla/route?json=%7B%7D", nil)
	req.Header.Set("Cookie", "rivolt_session=should-not-leak")
	req.Header.Set("Authorization", "Bearer should-not-leak")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.HasPrefix(gotPath, "/route") {
		t.Fatalf("path forwarded wrong: %q", gotPath)
	}
	if gotCookie != "" {
		t.Fatalf("cookie leaked to upstream: %q", gotCookie)
	}
	if gotAuth != "" {
		t.Fatalf("auth leaked to upstream: %q", gotAuth)
	}
}

// The upstream gets no caller address: not X-Forwarded-For (the old Director
// forwarded the inbound chain and appended the caller), not Forwarded, and none of
// the headers a CDN or ingress uses for it. Query, base path, Range and the upstream
// Host still come through.
func TestProxy_sendsNoClientAddress(t *testing.T) {
	t.Parallel()
	var got http.Header
	var gotURI, gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotURI, gotHost = r.Header.Clone(), r.URL.RequestURI(), r.Host
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer upstream.Close()

	h, err := NewProxy(upstream.URL + "/base") // a base path on the target is joined
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	req := httptest.NewRequest("GET", "/us.pmtiles?v=2", nil)
	req.RemoteAddr = "203.0.113.9:4444"
	for k, v := range map[string]string{
		"X-Forwarded-For": "198.51.100.7, 10.0.0.1", "X-Forwarded-Host": "rivolt.dev", "X-Forwarded-Proto": "https",
		"Forwarded": "for=198.51.100.7", "X-Real-Ip": "198.51.100.7", "X-Client-Ip": "198.51.100.7",
		"True-Client-Ip": "198.51.100.7", "Cf-Connecting-Ip": "198.51.100.7", "Cf-Ipcountry": "US",
		"Cookie": "rivolt_session=x", "Authorization": "Bearer x", "Range": "bytes=0-16383",
	} {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, k := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "X-Real-Ip", "X-Client-Ip",
		"True-Client-Ip", "Cf-Connecting-Ip", "Cf-Ipcountry", "Cookie", "Authorization"} {
		if v := got.Get(k); v != "" {
			t.Errorf("upstream received %s: %q", k, v)
		}
	}
	for _, v := range got.Values("X-Forwarded-For") {
		if strings.Contains(v, "203.0.113.9") {
			t.Errorf("the caller's address reached the upstream: %q", v)
		}
	}
	if got.Get("Range") != "bytes=0-16383" {
		t.Errorf("Range = %q, want it passed through", got.Get("Range"))
	}
	if gotURI != "/base/us.pmtiles?v=2" {
		t.Errorf("upstream URI = %q, want /base/us.pmtiles?v=2", gotURI)
	}
	if want := strings.TrimPrefix(upstream.URL, "http://"); gotHost != want {
		t.Errorf("upstream Host = %q, want %q", gotHost, want)
	}
}
