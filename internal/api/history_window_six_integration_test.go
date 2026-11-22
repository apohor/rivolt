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
	notice := histwin.NoticeAt
	t.Cleanup(func() { histwin.SetNotice(notice) })
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
	now := time.Now()
	// The account exists; the notice goes out tomorrow: it is an existing account, 12 months until the notice has run.
	histwin.SetNotice(now.Add(24 * time.Hour))
	if got := months(); got != histwin.LegacyMonths {
		t.Errorf("an existing account during the notice: %d months, want %d", got, histwin.LegacyMonths)
	}
	// The notice went out an hour before the account was created: a new account, 6 months at once (still inside the 30 days).
	histwin.SetNotice(created.Add(-time.Hour))
	if got := months(); got != histwin.Months {
		t.Errorf("a new account during the notice: %d months, want %d", got, histwin.Months)
	}
	// The notice has run: everyone has 6.
	histwin.SetNotice(now.AddDate(0, 0, -60))
	if got := months(); got != histwin.Months {
		t.Errorf("after the notice: %d months, want %d", got, histwin.Months)
	}
}
