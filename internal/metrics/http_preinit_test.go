package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The billing routes' error series exist at 0 from boot, so the first error on a fresh pod is an increase (a series born at 1 shows no change to
// increase() or rate(): found in flip rehearsal 2, where the pre-flight's first unsigned POST per pod, a 400, was invisible to the webhook panel).
func TestBillingRouteErrorSeriesExistAtZeroFromBoot(t *testing.T) {
	m := New()
	for route, statuses := range BillingRouteStatuses {
		for _, status := range statuses {
			if got := testutil.ToFloat64(m.HTTPRequestsTotal.WithLabelValues("POST", route, status)); got != 0 {
				t.Errorf("%s %s = %v at boot, want 0", route, status, got)
			}
		}
	}
	want := 0
	for _, statuses := range BillingRouteStatuses {
		want += len(statuses)
	}
	if n := testutil.CollectAndCount(m.HTTPRequestsTotal); n < want {
		t.Errorf("%d series at boot, want at least the %d billing ones", n, want)
	}
	for _, status := range BillingRouteStatuses["/api/billing/stripe/webhook"] {
		if status[0] != '4' && status[0] != '5' {
			t.Errorf("webhook status %s is not an error the alerts watch (4.. or 5..)", status)
		}
	}
}

// The pre-created series are the ones the middleware increments: same labels (method, chi route pattern, numeric status).
func TestPreCreatedSeriesAreTheOnesTheMiddlewareCounts(t *testing.T) {
	m := New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware)
	r.Post("/api/billing/stripe/webhook", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	before := testutil.CollectAndCount(m.HTTPRequestsTotal)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/billing/stripe/webhook", nil))
	if got := testutil.ToFloat64(m.HTTPRequestsTotal.WithLabelValues("POST", "/api/billing/stripe/webhook", "400")); got != 1 {
		t.Errorf("the first 400 reads %v, want 1", got)
	}
	if after := testutil.CollectAndCount(m.HTTPRequestsTotal); after != before {
		t.Errorf("the request created a new series (%d -> %d): the pre-created one has other labels", before, after)
	}
}
