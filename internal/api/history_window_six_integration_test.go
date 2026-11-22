//go:build integration

package api

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/histwin"
)

// An account created before the cutoff keeps 12 months with no end date; one created at or after it gets 6. The account's age is read from
// users.created_at, the way the server reads it.
func TestWindowCutoffAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	u, err := db.EnsureUser(ctx, pool, "cutoff-free")
	if err != nil {
		t.Fatal(err)
	}
	cutoff := histwin.Cutoff
	t.Cleanup(func() { histwin.SetCutoff(cutoff) })
	months0 := histwin.Months
	histwin.Months = 6
	t.Cleanup(func() { histwin.Months = months0 })
	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	months := func() int {
		w, on := ent.Window(ctx, u)
		if !on {
			t.Fatal("a free account has no window")
		}
		return w.Months
	}
	created := ent.Created(ctx, u)
	if created.IsZero() {
		t.Fatal("users.created_at was not read")
	}
	// The cutoff is tomorrow: the account exists already, so it keeps 12, and keeps it (there is no later date at which it shortens).
	histwin.SetCutoff(time.Now().Add(24 * time.Hour))
	if got := months(); got != histwin.LegacyMonths {
		t.Errorf("an existing account: %d months, want %d", got, histwin.LegacyMonths)
	}
	// The cutoff was an hour before the account was created: a new account, 6 months.
	histwin.SetCutoff(created.Add(-time.Hour))
	if got := months(); got != histwin.Months {
		t.Errorf("a new account: %d months, want %d", got, histwin.Months)
	}
}
