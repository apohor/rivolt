// Package metrics owns the application's Prometheus instrumentation.
//
// Rationale: kube-prometheus-stack already runs in the cluster and
// scrapes node/k8s/cnpg/argocd metrics out of the box. Adding a
// /metrics endpoint to rivolt with an explicit ServiceMonitor lights
// up per-handler latency, Rivian-result-class counters, and the
// other app-shaped signals the cluster-level scrapers can't see.
//
// All metrics live on a private *prometheus.Registry rather than the
// global default. Reasons:
//
//   - Tests can spin up a fresh registry per test.
//   - We don't accidentally collide with go-collector / process-collector
//     registrations from libraries that touch promauto.
//   - The handler is exported scoped to this registry, so /metrics
//     output is exactly what we asked for.
//
// Naming follows Prometheus conventions: snake_case, base unit suffix
// (_seconds, _bytes, _total).
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the rivolt-specific instrumentation surface. One
// instance is built at startup and shared with anything that
// emits a metric. Keep it small — adding a metric here means it
// shows up in Grafana, so each one should answer a real question
// you'd want to ask at 3am.
type Metrics struct {
	reg *prometheus.Registry

	// HTTP server side.
	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec

	// Rivian upstream classification (transient/outage/rate-limited/
	// user-action/unknown — see internal/rivian/errclass.go). Counts
	// outcomes per call class, not per error string, so cardinality
	// stays bounded.
	RivianResultsTotal *prometheus.CounterVec

	// Subscription leases owned by this pod. Phase 2 lease work
	// will increment/decrement these as it claims/releases.
	SubscriptionLeases prometheus.Gauge

	// Circuit breaker telemetry. State is a 0/1/2 gauge
	// (closed/half_open/open) so Grafana can alert on
	// max_over_time != 0. Trips is a counter labelled by reason
	// ("rate_limited" | "outage") so we can answer "what's making
	// it open" in one PromQL.
	RivianBreakerState prometheus.Gauge
	RivianBreakerTrips *prometheus.CounterVec
	// Global token bucket gating. Counter increments every time
	// the bucket says no, partitioned by class so we can answer
	// "is priority being starved".
	RivianRateLimitBlocked *prometheus.CounterVec
	// AI provider spend per user is intentionally NOT exposed here.
	// User-id labels would blow up cardinality at 1000 vehicles. The
	// existing internal/ai/usage.go writes per-user totals to
	// Postgres; surface those via a Grafana SQL data source if/when
	// we want to chart them.
	AIRequestsTotal *prometheus.CounterVec
}

// New constructs a Metrics with all collectors registered against a
// fresh registry. Pass the returned *Metrics into any package that
// needs to record a sample. Call Handler() to mount /metrics.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		reg: reg,

		HTTPRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_http_requests_total",
				Help: "Total HTTP requests handled, partitioned by route and status code.",
			},
			// `route` is the chi route pattern (e.g. /api/state/{vehicleID}),
			// NOT the raw URL — keeps cardinality bounded as vehicles
			// scale up. Status is bucketed by family (2xx/3xx/4xx/5xx)
			// for the same reason.
			[]string{"method", "route", "status"},
		),
		HTTPRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "rivolt_http_request_duration_seconds",
				Help:    "End-to-end HTTP handler latency, partitioned by route.",
				Buckets: prometheus.DefBuckets, // 5ms..10s, fine for web traffic
			},
			[]string{"method", "route"},
		),
		RivianResultsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_results_total",
				Help: "Outcome class for each call against Rivian's GraphQL gateway.",
			},
			// `op` is the GraphQL operation name (e.g. login,
			// vehicleState, currentUser), `class` is from
			// internal/rivian/errclass: success / transient / outage /
			// rate_limited / user_action / unknown.
			[]string{"op", "class"},
		),
		SubscriptionLeases: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rivolt_subscription_leases",
				Help: "Rivian websocket subscription leases currently owned by this pod.",
			},
		),
		RivianBreakerState: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rivolt_rivian_breaker_state",
				Help: "Rivian upstream breaker state. 0=closed, 1=half_open, 2=open.",
			},
		),
		RivianBreakerTrips: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_breaker_trips_total",
				Help: "Times the breaker tripped to open, partitioned by reason.",
			},
			[]string{"reason"},
		),
		RivianRateLimitBlocked: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_ratelimit_blocked_total",
				Help: "Outbound calls denied by the global token bucket, partitioned by class.",
			},
			[]string{"class"},
		),
		AIRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_ai_requests_total",
				Help: "AI provider calls, by provider and outcome.",
			},
			[]string{"provider", "outcome"},
		),
	}

	reg.MustRegister(
		m.HTTPRequestsTotal,
		m.HTTPRequestDuration,
		m.RivianResultsTotal,
		m.SubscriptionLeases,
		m.RivianBreakerState,
		m.RivianBreakerTrips,
		m.RivianRateLimitBlocked,
		m.AIRequestsTotal,
	)
	return m
}

// Handler returns the http.Handler for /metrics scoped to this
// Metrics' private registry. Mount under /metrics with no auth — the
// Prometheus scraper inside the cluster reaches it via the pod IP,
// and the Ingress doesn't expose it externally (see
// deploy/helm/rivolt/templates/ingress.yaml).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		// Surface scrape errors in the response so kube-prometheus-stack
		// flags the target unhealthy instead of silently dropping
		// metrics.
		ErrorHandling: promhttp.HTTPErrorOnError,
	})
}

// Registry returns the underlying registry. Tests use this to
// gather expected samples without going through the HTTP handler.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.reg
}
