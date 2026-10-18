//go:build integration

package integration_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/engage"
)

// The send log in Postgres: one claim per (account, kind, key) even when
// replicas race, the budget holds under a race, a utility sits outside it,
// skipped rows cost nothing, the push defaults changed, and purge/stuck work.
func TestEngageStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserVerified(ctx, pool, "eng", "eng@x.test", "eng", true)
	if err != nil {
		t.Fatal(err)
	}
	on := func(context.Context) (bool, error) { return true, nil }
	noon := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	e := &engage.Engage{Store: engage.NewStore(pool), Now: func() time.Time { return noon }}

	// Eight replicas race for one key: exactly one delivers.
	var delivered atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.Send(ctx, engage.Msg{User: uid, Kind: "recap", Key: "w1", Channel: engage.Email, Toggle: on},
				func(context.Context) error { delivered.Add(1); return nil })
		}()
	}
	wg.Wait()
	if delivered.Load() != 1 {
		t.Fatalf("%d deliveries for one key", delivered.Load())
	}
	var status string
	if err := pool.QueryRowContext(ctx, `SELECT status FROM engagement_sends WHERE user_id=$1 AND kind='recap' AND key='w1'`, uid).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("status %q (%v)", status, err)
	}

	// Budget under a race: 12 distinct keys, 3 slots left (one is used).
	delivered.Store(0)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = e.Send(ctx, engage.Msg{User: uid, Kind: "milestone", Key: "m" + string(rune('a'+i)), Channel: engage.Email, Toggle: on},
				func(context.Context) error { delivered.Add(1); return nil })
		}(i)
	}
	wg.Wait()
	if delivered.Load() != 3 {
		t.Errorf("%d milestones delivered, want 3 (budget 4 including the recap)", delivered.Load())
	}
	// A push to an account with no zone at all is refused, not sent at UTC hours.
	if out, _ := e.Send(ctx, engage.Msg{User: uid, Kind: "milestone", Key: "nozone", Channel: engage.Push, Toggle: on},
		func(context.Context) error { return nil }); out != engage.SkippedNoTZ {
		t.Errorf("push with no zone: %s", out)
	}
	// A utility is outside the budget; skipped rows cost nothing.
	if out, _ := e.Send(ctx, engage.Msg{User: uid, Kind: "plug_in_reminder", Key: "2026-10-21", Channel: engage.Push, Utility: true, Toggle: on},
		func(context.Context) error { return nil }); out != engage.SkippedNoTZ {
		t.Errorf("utility without a stored zone: %s", out)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO engagement_prefs (user_id, timezone) VALUES ($1, 'UTC')`, uid); err != nil {
		t.Fatal(err)
	}
	if out, _ := e.Send(ctx, engage.Msg{User: uid, Kind: "plug_in_reminder", Key: "2026-10-21", Channel: engage.Push, Utility: true, Toggle: on},
		func(context.Context) error { return nil }); out != engage.Sent {
		t.Errorf("utility over budget with a zone: %s", out)
	}
	if ok, err := e.Skip(ctx, uid, "recap", "w0", engage.Email, engage.StatusEmpty); !ok || err != nil {
		t.Errorf("skip: %v %v", ok, err)
	}
	if n, _ := engage.NewStore(pool).BudgetUsed(ctx, uid, noon.Add(-engage.BudgetWindow)); n != 4 {
		t.Errorf("budget used %d, want 4 (sent only; the utility and the skip are free)", n)
	}

	// Stuck and purge.
	if _, err := pool.ExecContext(ctx, `INSERT INTO engagement_sends (user_id, kind, key, channel, status, claimed_at) VALUES ($1,'x','stuck','push','sending', $2)`, uid, noon.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO engagement_sends (user_id, kind, key, channel, status, claimed_at) VALUES ($1,'x','old','push','sent', $2)`, uid, noon.AddDate(0, 0, -401)); err != nil {
		t.Fatal(err)
	}
	purged, stuck, err := e.Maintain(ctx)
	if err != nil || purged != 1 || stuck != 1 {
		t.Errorf("maintain: purged %d stuck %d err %v, want 1 and 1", purged, stuck, err)
	}

	// Push defaults: the reminder and milestones are opt-in.
	if _, err := pool.ExecContext(ctx, `INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES ($1,'https://push.example/1','k','a')`, uid); err != nil {
		t.Fatal(err)
	}
	var plug, mile bool
	if err := pool.QueryRowContext(ctx, `SELECT on_plug_in_reminder, on_milestones FROM push_subscriptions WHERE user_id=$1`, uid).Scan(&plug, &mile); err != nil || plug || mile {
		t.Errorf("defaults plug=%v milestones=%v (%v), want both off", plug, mile, err)
	}
	// The prefs constraints.
	if _, err := pool.ExecContext(ctx, `UPDATE engagement_prefs SET plug_remind_at = '22:30' WHERE user_id=$1`, uid); err == nil {
		t.Error("a remind time after 22:00 was accepted")
	}
	if _, err := pool.ExecContext(ctx, `UPDATE engagement_prefs SET plug_threshold_pct = 99 WHERE user_id=$1`, uid); err == nil {
		t.Error("a threshold of 99 was accepted")
	}
}
