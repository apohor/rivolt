//go:build integration

// trip_plan_links against real Postgres: exclusivity, never re-suggest,
// snapshots, user scoping, cascade.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/trips"
)

func TestPlanLinks_PG(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, err := db.EnsureUser(ctx, pool, "plan-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "plan-bob")
	if err != nil {
		t.Fatal(err)
	}
	tf := trips.NewFactory(pool)
	st, err := tf.For(alice).Create(ctx, "Vegas", json.RawMessage(`{"a":1}`), json.RawMessage(`{"b":2}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := pgPlanLinks{db: pool}
	t0 := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	conf := 0.91
	mk := func(id string) planLink {
		return planLink{TripID: id, Status: "linked", Confidence: &conf, StartedAt: t0, EndedAt: t0.Add(30 * time.Hour),
			Snapshot: planSnapshot{Inputs: json.RawMessage(`{"a":1}`), Plan: json.RawMessage(`{"b":2}`)}}
	}
	if err := store.Link(ctx, alice, st.ID, mk("trip_a")); err != nil {
		t.Fatal(err)
	}
	if err := store.Link(ctx, alice, st.ID, mk("trip_b")); err != nil {
		t.Fatal(err)
	}
	rows, err := store.List(ctx, alice, st.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %d (%v)", len(rows), err)
	}
	linked := map[string]string{}
	for _, r := range rows {
		linked[r.TripID] = r.Status
	}
	if linked["trip_a"] != "unlinked" || linked["trip_b"] != "linked" {
		t.Fatalf("linking another outing must unlink the first: %v", linked)
	}
	if string(rows[0].Snapshot.Plan) != `{"b": 2}` && string(rows[0].Snapshot.Plan) != `{"b":2}` {
		t.Fatalf("snapshot = %s", rows[0].Snapshot.Plan)
	}
	// The same outing can be re-linked later.
	if err := store.Link(ctx, alice, st.ID, mk("trip_a")); err != nil {
		t.Fatal(err)
	}
	// Unlink records the outing so it stays unlinked.
	if err := store.Unlink(ctx, alice, st.ID, mk("trip_c")); err != nil {
		t.Fatal(err)
	}
	rows, _ = store.List(ctx, alice, st.ID)
	n := 0
	for _, r := range rows {
		if r.Status == "linked" {
			n++
		}
	}
	if n != 1 || len(rows) != 3 {
		t.Fatalf("rows = %d, linked = %d", len(rows), n)
	}
	// Bob can neither read nor link Alice's saved trip.
	if got, _ := store.List(ctx, bob, st.ID); len(got) != 0 {
		t.Fatalf("bob sees %d rows", len(got))
	}
	if err := store.Link(ctx, bob, st.ID, mk("trip_x")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("bob link err = %v, want ErrNoRows", err)
	}
	if err := store.Unlink(ctx, bob, st.ID, mk("trip_y")); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM trip_plan_links WHERE user_id = $1`, bob).Scan(&count); err != nil || count != 0 {
		t.Fatalf("bob rows = %d (%v)", count, err)
	}
	// Deleting the saved trip removes its links.
	if err := tf.For(alice).Delete(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM trip_plan_links WHERE saved_trip_id = $1`, st.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("links after delete = %d (%v)", count, err)
	}
}
