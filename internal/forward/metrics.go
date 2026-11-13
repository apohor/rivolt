package forward

import "github.com/prometheus/client_golang/prometheus"

// Outcomes of a forwarded call, all created at 0 at boot (a counter born at its first event is invisible to increase()).
//   - ok: the owner answered;
//   - timeout: no answer in the kind's time, or the request expired before it ran;
//   - moved: the owner no longer holds the group (stale epoch or past its local deadline);
//   - local: the call was served on this pod (no owner, owner not ready, flag off, Redis down): the M3 fail-soft path;
//   - unavailable: refused (queue full, no ready owner on a pod that holds no session): 503 Retry-After: 5;
//   - rejected: a message the owner dropped (bad signature, unknown kind, bad args, wrong size);
//   - held: this pod holds the group, so the call ran here with no forwarding (the owner's own calls). Counted so total volume and the
//     held:forwarded split are visible; it is not a fail-soft path and is left out of the local-share ratio;
//   - local_busy: the owner answered `busy` twice (the user's concurrency cap) and the call was served on this pod (a pod that cannot serve
//     locally answers unavailable instead);
//   - no_group: this pod does not know the vehicle's group (not in the lease store's vehicle->group map), so the call ran here with no
//     forwarding. A fail-soft path like local: a non-owner Rivian call the local-share ratio must see;
//   - too_big: the owner ran the kind but its answer does not fit the reply cap (MaxReplyBytes less headroom); the requester gets a
//     reply_too_big error. Counted apart from ok so a failure the user sees is not counted as success;
//   - acquired: acquire-on-request (M4-2): the lease was absent or expired, this pod took the group and ran the call as its owner;
//   - no_local: a pod whose role holds no leases (RIVOLT_ROLE api, jobs) could not forward the call and, rather than run it here or acquire,
//     answered 503 Retry-After. Every fall-soft case lands here on such a pod;
//   - error: the owner ran the kind and the runner failed with an error that is not a timeout (a 500 internal reply). "ok" means served,
//     so the local-share and health arithmetic do not count a failure as success.
var Outcomes = []string{"ok", "timeout", "moved", "local", "unavailable", "rejected", "error", "local_busy", "held", "no_group", "too_big", "acquired", "no_local"}

// MetricKinds are the kind label values.
func MetricKinds() []string {
	out := make([]string, 0, len(Kinds)+1)
	for k := range Kinds {
		out = append(out, string(k))
	}
	return append(out, KindUnknown)
}

// Metrics are the forwarder's instruments. No label carries a user, vehicle or group (/metrics is public).
type Metrics struct {
	Total      *prometheus.CounterVec   // rivolt_rivian_forward_total{kind,outcome}
	Seconds    *prometheus.HistogramVec // rivolt_rivian_forward_seconds{kind}
	QueueDepth prometheus.Gauge         // rivolt_forward_queue_depth (this pod's owner list, owner loop)
	OwnerReady prometheus.Gauge         // rivolt_forward_owner_ready (1 while this pod's owner loop is running and marked ready)
	// DrainWait counts calls that waited for a draining owner's successor before sending anything: forwarded (a ready owner appeared and
	// the call went to it), timeout (the cap ran out: 503 Retry-After), canceled (the request ended first).
	DrainWait        *prometheus.CounterVec // rivolt_forward_drain_wait_total{outcome}
	DrainWaitSeconds prometheus.Histogram   // rivolt_forward_drain_wait_seconds
}

// DrainWaitOutcomes are the drain-wait outcome label values.
var DrainWaitOutcomes = []string{"forwarded", "timeout", "canceled"}

// NewMetrics creates the instruments, registers them on reg and creates every (kind, outcome) series at 0.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_rivian_forward_total",
			Help: "Calls that needed Rivian on a pod, by kind and outcome (ok, timeout, moved, local, unavailable, rejected, error, local_busy, held, no_group, too_big, acquired, no_local).",
		}, []string{"kind", "outcome"}),
		Seconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rivolt_rivian_forward_seconds",
			Help:    "Time a forwarded call took, requester side, by kind.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 35},
		}, []string{"kind"}),
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_forward_queue_depth",
			Help: "Length of this pod's forwarder request list, as last seen by its owner loop.",
		}),
		OwnerReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_forward_owner_ready",
			Help: "1 while this pod's owner loop is running and has set its readiness marker, else 0.",
		}),
		DrainWait: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_forward_drain_wait_total",
			Help: "Calls that waited for a draining owner's successor, by outcome (forwarded, timeout, canceled).",
		}, []string{"outcome"}),
		DrainWaitSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rivolt_forward_drain_wait_seconds",
			Help:    "Time a call waited for a draining owner's successor.",
			Buckets: []float64{0.25, 0.5, 1, 2, 4, 6, 8, 12},
		}),
	}
	if reg != nil {
		reg.MustRegister(m.Total, m.Seconds, m.QueueDepth, m.OwnerReady, m.DrainWait, m.DrainWaitSeconds)
	}
	for _, o := range DrainWaitOutcomes {
		m.DrainWait.WithLabelValues(o)
	}
	for _, k := range MetricKinds() {
		for _, o := range Outcomes {
			m.Total.WithLabelValues(k, o)
		}
		m.Seconds.WithLabelValues(k)
	}
	return m
}
