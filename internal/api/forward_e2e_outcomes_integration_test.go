//go:build integration && e2e_forward

package api

// Slice 2 of the multi-pod forward harness: plan_legs through the owner, and the outcome rule for the cases where the owner cannot be used
// (DA's single rule, asserted per kind):
//   - Redis down, owner not ready, no owner, moved with nobody new, busy after one retry, no group: served on the requesting pod (soft).
//   - A timeout with nobody new to ask: 503 with Retry-After 5 for plan_legs and for fresh_state (a cold pod has no cached snapshot to answer
//     from; a pod with one answering from it is the later change).
// Every case counts the Rivian calls at the fake gateway of each pod.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

func e2ePlanInput() rivian.PlanTripInput {
	return rivian.PlanTripInput{
		VehicleID: e2eVehicle, StartingSoC: 80, StartingRangeMeters: 250000, OriginBearing: 90,
		Waypoints: []rivian.PlanTripWaypoint{{Latitude: 34.1, Longitude: -118.2, WaypointType: "origin"}, {Latitude: 35.1, Longitude: -117.2, WaypointType: "destination"}},
	}
}

// plan is the planner call of /api/trips/plan on pod p, through the real forwardPlanner.
func (p *e2ePod) plan(t *testing.T) (*rivian.TripPlan, error) {
	t.Helper()
	sf := &stateForward{fw: p.fwd, groupOf: p.store.GroupOfVehicle, user: p.user}
	return (&forwardPlanner{inner: p.client, sf: sf}).PlanTrip(context.Background(), e2ePlanInput())
}

const (
	opState = "GetVehicleState"
	opPlan  = "planTripWithMultiStopV2"
)

// (b) plan_legs: the non-owner's plan is made by the owner; the requester never calls Rivian.
func TestForwardE2EPlanLegs(t *testing.T) {
	env := newE2EEnv(t)
	plan, err := env.b.plan(t)
	if err != nil || plan == nil || plan.Status != "OK" || len(plan.Routes) != 1 || len(plan.Routes[0].Waypoints) != 2 {
		t.Fatalf("plan %+v, err %v", plan, err)
	}
	if got := env.b.gw.count(opPlan); got != 0 {
		t.Errorf("the requesting pod called Rivian %d times, want 0", got)
	}
	if got := env.a.gw.count(opPlan); got != 1 {
		t.Errorf("the owner called Rivian %d times, want exactly 1", got)
	}
	if got := env.b.forwardTotal(forward.KindPlanLegs, "ok"); got != 1 {
		t.Errorf("requester ok count %v, want 1", got)
	}
}

// (d) Redis down: the requester cannot reach the owner and serves the call itself, for both kinds; when Redis is back, forwarding resumes.
func TestForwardE2ERedisDownFallsBackToLocal(t *testing.T) {
	env := newE2EEnv(t)
	env.mr.Close()
	if w := env.b.getState(t); w.Code != 200 {
		t.Fatalf("state with Redis down: %d %s", w.Code, w.Body)
	}
	if _, err := env.b.plan(t); err != nil {
		t.Fatalf("plan with Redis down: %v", err)
	}
	if got := env.b.gw.count(opState); got != 1 {
		t.Errorf("fresh_state: requester Rivian calls %d, want 1 (served here)", got)
	}
	if got := env.b.gw.count(opPlan); got != 1 {
		t.Errorf("plan_legs: requester Rivian calls %d, want 1 (served here)", got)
	}
	if got := env.a.gw.count(opState) + env.a.gw.count(opPlan); got != 0 {
		t.Errorf("the owner was called %d times with Redis down", got)
	}
	for _, k := range []forward.Kind{forward.KindFreshState, forward.KindPlanLegs} {
		if got := env.b.forwardTotal(k, "local"); got != 1 {
			t.Errorf("%s: local count %v, want 1", k, got)
		}
	}
}

