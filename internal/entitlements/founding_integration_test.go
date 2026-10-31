//go:build integration

package entitlements_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/testpg"
)

// The founding grant against Postgres: every account that exists at launch gets a founding row ending a year after launch, the demo
// account and an account created after launch get none, --apply needs the dry run's count and hash, and a re-run grants nothing.
func TestFoundingGrantAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	launch := time.Now().UTC().Truncate(time.Second)
	var existing []uuid.UUID
	for _, name := range []string{"found-a", "found-b", "found-c"} {
		id, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = $2 WHERE id = $1`, id, launch.AddDate(0, -3, 0)); err != nil {
			t.Fatal(err)
		}
		existing = append(existing, id)
	}
	demoID, err := db.EnsureUserFull(ctx, pool, db.DemoUsername, "", "Demo")
	if err != nil {
		t.Fatal(err)
	}
	if demoID != db.UserIDFor(db.DemoUsername) {
		t.Fatal("demo id mismatch")
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = $2 WHERE id = $1`, demoID, launch.AddDate(0, -3, 0)); err != nil {
		t.Fatal(err)
	}
	late, err := db.EnsureUser(ctx, pool, "found-late")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = $2 WHERE id = $1`, late, launch.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Other tests in the same database may have created accounts: count what the grant should cover, the same way the plan does.
	var accounts int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE created_at <= $1 AND id <> $2`, launch, demoID).Scan(&accounts); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := entitlements.RunFounding(ctx, pool, &out, &errOut, demoID, launch, false, -1, ""); code != 0 {
		t.Fatalf("dry run: %d %s", code, errOut.String())
	}
	rep, err := entitlements.PlanFounding(ctx, pool, demoID, launch)
	if err != nil || rep.Eligible != accounts || rep.ToGrant != accounts || rep.Already != 0 {
		t.Fatalf("plan: %+v %v, want %d accounts to grant", rep, err, accounts)
	}
	for _, id := range rep.IDs {
		if id == demoID || id == late {
			t.Errorf("plan lists the demo or a late account: %s", id)
		}
	}
	if strings.Contains(out.String(), "@") {
		t.Error("the dry run printed something that looks like an address")
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entitlements WHERE source = 'founding'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a dry run wrote rows: %d %v", n, err)
	}

	// --apply refuses a count or hash that is not the dry run's.
	if code := entitlements.RunFounding(ctx, pool, &out, &errOut, demoID, launch, true, rep.ToGrant+1, rep.IDsHash()); code == 0 {
		t.Error("apply went ahead with a wrong count")
	}
	if code := entitlements.RunFounding(ctx, pool, &out, &errOut, demoID, launch, true, rep.ToGrant, "deadbeef"); code == 0 {
		t.Error("apply went ahead with a wrong hash")
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entitlements WHERE source = 'founding'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused apply wrote rows: %d %v", n, err)
	}

	if code := entitlements.RunFounding(ctx, pool, &out, &errOut, demoID, launch, true, rep.ToGrant, rep.IDsHash()); code != 0 {
		t.Fatalf("apply: %d %s", code, errOut.String())
	}
	// Every account at launch (not the demo, not the late one) is Pro until launch + 1 year, as a founding grant.
	for _, id := range existing {
		var end time.Time
		if err := pool.QueryRowContext(ctx, `SELECT current_period_end FROM entitlements WHERE user_id = $1 AND source = 'founding' AND status = 'active'`, id).Scan(&end); err != nil {
			t.Fatalf("no founding row for %s: %v", id, err)
		}
		if !end.Equal(launch.AddDate(1, 0, 0)) {
			t.Errorf("founding ends %v, want %v", end, launch.AddDate(1, 0, 0))
		}
	}
	for _, id := range []uuid.UUID{demoID, late} {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entitlements WHERE user_id = $1`, id).Scan(&n); err != nil || n != 0 {
			t.Errorf("the demo or a late account got a row: %d %v", n, err)
		}
	}
	// The count check holds, and a re-run grants nothing.
	var grants int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entitlements e JOIN users u ON u.id = e.user_id WHERE e.source = 'founding' AND u.created_at <= $1 AND u.id <> $2`, launch, demoID).Scan(&grants); err != nil || grants != accounts {
		t.Errorf("founding rows %d, accounts %d (%v)", grants, accounts, err)
	}
	again, err := entitlements.ApplyFounding(ctx, pool, demoID, launch)
	if err != nil || again != 0 {
		t.Errorf("a re-run granted %d (%v), want 0", again, err)
	}
	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	if st, err := ent.State(ctx, existing[0]); err != nil || !st.Pro || st.Source != entitlements.SourceFounding {
		t.Errorf("a founding account reads %+v %v, want Pro by founding", st, err)
	}
}

