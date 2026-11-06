package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/applebill"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/stripebill"
)

// metrics.BillingRouteStatuses names the billing routes whose error series are created at 0 at boot (so the first error on a fresh pod is an
// increase). Those keys must be the chi patterns the app's REAL router reports for its POST routes (the middleware labels a request with
// chi.RouteContext().RoutePattern()): a renamed route or a different subrouter prefix would leave the pre-created series dead at 0 while the real
// one is born at 1 again, the same class of silent mismatch as the old status="5xx" matcher. This builds the real router with the billing routes
// mounted (they exist only where Stripe and Apple are configured), walks it, and checks every key is a registered POST pattern.
func TestBillingRouteSeriesAreRegisteredPostPatterns(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	d := Deps{
		StripeBilling:         new(stripebill.Service),
		StripeWebhook:         stub,
		AppleIAPNotifications: stub,
		AppleIAPTransaction:   new(applebill.Transaction), // mounted, never called
	}
	registered := map[string]bool{}
	err := chi.Walk(newAdminTestRouter(t, d).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodPost {
			registered[strings.TrimSuffix(route, "/")] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(registered) < 5 {
		t.Fatalf("only %d POST routes found: the walk is not seeing the router", len(registered))
	}
	for route := range metrics.BillingRouteStatuses {
		if !registered[route] {
			t.Errorf("metrics.BillingRouteStatuses has %q, which is not a registered POST route pattern of the API (renamed? update the list and the alerts and panels that match it)", route)
		}
	}
}
