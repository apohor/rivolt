//go:build integration

package entitlements

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// The plan from real rows: the table's constraints, the rules over what Postgres returns, the cache and Invalidate, and that the rows go
// with the account.
func TestEntitlementsAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, _ := db.EnsureUser(ctx, pool, "ent-a")
	other, _ := db.EnsureUser(ctx, pool, "ent-b")
	s := New(pool, Config{Billing: true})
	now := time.Now().UTC()
	s.now = func() time.Time { return now }

	if st, err := s.State(ctx, uid); err != nil || st.Pro {
		t.Fatalf("no rows: %+v %v, want free", st, err)
	}
	ins := func(user any, source, status string, end any, ext any) error {
		_, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end, external_id) VALUES ($1, $2, $3, $4, $5)`,
			user, source, status, end, ext)
		return err
	}

	// Constraints: trial and founding need an end; unknown source/status refused; one row per (account, source); external ids unique.
	if err := ins(uid, "trial", "active", nil, nil); err == nil {
		t.Error("a trial without an end was accepted")
	}
	if err := ins(uid, "founding", "active", nil, nil); err == nil {
		t.Error("a founding row without an end was accepted")
	}
	if err := ins(uid, "paypal", "active", now.Add(time.Hour), nil); err == nil {
		t.Error("an unknown source was accepted")
	}
	if err := ins(uid, "stripe", "paused", now.Add(time.Hour), nil); err == nil {
		t.Error("an unknown status was accepted")
	}
	if err := ins(uid, "grant", "active", nil, nil); err != nil {
		t.Fatalf("a grant with no end: %v", err)
	}
	if err := ins(uid, "grant", "active", nil, nil); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("a second grant row for the same account: %v", err)
	}
	if err := ins(uid, "stripe", "active", now.Add(time.Hour), "sub_test_1"); err != nil {
		t.Fatal(err)
	}
	if err := ins(other, "stripe", "active", now.Add(time.Hour), "sub_test_1"); err == nil {
		t.Error("two accounts shared one external id")
	}

	// The rules over what comes back, and the cache: a change is not seen until Invalidate or the TTL.
	s.Invalidate(uid)
	if st, err := s.State(ctx, uid); err != nil || !st.Pro || st.Source != SourceStripe {
		t.Fatalf("grant + stripe: %+v %v, want stripe", st, err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE entitlements SET status = 'ended' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.State(ctx, uid); !st.Pro {
		t.Error("the change was seen through the cache before its TTL")
	}
	s.Invalidate(uid)
	if st, _ := s.State(ctx, uid); st.Pro || st.EndedAt == nil || st.EndedSource == "" {
		t.Errorf("all rows ended, after Invalidate: %+v, want free with the lapse reported", st)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE entitlements SET status = 'active', current_period_end = $2 WHERE user_id = $1`, uid, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(s.ttl + time.Second) // the TTL passes: the new rows are read
	if st, _ := s.State(ctx, uid); !st.Pro {
		t.Errorf("after the TTL: %+v, want Pro", st)
	}
	// A cache hit is recomputed at the current moment: a grant that ends is seen to end on time, without a re-read.
	s.ttl = 24 * time.Hour
	s.Invalidate(uid)
	if st, _ := s.State(ctx, uid); !st.Pro {
		t.Fatalf("warming the cache: %+v, want Pro", st)
	}
	now = now.Add(3 * time.Hour) // inside the TTL (a hit), past the 2 h end
	if st, _ := s.State(ctx, uid); st.Pro {
		t.Errorf("a cache hit past every end: %+v, want free", st)
	}

	// The running trial's end, for a Stripe checkout started during the trial (billing starts when it ends).
	trialEnd := now.Add(5 * 24 * time.Hour)
	if err := ins(other, "trial", "active", trialEnd, nil); err != nil {
		t.Fatal(err)
	}
	if end, ok, err := s.TrialEnd(ctx, other); err != nil || !ok || !end.Equal(trialEnd.Truncate(time.Microsecond)) {
		t.Errorf("TrialEnd = %v %v %v, want %v", end, ok, err, trialEnd)
	}
	if _, ok, _ := s.TrialEnd(ctx, uid); ok {
		t.Error("TrialEnd reported a trial for an account without one")
	}

	// Gone with the account.
	var n int
	if err := db.DeleteUserAccount(ctx, pool, uid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entitlements WHERE user_id = $1`, uid).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows left after account delete: %d %v", n, err)
	}
}
