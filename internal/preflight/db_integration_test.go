//go:build integration

package preflight_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/preflight"
	"github.com/apohor/rivolt/internal/testpg"
)

// The founding dry run's counts come from the database in a read-only transaction: they match accounts minus the demo, accounts created
// after the launch instant are counted apart, and nothing is written.
func TestFoundingFromDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	var before int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id <> $1`, db.UserIDFor(db.DemoUsername)).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pf-a", "pf-b", "pf-c"} {
		if _, err := db.EnsureUser(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.EnsureUserFull(ctx, pool, db.DemoUsername, "", "Demo"); err != nil {
		t.Fatal(err)
	}
	demo := db.UserIDFor(db.DemoUsername)
	// One account is created after the launch instant.
	launch := time.Now().Add(-time.Hour).UTC()
	if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = $1 WHERE username IN ('pf-a', 'pf-b') OR id = $2`, launch.Add(-time.Hour), demo); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = $1 WHERE username = 'pf-c'`, launch.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	ents := func() (n int) {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entitlements`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	e0 := ents()
	f, err := preflight.FoundingFromDB(pool, demo)(ctx, launch)
	if err != nil {
		t.Fatal(err)
	}
	if f.Demo != 1 || f.Accounts != before+4 || f.Eligible+f.After != f.Accounts-f.Demo || f.After < 1 || f.ToGrant != f.Eligible-f.Already {
		t.Errorf("counts %+v (accounts before the test: %d), want demo 1, eligible + created-after = accounts - demo, at least one created after", f, before)
	}
	if ents() != e0 {
		t.Error("the dry run wrote entitlement rows")
	}
	// The report built on it is consistent, and is a report, not a write.
	rep := preflight.Run(ctx, preflight.Input{Getenv: func(string) string { return "" }, Launch: launch, Founding: preflight.FoundingFromDB(pool, demo)})
	for _, p := range rep.Problems {
		if len(p) > 0 && p[:min(len(p), 8)] == "accounts" {
			t.Errorf("a counts problem on a consistent database: %s", p)
		}
	}
	if ents() != e0 {
		t.Error("the report wrote entitlement rows")
	}
}
