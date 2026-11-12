//go:build integration && e2e_forward

package api

// Three pods, one real syncer each over one Postgres: receipts_sync sent from the two pods that do not hold the user's group makes exactly one
// summaries call, at the owner, and no more however many are sent inside the claim interval.

import (
	"context"
	"testing"
	"time"
)

func TestForwardE2EReceiptsSyncFromNonOwnersMakesOneSummariesCallAtTheOwner(t *testing.T) {
	env := newE2EEnv(t)
	c := env.pod(t, "pod-c", env.uid)
	c.store.SetVehicleGroups(map[string]string{e2eVehicle: env.group})
	env.waitReady(t, c)

	ctx := context.Background()
	for _, p := range []*e2ePod{env.b, c} {
		if !p.fwd.ReceiptsSync(ctx, env.uid.String(), env.group) {
			t.Fatalf("%s: the push did not reach the owner's queue", p.name)
		}
	}
	waitFor(t, func() bool { return env.a.entered.Load() >= 2 && env.a.summary.Load() >= 1 })

	// Five more toggles inside the claim interval, from both non-owners, one at a time (the owner admits two per user at once and drops the
	// rest unanswered): each reaches the owner's runner, and the claim keeps the Rivian calls at the one.
	for i := 0; i < 5; i++ {
		p := env.b
		if i%2 == 1 {
			p = c
		}
		p.fwd.ReceiptsSync(ctx, env.uid.String(), env.group)
		want := int32(3 + i)
		waitFor(t, func() bool { return env.a.entered.Load() >= want })
	}

	if got := env.a.summary.Load(); got != 1 {
		t.Errorf("owner made %d summaries calls, want exactly 1", got)
	}
	for _, p := range []*e2ePod{env.b, c} {
		if got := p.summary.Load(); got != 0 {
			t.Errorf("%s (not the owner) made %d summaries calls, want 0", p.name, got)
		}
		if got := p.gw.count(opState) + p.gw.count(opPlan); got != 0 {
			t.Errorf("%s made %d other Rivian calls, want 0", p.name, got)
		}
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the owner")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
