//go:build integration

package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

func TestEventsSink_InsertPurgeAndActivatedView(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "events-user")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "events-late")
	if err != nil {
		t.Fatal(err)
	}
	sink := newPGEventsSink(pool)
	if err := sink.Insert(ctx, "anon-0123456789abcdef", &uid, []storedEvent{
		{Name: "rivian_connect_done", Props: map[string]any{"vehicle_count": int64(1)}},
		{Name: "receipt_open", Props: map[string]any{"source": "billed"}},
	}); err != nil {
		t.Fatal(err)
	}
	// A user whose receipt view came 10 days after connecting is not activated.
	for _, q := range []string{
		`INSERT INTO events (anon_id, user_id, name, ts) VALUES ('anon-late-0123456789', $1, 'rivian_connect_done', now() - interval '20 days')`,
		`INSERT INTO events (anon_id, user_id, name, ts) VALUES ('anon-late-0123456789', $1, 'receipt_open', now() - interval '10 days')`,
		`INSERT INTO events (anon_id, name, ts) VALUES ('anon-old-0123456789ab', 'landing_view', now() - interval '100 days')`,
	} {
		var args []any
		if strings.Contains(q, "$1") {
			args = []any{other}
		}
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM activated WHERE user_id = $1`, uid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("activated for user = %d (%v)", n, err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM activated WHERE user_id = $1`, other).Scan(&n); err != nil || n != 0 {
		t.Fatalf("late user activated = %d (%v)", n, err)
	}
	// Purge removes only rows older than 90 days.
	sink.lastPurge.Store(0) // Insert already ran (and rate-limited) a purge
	sink.maybePurge()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE name = 'landing_view'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n != 0 {
		t.Fatalf("100-day-old event not purged")
	}
}
