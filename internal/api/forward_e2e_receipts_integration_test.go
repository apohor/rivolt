//go:build integration && e2e_forward

package api

// Three pods, one real syncer each over one Postgres: receipts_sync sent from the two pods that do not hold the user's group makes exactly one
// summaries call, at the owner, and no more however many are sent inside the claim interval.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/settings"
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

// Opting out purges rivian_receipt_sync, and with it the Postgres claim. Five on/off/on cycles through the real opt-in handler (real purge) from a
// non-owner still make one summaries call at the owner: the send window (Valkey) outlives the purge.
func TestForwardE2EReceiptsToggleLoopMakesOneSummariesCall(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()
	once := receipts.SendOnce(env.rdb, "e2e")
	send := func(uid uuid.UUID) {
		if once(ctx, uid) {
			env.b.fwd.ReceiptsSync(ctx, uid.String(), env.group)
		}
	}
	put := handleRivianReceiptsPut(nil, settings.NewFactory(env.pool), receipts.NewStore(env.pool), send)
	toggle := func(body string) {
		rec := httptest.NewRecorder()
		put(env.uid, rec, httptest.NewRequest("PUT", "/api/settings/rivian-receipts", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("PUT %s = %d", body, rec.Code)
		}
	}
	for i := 0; i < 5; i++ {
		toggle(`{"opt_in":true}`)
		want := int32(1)
		waitFor(t, func() bool { return env.a.entered.Load() >= want })
		toggle(`{"opt_in":false}`) // purges the claim row
	}
	time.Sleep(500 * time.Millisecond)
	if got := env.a.summary.Load(); got != 1 {
		t.Errorf("owner made %d summaries calls over 5 toggle cycles, want exactly 1", got)
	}
	if got := env.a.entered.Load(); got != 1 {
		t.Errorf("owner ran %d receipts_sync requests, want 1 (the window drops the rest before they are sent)", got)
	}
}
