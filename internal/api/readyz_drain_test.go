package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
