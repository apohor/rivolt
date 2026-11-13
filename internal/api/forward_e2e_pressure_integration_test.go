//go:build integration && e2e_forward

package api

// Slice 4 (last) of the multi-pod forward harness: the owner under pressure, and a warm requester when the owner does not answer.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

// silentOwner removes pod A's owner loop and leaves a stale readiness marker behind: requesters believe it is ready and nothing ever answers.
func (e *e2eEnv) silentOwner(t *testing.T) {
	t.Helper()
	ready := forward.Keys{Prefix: "e2e"}.Ready(e.a.name)
	e.a.stop()
	for i := 0; ; i++ {
		if v, _ := e.rdb.Get(context.Background(), ready).Result(); v == "draining" {
			break
		}
		if i > 100 {
			t.Fatal("the owner's readiness marker never went to draining")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := e.rdb.Set(context.Background(), ready, "1", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
}

// A warm requester (its monitor already holds a state for the vehicle) whose forward times out answers 200 with that state: the owner's
// silence costs the user nothing, and no pod calls Rivian.
func TestForwardE2EWarmPodTimeoutServesItsCache(t *testing.T) {
	env := newE2EEnv(t, e2eOpts{Timeouts: map[forward.Kind]time.Duration{forward.KindFreshState: 600 * time.Millisecond}})
	env.silentOwner(t)
	env.b.mon.Prime(e2eVehicle, &rivian.State{VehicleID: e2eVehicle, BatteryLevelPct: 77, PowerState: "ready"})

	w := env.b.getState(t)
	if w.Code != http.StatusOK {
		t.Fatalf("warm pod, owner silent: %d %s", w.Code, w.Body)
	}
	var st rivian.State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.BatteryLevelPct != 77 {
		t.Errorf("served %s (%v), want the cached state at 77%%", w.Body, err)
	}
	for _, p := range []*e2ePod{env.a, env.b} {
		if got := p.gw.count(opState) + p.gw.count(opPlan); got != 0 {
			t.Errorf("%s made %d Rivian calls, want 0", p.name, got)
		}
	}
	if got := env.b.forwardTotal(forward.KindFreshState, "timeout"); got != 1 {
		t.Errorf("timeout count %v, want 1", got)
	}
}

// Busy: the owner is at its per-user cap, answers busy to the retry as well, and the requester serves the call itself (local_busy).
func TestForwardE2EBusyAfterOneRetryRunsLocally(t *testing.T) {
	env := newE2EEnv(t, e2eOpts{PerUser: 1, Jitter: 20 * time.Millisecond})
	env.a.stall = make(chan struct{})
	first := make(chan int, 1)
	go func() { first <- env.b.getState(t).Code }() // occupies the owner's one slot for this user
	deadline := time.Now().Add(5 * time.Second)
	for env.a.entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if env.a.entered.Load() == 0 {
		t.Fatal("the first request never reached the owner")
	}

	plan, err := env.b.plan(t) // same user, second kind: busy, busy, then served here
	if err != nil || plan == nil || plan.Status != "OK" {
		t.Fatalf("plan while the owner is busy: %+v %v", plan, err)
	}
	if got := env.b.gw.count(opPlan); got != 1 {
		t.Errorf("requester plan calls %d, want 1 (served here)", got)
	}
	if got := env.b.forwardTotal(forward.KindPlanLegs, "local_busy"); got != 1 {
		t.Errorf("local_busy count %v, want 1", got)
	}
	if got := env.a.gw.count(opPlan); got != 0 {
		t.Errorf("owner plan calls %d, want 0", got)
	}
	close(env.a.stall) // let the first request finish through the owner
	select {
	case code := <-first:
		if code != http.StatusOK {
			t.Errorf("the first read: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first read never finished")
	}
	if got := env.a.gw.count(opState); got != 1 {
		t.Errorf("owner state calls %d, want 1", got)
	}
}

// Queue full: the owner's list is longer than the cap, so the requester does not push at all. Both kinds answer 503 with Retry-After 5 and
// nothing is served locally.
func TestForwardE2EQueueFullIs503(t *testing.T) {
	env := newE2EEnv(t)
	env.silentOwner(t)
	q := forward.Keys{Prefix: "e2e"}.Queue(env.a.name)
	for i := 0; i <= forward.MaxQueue; i++ {
		if err := env.rdb.RPush(context.Background(), q, "x").Err(); err != nil {
			t.Fatal(err)
		}
	}
	w := env.b.getState(t)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" {
		t.Errorf("fresh_state, queue full: %d Retry-After %q (%s)", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
	if _, err := env.b.plan(t); err == nil {
		t.Error("plan_legs, queue full: succeeded, want 503")
	} else if st, _ := upstreamErrorBody(err); st != http.StatusServiceUnavailable {
		t.Errorf("plan_legs, queue full: status %d, want 503", st)
	}
	for _, p := range []*e2ePod{env.a, env.b} {
		if got := p.gw.count(opState) + p.gw.count(opPlan); got != 0 {
			t.Errorf("%s made %d Rivian calls with the queue full, want 0", p.name, got)
		}
	}
	for _, k := range []forward.Kind{forward.KindFreshState, forward.KindPlanLegs} {
		if got := env.b.forwardTotal(k, "unavailable"); got != 1 {
			t.Errorf("%s: unavailable count %v, want 1", k, got)
		}
	}
	if n, _ := env.rdb.LLen(context.Background(), q).Result(); n != int64(forward.MaxQueue+1) {
		t.Errorf("the queue grew to %d: the requester pushed", n)
	}
}
