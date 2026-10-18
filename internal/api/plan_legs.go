package api

import (
	"context"
	"math"
	"net/http"
	"strconv"

	"github.com/google/uuid"

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
	return legCachedClient{Client: c, planner: d.PlanLegs.Wrap(tp, uid)}
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
