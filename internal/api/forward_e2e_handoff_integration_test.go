//go:build integration && e2e_forward

package api

// Slice 3 of the multi-pod forward harness: the group's owner changes under a requester (real lease rows, real epoch change).

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/forward"
)

// takeOver moves the group from pod `from` to a new pod: from releases it, the new pod's lead pass acquires it (epoch + 1), and the new
// pod's owner loop is ready. It returns the new pod.
func (e *e2eEnv) takeOver(t *testing.T, from *e2ePod, name string) *e2ePod {
	t.Helper()
	ctx := context.Background()
	var before int64
	if err := e.pool.QueryRowContext(ctx, `SELECT epoch FROM account_leases WHERE group_id=$1`, e.group).Scan(&before); err != nil {
		t.Fatal(err)
	}
	next := e.pod(t, name, e.uid)
	next.store.SetVehicleGroups(map[string]string{e2eVehicle: e.group})
	if err := from.store.ReleaseGroup(ctx, e.group); err != nil {
		t.Fatal(err)
	}
	if _, err := next.store.LeadGroups(ctx); err != nil {
		t.Fatal(err)
	}
	var after int64
	var pod string
	if err := e.pool.QueryRowContext(ctx, `SELECT epoch, pod_id FROM account_leases WHERE group_id=$1`, e.group).Scan(&after, &pod); err != nil || pod != name || after <= before {
		t.Fatalf("handoff: pod %q epoch %d -> %d (%v)", pod, before, after, err)
	}
	e.waitReady(t, next)
	return next
}

// (c1) The owner changes between the requester's lease read and its push: the old owner answers `moved`, the requester re-reads the lease and
// the call is made by the new owner, once. Nothing runs on the requester or the old owner for the second read.
func TestForwardE2EMovedThenTheNewOwner(t *testing.T) {
	env := newE2EEnv(t)
	// Prime B's cached view of the lease (pod-a, kept for 2 s) without a state read, which would also fill B's own cache.
	if o, err := env.b.view.Owner(context.Background(), env.group); err != nil || o.Pod != "pod-a" {
		t.Fatalf("view: %+v %v", o, err)
	}
	c := env.takeOver(t, env.a, "pod-c")

	// B still believes pod-a owns the group; pod-a's loop is up and ready, and no longer holds it.
	w := env.b.getState(t)
	if w.Code != 200 {
		t.Fatalf("read after the handoff: %d %s", w.Code, w.Body)
	}
	if got := c.gw.count(opState); got != 1 {
		t.Errorf("new owner Rivian calls %d, want exactly 1", got)
	}
	if got := env.a.gw.count(opState); got != 0 {
		t.Errorf("old owner Rivian calls %d, want 0", got)
	}
	if got := env.b.gw.count(opState); got != 0 {
		t.Errorf("requester Rivian calls %d, want 0", got)
	}
	if got := env.a.forwardTotal(forward.KindFreshState, "moved"); got != 1 {
		t.Errorf("old owner moved count %v, want 1", got)
	}
}

// (c2) The owner dies mid-flight: it has the request in hand and never answers (a dead process sends no reply); the lease passes to another
// pod. The requester times out, sees the owner changed, retries once, and the new owner answers. The requester never calls Rivian.
func TestForwardE2EOwnerKilledMidFlight(t *testing.T) {
	short := map[forward.Kind]time.Duration{forward.KindFreshState: 1200 * time.Millisecond}
	env := newE2EEnv(t, e2eOpts{Timeouts: short})
	env.a.stall = make(chan struct{})
	defer close(env.a.stall)

	type out struct {
		code int
		body string
	}
	done := make(chan out, 1)
	go func() {
		w := env.b.getState(t)
		done <- out{w.Code, w.Body.String()}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for env.a.entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if env.a.entered.Load() == 0 {
		t.Fatal("the request never reached pod-a")
	}
	c := env.takeOver(t, env.a, "pod-c")

	select {
	case o := <-done:
		if o.code != 200 {
			t.Fatalf("read across the owner's death: %d %s", o.code, o.body)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the read never finished")
	}
	if got := c.gw.count(opState); got != 1 {
		t.Errorf("new owner Rivian calls %d, want exactly 1", got)
	}
	if got := env.b.gw.count(opState); got != 0 {
		t.Errorf("requester Rivian calls %d, want 0", got)
	}
	if got := env.a.gw.count(opState); got != 0 {
		t.Errorf("dead owner Rivian calls %d, want 0 (it never got to call)", got)
	}
}
