package api

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/metrics"
)

// /metrics is never on the public router: the Ingress routes every path
// to it, so a route there is public. Prometheus reads it from its own
// listener (cmd/rivolt).
func TestMetricsIsNotOnThePublicRouter(t *testing.T) {
	router := New(Deps{Metrics: metrics.New()})
	err := chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == "/metrics" || route == "/metrics/*" {
			t.Errorf("%s %s is mounted on the public router", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
