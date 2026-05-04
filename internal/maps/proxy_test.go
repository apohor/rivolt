package maps

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewOSRMProxy_emptyURLReturnsNil(t *testing.T) {
	t.Parallel()
	h, err := NewOSRMProxy("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h != nil {
		t.Fatalf("expected nil handler for empty URL")
	}
}

func TestNewOSRMProxy_invalidURLRejected(t *testing.T) {
	t.Parallel()
	if _, err := NewOSRMProxy("nohost"); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("want ErrInvalidURL, got %v", err)
	}
}

func TestOSRMProxy_forwardsAndStripsCreds(t *testing.T) {
	t.Parallel()

	var gotPath, gotCookie, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotCookie = r.Header.Get("Cookie")
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	h, err := NewOSRMProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewOSRMProxy: %v", err)
	}

	// Mounted at /api/maps/osrm, path stripped before reaching us.
	mux := http.NewServeMux()
	mux.Handle("/api/maps/osrm/", http.StripPrefix("/api/maps/osrm", h))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/maps/osrm/match/v1/driving/0,0;1,1?geometries=geojson", nil)
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
	if !strings.HasPrefix(gotPath, "/match/v1/driving/0,0;1,1") {
		t.Fatalf("path forwarded wrong: %q", gotPath)
	}
	if gotCookie != "" {
		t.Fatalf("cookie leaked to upstream: %q", gotCookie)
	}
	if gotAuth != "" {
		t.Fatalf("auth leaked to upstream: %q", gotAuth)
	}
}
