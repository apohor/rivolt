package api

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/applebill"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/stripebill"
)

// The series exist at 0 after boot: a pod's first error is then an increase of 1, not a series born at 1 that increase() cannot see.
func TestBillingErrorSeriesExistAtZeroAfterBoot(t *testing.T) {
	m := metrics.New()
	if n := testutil.CollectAndCount(m.HTTPRequestsTotal); n != 0 {
		t.Fatalf("%d series before boot", n)
	}
	preinitBillingSeries(m)
	want := 0
	for _, s := range billingErrorSeries {
		for _, status := range s.statuses {
			want++
			if got := testutil.ToFloat64(m.HTTPRequestsTotal.WithLabelValues(s.method, s.route, status)); got != 0 {
				t.Errorf("%s %s %s = %v, want 0", s.method, s.route, status, got)
			}
		}
	}
	if got := testutil.CollectAndCount(m.HTTPRequestsTotal); got != want {
		t.Errorf("%d series, want %d", got, want)
	}
	// Idempotent, and a counted request still adds to the same child.
	preinitBillingSeries(m)
	m.HTTPRequestsTotal.WithLabelValues("POST", "/api/billing/stripe/webhook", "500").Inc()
	if got := testutil.ToFloat64(m.HTTPRequestsTotal.WithLabelValues("POST", "/api/billing/stripe/webhook", "500")); got != 1 {
		t.Errorf("first error = %v, want 1", got)
	}
	preinitBillingSeries(nil) // no metrics: no panic
}

// The route strings are exactly the router's registered patterns for those methods, so a renamed or moved route fails here instead of
// quietly leaving its series (and the alert that reads them) pointing at nothing. New also creates the series itself.
func TestBillingErrorSeriesRoutesAreTheRoutersPatterns(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	m := metrics.New()
	h := New(Deps{
		Auth: svc, Metrics: m,
		StripeWebhook:         http.NotFoundHandler(),
		AppleIAPNotifications: http.NotFoundHandler(),
		AppleIAPTransaction:   &applebill.Transaction{},
		StripeBilling:         &stripebill.Service{},
	})
	routes, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("New returned %T, not a chi router", h)
	}
	registered := map[string]bool{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range billingErrorSeries {
		if !registered[s.method+" "+s.route] {
			t.Errorf("%s %s is not a registered route; the billing alerts would read a series that never moves", s.method, s.route)
		}
		for _, status := range s.statuses {
			if got := testutil.ToFloat64(m.HTTPRequestsTotal.WithLabelValues(s.method, s.route, status)); got != 0 {
				t.Errorf("after New: %s %s %s = %v", s.method, s.route, status, got)
			}
		}
	}
	want := 0
	for _, s := range billingErrorSeries {
		want += len(s.statuses)
	}
	if n := testutil.CollectAndCount(m.HTTPRequestsTotal); n != want {
		t.Errorf("New created %d series, want %d", n, want)
	}
}
