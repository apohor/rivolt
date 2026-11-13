package api

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/forward"
)

// A forwarded call's kind timeout must stay at least 2 s under the handler deadline of every route that forwards the kind, or the
// requester would be cut off before it could answer or retry. The deadlines are the router's own constants (api.go).
func TestForwardKindTimeoutsFitTheirRoutes(t *testing.T) {
	const slack = 2 * time.Second
	// Each kind with the routes that forward it and the deadline each route runs under. plan_legs is forwarded by /trips/plan (the
	// main 30 s group) and by plan-multiday (the 5 minute group, one forward per leg): the tighter one binds.
	table := []struct {
		kind   forward.Kind
		routes map[string]time.Duration
	}{
		{forward.KindPlanLegs, map[string]time.Duration{"POST /trips/plan": routeDeadline, "POST /trips/plan-multiday (per leg)": longRouteDeadline}},
		{forward.KindFreshState, map[string]time.Duration{"GET /state/{id}": routeDeadline, "GET /state/{id}/fresh": routeDeadline}},
		{forward.KindOTANotes, map[string]time.Duration{"GET /vehicles/{id}/ota/notes": routeDeadline}},
		{forward.KindLiveSession, map[string]time.Duration{"GET /live-session/{id}": routeDeadline}},
		// adopt follows a login in the background: no route waits for it, but it must not outlast the main group's deadline either.
		{forward.KindAdopt, map[string]time.Duration{"after POST /settings/rivian login (best effort)": routeDeadline}},
		// receipts_sync follows a receipts opt-in in the background: no route waits for it.
		{forward.KindReceiptsSync, map[string]time.Duration{"after PUT /settings/rivian_receipts (best effort)": routeDeadline}},
	}
	covered := map[forward.Kind]bool{}
	for _, row := range table {
		covered[row.kind] = true
		spec, ok := forward.Kinds[row.kind]
		if !ok {
			t.Errorf("kind %s is not in the allowlist", row.kind)
			continue
		}
		for route, deadline := range row.routes {
			if spec.Timeout > deadline-slack {
				t.Errorf("%s: kind %s waits %v but the route's deadline is %v (needs %v of slack)", route, row.kind, spec.Timeout, deadline, slack)
			}
			// The wait for a draining owner comes before the send and is cut to what is left: wait + kind timeout + slack fits the route.
			if wait := forward.WaitBudget(row.kind, deadline, 12*time.Second); wait+spec.Timeout+slack > deadline {
				t.Errorf("%s: kind %s may wait %v for a draining owner, then %v, past the route's %v", route, row.kind, wait, spec.Timeout, deadline)
			}
		}
	}
	for k := range forward.Kinds {
		if !covered[k] {
			t.Errorf("kind %s has no row in this table: add the routes that forward it and their deadlines", k)
		}
	}
	if w := forward.WaitBudget(forward.KindLiveSession, routeDeadline, 12*time.Second); w > 4*time.Second {
		t.Errorf("live_session may wait %v for a draining owner, want about 4 s (the page polls every 10 s)", w)
	}
	if routeDeadline != 30*time.Second || longRouteDeadline != 5*time.Minute {
		t.Errorf("the route deadlines changed (%v, %v): re-check the kind timeouts in internal/forward", routeDeadline, longRouteDeadline)
	}
}
