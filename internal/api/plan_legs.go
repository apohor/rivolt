package api

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/planleg"
	"github.com/apohor/rivolt/internal/rivian"
)

// legCachedClient is a live client whose trip planning goes through the leg
// cache and the per-user quota.
type legCachedClient struct {
	rivian.Client
	planner rivian.TripPlanner
}

func (c legCachedClient) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	return c.planner.PlanTrip(ctx, in)
}

// withLegCache puts the leg cache and quota in front of a client's trip
// planning, whichever planner it is: the live gateway, or the replay client's
// stored plans and Valhalla-backed synthetic planner. A client with no planner
// (the stub, the mock) is returned as it is, and so is everything when there
// is no Redis.
func withLegCache(d Deps, uid uuid.UUID, c rivian.Client) rivian.Client {
	if d.PlanLegs == nil {
		return c
	}
	tp, ok := tripPlannerOf(c)
	if !ok {
		return c
	}
	return legCachedClient{Client: c, planner: &countedPlanner{inner: d.PlanLegs.Wrap(&timedPlanner{inner: tp, m: d.Metrics}, uid), m: d.Metrics}}
}

// legTrace tells the counting wrapper whether the leg reached the planner: the timing wrapper, which sits inside the leg cache, sets it, so a
// leg it never saw was answered by the cache or refused by the quota.
type legTrace struct{ upstream atomic.Bool }

type legTraceKey struct{}

// timedPlanner times the calls that reach the planner (leg-cache misses) in rivolt_trip_plan_upstream_seconds.
type timedPlanner struct {
	inner rivian.TripPlanner
	m     *metrics.Metrics
}

func (p *timedPlanner) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	if t, ok := ctx.Value(legTraceKey{}).(*legTrace); ok {
		t.upstream.Store(true)
	}
	start := time.Now()
	plan, err := p.inner.PlanTrip(ctx, in)
	if p.m != nil {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		p.m.TripPlanUpstreamSeconds.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
	}
	return plan, err
}

// countedPlanner counts every leg asked for in rivolt_trip_plan_legs_total by what happened to it.
type countedPlanner struct {
	inner rivian.TripPlanner
	m     *metrics.Metrics
}

func (p *countedPlanner) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	tr := &legTrace{}
	plan, err := p.inner.PlanTrip(context.WithValue(ctx, legTraceKey{}, tr), in)
	if p.m != nil {
		result := "hit"
		switch _, quota := planleg.IsQuota(err); {
		case quota:
			result = "quota"
		case err != nil:
			result = "error"
		case tr.upstream.Load():
			result = "miss"
		}
		p.m.TripPlanLegsTotal.WithLabelValues(result).Inc()
	}
	return plan, err
}

// writeQuotaError answers 429 with Retry-After when err is a planner quota
// refusal, and reports whether it did.
func writeQuotaError(w http.ResponseWriter, err error) bool {
	q, ok := planleg.IsQuota(err)
	if !ok {
		return false
	}
	secs := int(math.Ceil(q.RetryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": q.Error(), "class": "plan_quota", "retry_after_sec": secs})
	return true
}
