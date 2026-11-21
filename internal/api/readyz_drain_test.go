package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/apohor/rivolt/internal/livekeys"
)

// /readyz is 200 until the pod starts to drain and 503 from then on, so a draining pod is out of the Service's endpoints whether or not the
// kubelet has marked it Terminating yet.
func TestReadyzFailsFromTheStartOfTheDrain(t *testing.T) {
	t.Cleanup(func() { draining.Store(false) })
	draining.Store(false)
	h := handleReadyz(nil)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("before the drain: %d, want 200", rec.Code)
	}
	StartDrain()
	StartDrain() // idempotent
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("while draining: %d, want 503", rec.Code)
	}
}

// A pod whose live-keys file is present but invalid, with no good list to keep, is not ready (a roll stalls and the old pods keep serving);
// the poll after the file is fixed makes it ready again, and a bad file later does not take a running pod out.
func TestReadyzFollowsTheDesignKeyStore(t *testing.T) {
	t.Cleanup(func() { SetDesignKeysReady(nil) })
	p := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(p, []byte("design_preview=now v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ks, err := livekeys.New(p, "fallback_key", nil)
	if err != nil {
		t.Fatal(err)
	}
	SetDesignKeysReady(ks.Ready)
	code := func() int {
		rec := httptest.NewRecorder()
		handleReadyz(nil)(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	if c := code(); c != http.StatusServiceUnavailable {
		t.Fatalf("invalid file at start (a valid env does not rescue it): %d, want 503", c)
	}
	if err := os.WriteFile(p, []byte("design_preview=a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ks.Reload()
	if c := code(); c != http.StatusOK {
		t.Fatalf("after the fix: %d, want 200", c)
	}
	if err := os.WriteFile(p, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	ks.Reload()
	if c := code(); c != http.StatusOK {
		t.Fatalf("a bad file while running keeps the pod ready: %d, want 200", c)
	}
}
