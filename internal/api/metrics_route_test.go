package api

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/metrics"
)

func hasRoute(t *testing.T, h http.Handler, path string) bool {
	t.Helper()
	found := false
	err := chi.Walk(h.(chi.Routes), func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == path {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// /metrics is only on the public router when no separate listener serves
// it: the Ingress routes every path to this router.
func TestMetricsRouteOnlyWithoutSeparateListener(t *testing.T) {
	m := metrics.New()
	if !hasRoute(t, New(Deps{Metrics: m}), "/metrics") {
		t.Error("default: /metrics should stay on the router (unchanged behaviour)")
	}
	if hasRoute(t, New(Deps{Metrics: m, MetricsOnSeparateListener: true}), "/metrics") {
		t.Error("/metrics is on the public router although a separate listener serves it")
	}
}
