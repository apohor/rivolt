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

// The window tests below run in the steady state: the 30 days' notice of the 12 to 6 month change has run, so every free account has
// histwin.Months. TestWindowNoticeAgainstPostgres puts the dates back for the notice period itself.
func init() {
	histwin.NoticeAt = time.Unix(0, 0).UTC()
	histwin.EffectiveAt = histwin.NoticeAt.AddDate(0, 0, 30)
}

// During the notice an account that existed before it keeps 12 months (Terms section 11); an account created after it gets 6 at once;
// when the notice has run every free account has 6. The account's age is read from users.created_at, the way the server reads it.
func TestWindowNoticeAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	u, err := db.EnsureUser(ctx, pool, "notice-free")
	if err != nil {
		t.Fatal(err)
	}
	notice, effective := histwin.NoticeAt, histwin.EffectiveAt
	t.Cleanup(func() { histwin.NoticeAt, histwin.EffectiveAt = notice, effective })
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
	now := time.Now()
	// The account exists; the notice goes out tomorrow: it is an existing account, 12 months until the notice has run.
	histwin.NoticeAt, histwin.EffectiveAt = now.Add(24*time.Hour), now.Add(24*time.Hour).AddDate(0, 0, 30)
	if got := months(); got != histwin.LegacyMonths {
		t.Errorf("an existing account during the notice: %d months, want %d", got, histwin.LegacyMonths)
	}
	// The notice went out an hour before the account was created: a new account, 6 months at once (still inside the 30 days).
	histwin.NoticeAt, histwin.EffectiveAt = created.Add(-time.Hour), created.Add(-time.Hour).AddDate(0, 0, 30)
	if got := months(); got != histwin.Months {
		t.Errorf("a new account during the notice: %d months, want %d", got, histwin.Months)
	}
	// The notice has run: everyone has 6.
	histwin.NoticeAt, histwin.EffectiveAt = now.AddDate(0, 0, -60), now.AddDate(0, 0, -30)
	if got := months(); got != histwin.Months {
		t.Errorf("after the notice: %d months, want %d", got, histwin.Months)
	}
}
