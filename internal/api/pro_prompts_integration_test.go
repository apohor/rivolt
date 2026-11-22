//go:build integration

package api

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// "Not now" against Postgres (Pro entry D6): one dismissal hides a prompt for 90 days, the second for good, a plan change brings it back,
// a read that fails hides everything, two writers do not lose a count, and deleting the account deletes the state.
func TestProPromptsAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, _ := db.EnsureUser(ctx, pool, "prompts-a")
	other, _ := db.EnsureUser(ctx, pool, "prompts-b")
	t0 := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	none := time.Time{}

	if h := hiddenPrompts(ctx, pool, uid, none, t0); len(h) != 0 {
		t.Fatalf("fresh account hides %v", h)
	}
	if _, err := dismissPrompt(ctx, pool, uid, "planner", none, t0); err != nil {
		t.Fatal(err)
	}
	if h := hiddenPrompts(ctx, pool, uid, none, t0.Add(24*time.Hour)); len(h) != 1 || h[0] != "planner" {
		t.Fatalf("after one Not now: %v", h)
	}
	if h := hiddenPrompts(ctx, pool, other, none, t0); len(h) != 0 {
		t.Fatalf("another account is affected: %v", h)
	}
	if h := hiddenPrompts(ctx, pool, uid, none, t0.Add(91*24*time.Hour)); len(h) != 0 {
		t.Fatalf("after 91 days it must show again: %v", h)
	}
	if m, err := dismissPrompt(ctx, pool, uid, "planner", none, t0.Add(91*24*time.Hour)); err != nil || m.Count != 2 {
		t.Fatalf("second Not now: %v %v", m, err)
	}
	if h := hiddenPrompts(ctx, pool, uid, none, t0.Add(1000*24*time.Hour)); len(h) != 1 {
		t.Fatalf("a second Not now hides for good: %v", h)
	}
	if m, _ := dismissPrompt(ctx, pool, uid, "planner", none, t0.Add(92*24*time.Hour)); m.Count != 2 {
		t.Fatalf("the count stops at 2, got %d", m.Count)
	}
	// A plan change after the dismissals brings the prompt back, and the next Not now starts over.
	lapse := t0.Add(200 * 24 * time.Hour)
	if h := hiddenPrompts(ctx, pool, uid, lapse, lapse.Add(time.Hour)); len(h) != 0 {
		t.Fatalf("a lapse must show the prompt again: %v", h)
	}
	if m, _ := dismissPrompt(ctx, pool, uid, "planner", lapse, lapse.Add(time.Hour)); m.Count != 1 {
		t.Fatalf("after a lapse the count starts over, got %d", m.Count)
	}

	// Two writers at once: no lost count.
	v, _ := db.EnsureUser(ctx, pool, "prompts-c")
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := dismissPrompt(ctx, pool, v, "lapse_banner", none, t0); errs <- err }()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if h := hiddenPrompts(ctx, pool, v, none, t0.Add(400*24*time.Hour)); len(h) != 1 {
		t.Fatalf("two concurrent dismissals must make a second one (hidden for good): %v", h)
	}

	// A failed read hides every place.
	closed, _ := db.Open(ctx, testpg.DSN(ctx, t))
	_ = closed.Close()
	if h := hiddenPrompts(ctx, closed, uid, none, t0); len(h) != len(proPromptWheres) {
		t.Fatalf("a failed read must hide all, got %v", h)
	}

	// Deleting the account deletes the state.
	if err := db.DeleteUserAccount(ctx, pool, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM user_settings WHERE user_id = $1 AND value ? $2`, uid, keyPromptState).Scan(&n); err != nil || n != 0 {
		t.Fatalf("prompt state left after account deletion: n=%d err=%v", n, err)
	}
	if h := hiddenPrompts(ctx, pool, other, none, t0); len(h) != 0 {
		t.Fatalf("deleting one account touched another: %v", h)
	}
}
