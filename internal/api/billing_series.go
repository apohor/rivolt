package api

import "github.com/apohor/rivolt/internal/metrics"

// billingErrorSeries are the request-counter children the billing alerts read, created at 0 when the app starts. A counter child that
// only appears on its first increment is born at 1: Prometheus then has no earlier sample, so increase() and rate() see no change and
// the first error a pod serves after a restart is invisible to the 5xx alerts. The routes are chi's registered patterns (the
// middleware labels by pattern); a test walks the router so a renamed route fails.
var billingErrorSeries = []struct {
	method, route string
	statuses      []string
}{
	{"POST", "/api/billing/stripe/webhook", []string{"400", "500", "502", "503"}},
	{"POST", "/api/billing/checkout", []string{"500", "502", "503"}},
	{"POST", "/api/billing/portal", []string{"500", "502", "503"}},
	{"POST", "/api/billing/apple/transaction", []string{"500", "502", "503"}},
	{"POST", "/api/billing/apple/notifications", []string{"400", "500"}},
}

// preinitBillingSeries creates those children at 0 (a no-op without metrics).
func preinitBillingSeries(m *metrics.Metrics) {
	if m == nil || m.HTTPRequestsTotal == nil {
		return
	}
	for _, s := range billingErrorSeries {
		for _, status := range s.statuses {
			m.HTTPRequestsTotal.WithLabelValues(s.method, s.route, status)
		}
	}
}