// Soft case: no owner (the lease row is released), the owner's pod is not ready, and a vehicle with no group are all served on the requester.
func TestForwardE2ESoftCasesServeLocally(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()

	// Not ready: the owner's readiness marker is gone (a pod that does not forward, an old image, a crashed loop).
	if err := env.rdb.Del(ctx, forward.Keys{Prefix: "e2e"}.Ready(env.a.name)).Err(); err != nil {
		t.Fatal(err)
	}
	if w := env.b.getState(t); w.Code != 200 {
		t.Fatalf("not ready: %d %s", w.Code, w.Body)
	}
	if got := env.b.gw.count(opState); got != 1 {
		t.Errorf("not ready: requester Rivian calls %d, want 1", got)
	}

	// No group: the vehicle is unknown to this pod's lease map.
	env.b.store.SetVehicleGroups(map[string]string{})
	if _, err := env.b.plan(t); err != nil {
		t.Fatalf("no group: %v", err)
	}
	if got := env.b.gw.count(opPlan); got != 1 {
		t.Errorf("no group: requester Rivian calls %d, want 1", got)
	}
	if got := env.b.forwardTotal(forward.KindPlanLegs, "no_group"); got != 1 {
		t.Errorf("no_group count %v, want 1", got)
	}
	if got := env.a.gw.count(opState) + env.a.gw.count(opPlan); got != 0 {
		t.Errorf("the owner was called %d times in the soft cases", got)
	}
}

// A timeout with nobody new to ask is not served locally: both kinds answer 503 with Retry-After 5 (and make no Rivian call anywhere). The
// owner is "ready" (its marker exists) but never answers.
func TestForwardE2ETimeoutIs503AndNeverLocal(t *testing.T) {
	short := map[forward.Kind]time.Duration{forward.KindFreshState: 600 * time.Millisecond, forward.KindPlanLegs: 600 * time.Millisecond}
	env := newE2EEnv(t, e2eOpts{Timeouts: short})
	env.a.stop() // the owner loop ends and marks it draining (once its blocking pop returns)...
	ready := forward.Keys{Prefix: "e2e"}.Ready(env.a.name)
	for i := 0; ; i++ {
		if v, _ := env.rdb.Get(context.Background(), ready).Result(); v == "draining" {
			break
		}
		if i > 100 {
			t.Fatal("the owner's readiness marker never went to draining")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := env.rdb.Set(context.Background(), forward.Keys{Prefix: "e2e"}.Ready(env.a.name), "1", time.Minute).Err(); err != nil { // ...and a stale one comes back
		t.Fatal(err)
	}

	w := env.b.getState(t)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" {
		t.Errorf("fresh_state timeout: %d, Retry-After %q, want 503 and 5 (%s)", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
	if _, err := env.b.plan(t); err == nil {
		t.Error("plan_legs timeout: the plan succeeded, want a 503")
	} else if st, _ := upstreamErrorBody(err); st != http.StatusServiceUnavailable {
		t.Errorf("plan_legs timeout: err %v status %d, want 503", err, st)
	}
	for _, p := range []*e2ePod{env.a, env.b} {
		if got := p.gw.count(opState) + p.gw.count(opPlan); got != 0 {
			t.Errorf("%s made %d Rivian calls on a timeout, want 0", p.name, got)
		}
	}
	for _, k := range []forward.Kind{forward.KindFreshState, forward.KindPlanLegs} {
		if got := env.b.forwardTotal(k, "timeout"); got != 1 {
			t.Errorf("%s: timeout count %v, want 1", k, got)
		}
	}
}

// Moved with nobody new (the lease row now names a different pod that is not ready, or none): the owner answers `moved` and the requester
// serves the call itself, counted local, for both kinds.
func TestForwardE2EMovedWithNoNewOwnerServesLocally(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()
	// Pod A is still ready and still dequeues, but loses the group: the lease row is released, so A answers moved and the row names nobody.
	if _, err := env.pool.ExecContext(ctx, `UPDATE account_leases SET pod_id = NULL, expires_at = now() - interval '1 second' WHERE group_id = $1`, env.group); err != nil {
		t.Fatal(err)
	}
	// B's view of the lease is cached for 2 s: let it expire so B reads the new state.
	time.Sleep(2200 * time.Millisecond)
	if w := env.b.getState(t); w.Code != 200 {
		t.Fatalf("fresh_state after the group moved away: %d %s", w.Code, w.Body)
	}
	if _, err := env.b.plan(t); err != nil {
		t.Fatalf("plan_legs after the group moved away: %v", err)
	}
	if got := env.b.gw.count(opState); got != 1 {
		t.Errorf("fresh_state: requester Rivian calls %d, want 1", got)
	}
	if got := env.b.gw.count(opPlan); got != 1 {
		t.Errorf("plan_legs: requester Rivian calls %d, want 1", got)
	}
}