// A brand-new account starts a 14-day trial, once, ever; the demo and an account that signs in again do not.
func TestNewAccountTrialAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	t.Cleanup(func() { db.SetNewAccountTrial(0) })

	before, err := db.EnsureUser(ctx, pool, "trial-before-billing") // billing off: no trial
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entitlements WHERE user_id = $1`, before).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a trial started with billing off: %d %v", n, err)
	}

	db.SetNewAccountTrial(entitlements.TrialLength)
	start := time.Now()
	uid, err := db.EnsureUserFull(ctx, pool, "trial-new", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var end time.Time
	if err := pool.QueryRowContext(ctx, `SELECT current_period_end FROM entitlements WHERE user_id = $1 AND source = 'trial' AND status = 'active'`, uid).Scan(&end); err != nil {
		t.Fatalf("a new account has no trial: %v", err)
	}
	if d := end.Sub(start); d < entitlements.TrialLength-time.Minute || d > entitlements.TrialLength+time.Minute {
		t.Errorf("the trial runs %v, want 14 days", d)
	}
	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	if st, _ := ent.State(ctx, uid); !st.Pro || st.Source != entitlements.SourceTrial || st.TrialEnds == nil {
		t.Errorf("a trial account reads %+v", st)
	}

	// Signing in again is not a new account, and a trial row that ended is never replaced: one trial per account, ever.
	if _, err := pool.ExecContext(ctx, `UPDATE entitlements SET status = 'ended', current_period_end = now() - interval '1 day' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureUserFull(ctx, pool, "trial-new", "", ""); err != nil {
		t.Fatal(err)
	}
	if started, err := db.StartTrial(ctx, pool, uid, entitlements.TrialLength); err != nil || started {
		t.Errorf("a second trial started: %v %v", started, err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entitlements WHERE user_id = $1 AND source = 'trial' AND status = 'ended'`, uid).Scan(&n); err != nil || n != 1 {
		t.Errorf("the ended trial was replaced: %d %v", n, err)
	}

	// The demo account never gets one.
	demoID, err := db.EnsureUserFull(ctx, pool, db.DemoUsername, "", "Demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entitlements WHERE user_id = $1`, demoID).Scan(&n); err != nil || n != 0 {
		t.Errorf("the demo got a trial: %d %v", n, err)
	}
}

// The dry run is a pure read: it never creates the demo user (prod's users table may have no demo row; the CLI only computes the demo's
// id, it never looks it up with a write) or any other row.
func TestFoundingDryRunWritesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if _, err := db.EnsureUser(ctx, pool, "dryrun-a"); err != nil {
		t.Fatal(err)
	}
	counts := func() (users, ents, demos int) {
		t.Helper()
		if err := pool.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM entitlements), (SELECT count(*) FROM users WHERE id = $1)`, db.UserIDFor(db.DemoUsername)).Scan(&users, &ents, &demos); err != nil {
			t.Fatal(err)
		}
		return
	}
	u0, e0, d0 := counts()
	var out, errOut bytes.Buffer
	if code := entitlements.RunFounding(ctx, pool, &out, &errOut, db.UserIDFor(db.DemoUsername), time.Now().UTC(), false, -1, ""); code != 0 {
		t.Fatalf("dry run: %d %s", code, errOut.String())
	}
	u1, e1, demos := counts()
	if u1 != u0 || e1 != e0 || demos != d0 {
		t.Errorf("the dry run wrote: users %d -> %d, entitlements %d -> %d, demo rows %d -> %d", u0, u1, e0, e1, d0, demos)
	}
}
