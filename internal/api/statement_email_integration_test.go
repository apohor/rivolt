//go:build integration

// Monthly statement email against real Postgres: the claim row is the
// idempotency key (also under concurrency), only opted-in users with an
// address are candidates, and the email's totals are the ledger's.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/statement"
)

func TestStatementStore_ClaimPrefsCandidatesPurge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := statement.NewStore(pool)

	mkUser := func(name, mail string) (u [16]byte) {
		id, err := db.EnsureUserVerified(ctx, pool, name, mail, "Sam Rivera", true)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	alice := mkUser("stm-alice", "alice@example.com")
	bob := mkUser("stm-bob", "bob@example.com")
	noMail := mkUser("stm-nomail", "")
	off := mkUser("stm-off", "off@example.com")

	for id, p := range map[[16]byte]statement.Prefs{
		alice:  {Enabled: true, Timezone: "America/Chicago"},
		bob:    {Enabled: true, Timezone: "UTC"},
		noMail: {Enabled: true, Timezone: "UTC"},
		off:    {Enabled: false, Timezone: "UTC"},
	} {
		if err := store.SetPrefs(ctx, id, p); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := store.OptedIn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[[16]byte]statement.Candidate{}
	for _, c := range cs {
		seen[c.UserID] = c
	}
	if _, ok := seen[alice]; !ok || seen[alice].Timezone != "America/Chicago" || seen[alice].Email != "alice@example.com" {
		t.Fatalf("alice missing/wrong: %+v", seen[alice])
	}
	if _, ok := seen[bob]; !ok {
		t.Fatal("bob missing")
	}
	if _, ok := seen[noMail]; ok {
		t.Fatal("a user with no email address is a candidate")
	}
	if _, ok := seen[off]; ok {
		t.Fatal("a user who didn't opt in is a candidate")
	}

	// An address the IdP did NOT verify (an iss:sub account) never
	// receives mail: not a candidate, and Email() reports none.
	unv, err := db.EnsureUserVerified(ctx, pool, "https://idp.example.com:sub-1", "stranger-inbox@example.com", "X", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPrefs(ctx, unv, statement.Prefs{Enabled: true, Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	cs2, _ := store.OptedIn(ctx)
	for _, c := range cs2 {
		if c.UserID == unv {
			t.Fatal("a user with an unverified address is a candidate")
		}
	}
	if e, _ := store.Email(ctx, unv); e != "" {
		t.Fatalf("Email() = %q for an unverified address, want none", e)
	}
	// Verification follows the address: a later sign-in verifying the
	// same address raises the flag; a different address resets it.
	if _, err := db.EnsureUserVerified(ctx, pool, "https://idp.example.com:sub-1", "stranger-inbox@example.com", "X", true); err != nil {
		t.Fatal(err)
	}
	if e, _ := store.Email(ctx, unv); e != "stranger-inbox@example.com" {
		t.Fatalf("after verifying: Email() = %q", e)
	}
	if _, err := db.EnsureUserVerified(ctx, pool, "https://idp.example.com:sub-1", "other@example.com", "X", false); err != nil {
		t.Fatal(err)
	}
	if e, _ := store.Email(ctx, unv); e != "" {
		t.Fatalf("a new unverified address inherited the old verification: %q", e)
	}

	// Claim: the idempotency key.
	now := time.Now().UTC()
	if ok, _ := store.Claim(ctx, alice, "2026-09", now); !ok {
		t.Fatal("first claim refused")
	}
	if ok, _ := store.Claim(ctx, alice, "2026-09", now); ok {
		t.Fatal("a 'sending' period was claimed again")
	}
	if err := store.Mark(ctx, alice, "2026-09", "failed", "send_failed", now); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Claim(ctx, alice, "2026-09", now.Add(time.Hour)); !ok {
		t.Fatal("a failed period inside the retry window was not reclaimable")
	}
	_ = store.Mark(ctx, alice, "2026-09", "sent", "", now)
	if ok, _ := store.Claim(ctx, alice, "2026-09", now.Add(2*time.Hour)); ok {
		t.Fatal("a sent period was claimed again")
	}
	if last, _ := store.LastSentPeriod(ctx, alice); last != "2026-09" {
		t.Fatalf("last sent = %q", last)
	}
	// Failed and older than the window: given up.
	_, _ = store.Claim(ctx, bob, "2026-08", now.Add(-4*24*time.Hour))
	_ = store.Mark(ctx, bob, "2026-08", "failed", "send_failed", now)
	if ok, _ := store.Claim(ctx, bob, "2026-08", now); ok {
		t.Fatal("a period failed more than 3 days ago was retried")
	}

	// Attempts are capped: a send that keeps failing is given up on.
	for i := 0; i < 4; i++ {
		ok, _ := store.Claim(ctx, noMail, "2026-07", now)
		if want := i < 3; ok != want {
			t.Fatalf("attempt %d: claim = %v, want %v", i+1, ok, want)
		}
		_ = store.Mark(ctx, noMail, "2026-07", "failed", "send_failed", now)
	}
	// Claims left 'sending' are counted so a lost month is visible.
	if ok, _ := store.Claim(ctx, off, "2026-06", now.Add(-2*time.Hour)); !ok {
		t.Fatal("setup claim refused")
	}
	if n, err := store.StuckSending(ctx, time.Hour, now); err != nil || n != 1 {
		t.Fatalf("stuck = %d (%v), want 1", n, err)
	}

	// Concurrency: exactly one winner per (user, period).
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := store.Claim(ctx, bob, "2026-10", now); err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d concurrent claims won, want exactly 1", wins.Load())
	}

	// Purge removes only rows older than the cutoff.
	n, err := store.Purge(ctx, now.Add(-24*time.Hour))
	if err != nil || n != 1 { // bob's 2026-08 row was created 4 days ago
		t.Fatalf("purge = %d (%v), want 1", n, err)
	}
}

// The email's numbers are the ledger's: the same Build over the same
// rows as GET /api/ledger/monthly.
func TestStatementMonthly_MatchesLedgerBuild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserVerified(ctx, pool, "stm-ledger", "l@example.com", "", true)
	if err != nil {
		t.Fatal(err)
	}
	cf := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	st := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	if err := cf.For(uid).Upsert(ctx, charges.Charge{ID: "stm-c1", VehicleID: "rv-s", StartedAt: st, EndedAt: st.Add(time.Hour),
		EnergyAddedKWh: 40, Cost: 6.4, Currency: "USD", PricePerKWh: 0.16, CostSource: "home_rate", Source: "live"}); err != nil {
		t.Fatal(err)
	}
	if err := cf.For(uid).Upsert(ctx, charges.Charge{ID: "stm-c2", VehicleID: "rv-s", StartedAt: st.Add(48 * time.Hour), EndedAt: st.Add(49 * time.Hour),
		EnergyAddedKWh: 30, Source: "live"}); err != nil { // unpriced
		t.Fatal(err)
	}
	la := newLedgerAPI(pool, settings.NewFactory(pool))
	got, err := newStatementMonthly(la)(ctx, uid, "2026-09", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := ledger.ParsePeriod("2026-09", time.UTC)
	in, err := la.input(ctx, uid, ledgerParams{loc: time.UTC}, p)
	if err != nil {
		t.Fatal(err)
	}
	want := ledger.Build(in, p, true)
	if got.Spend.Total != want.Spend.Total || got.Spend.UnpricedSessions != want.Spend.UnpricedSessions ||
		got.Energy.KWhAdded != want.Energy.KWhAdded || got.Spend.Total != 6.4 || got.Spend.UnpricedSessions != 1 {
		t.Fatalf("statement monthly %+v vs ledger %+v", got.Spend, want.Spend)
	}
	r := statement.Render(statement.Input{Period: "2026-09", Currency: "USD", Month: got})
	if r.Silent || r.Subject != "Your Rivian cost for September: $6.40" {
		t.Fatalf("subject = %q", r.Subject)
	}
}
