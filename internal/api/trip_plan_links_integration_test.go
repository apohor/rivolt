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
	rows, err := store.List(ctx, alice, st.ID, 0)
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
	// Re-linking the already-linked outing after a re-plan must keep
	// the original snapshot.
	replanned := mk("trip_b")
	replanned.Snapshot = planSnapshot{Inputs: json.RawMessage(`{"a":9}`), Plan: json.RawMessage(`{"b":9}`)}
	if err := store.Link(ctx, alice, st.ID, replanned); err != nil {
		t.Fatal(err)
	}
	rows, _ = store.List(ctx, alice, st.ID, 0)
	for _, r := range rows {
		if r.TripID == "trip_b" && (string(r.Snapshot.Plan) == `{"b": 9}` || string(r.Snapshot.Plan) == `{"b":9}`) {
			t.Fatalf("snapshot of an already-linked outing was rewritten: %s", r.Snapshot.Plan)
		}
	}
	// The same outing can be re-linked later.
	if err := store.Link(ctx, alice, st.ID, mk("trip_a")); err != nil {
		t.Fatal(err)
	}
	// Unlink records the outing so it stays unlinked.
	if err := store.Unlink(ctx, alice, st.ID, mk("trip_c")); err != nil {
		t.Fatal(err)
	}
	rows, _ = store.List(ctx, alice, st.ID, 0)
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
	if got, _ := store.List(ctx, bob, st.ID, 0); len(got) != 0 {
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

// A multi-day saved trip's days each link their own outing, and several days
// can link the SAME outing (one recorded trip carries all the days).
func TestPlanLinks_PerDay_PG(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "plan-days")
	if err != nil {
		t.Fatal(err)
	}
	st, err := trips.NewFactory(pool).For(uid).Create(ctx, "3 days", json.RawMessage(`{"a":1}`), json.RawMessage(`{"kind":"multiday","days":[]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := pgPlanLinks{db: pool}
	t0 := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	mk := func(day int, id string) planLink {
		conf := 0.9
		return planLink{Day: day, TripID: id, Status: "linked", Confidence: &conf, StartedAt: t0, EndedAt: t0.Add(72 * time.Hour),
			Snapshot: planSnapshot{Inputs: json.RawMessage(`{"d":1}`), Plan: json.RawMessage(`{"p":1}`)}}
	}
	// Three days, one outing: the primary key must allow it.
	for day := 1; day <= 3; day++ {
		if err := store.Link(ctx, uid, st.ID, mk(day, "trip_one")); err != nil {
			t.Fatalf("link day %d: %v", day, err)
		}
	}
	for day := 1; day <= 3; day++ {
		rows, err := store.List(ctx, uid, st.ID, day)
		if err != nil || len(rows) != 1 || rows[0].Status != "linked" || rows[0].Day != day {
			t.Fatalf("day %d rows = %+v (%v)", day, rows, err)
		}
	}
	// Linking another outing for day 2 unlinks day 2's first one and nobody else's.
	if err := store.Link(ctx, uid, st.ID, mk(2, "trip_two")); err != nil {
		t.Fatal(err)
	}
	status := func(day int, id string) string {
		rows, _ := store.List(ctx, uid, st.ID, day)
		for _, r := range rows {
			if r.TripID == id {
				return r.Status
			}
		}
		return "absent"
	}
	if status(2, "trip_one") != "unlinked" || status(2, "trip_two") != "linked" || status(1, "trip_one") != "linked" || status(3, "trip_one") != "linked" {
		t.Fatalf("per-day exclusivity broken: d2 one=%s two=%s d1=%s d3=%s", status(2, "trip_one"), status(2, "trip_two"), status(1, "trip_one"), status(3, "trip_one"))
	}
	// The whole-trip scope (day 0) is separate from the days.
	if rows, _ := store.List(ctx, uid, st.ID, 0); len(rows) != 0 {
		t.Fatalf("day 0 sees the days' rows: %+v", rows)
	}
	// At most one linked row per day, enforced by the database.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO trip_plan_links (saved_trip_id, user_id, day_index, trip_id, status, started_at, ended_at, plan_snapshot)
		VALUES ($1, $2, 1, 'trip_other', 'linked', now(), now(), '{}'::jsonb)`, st.ID, uid); err == nil {
		t.Fatal("two linked rows for one day were accepted")
	}
	// Unlinking is per day too.
	if err := store.Unlink(ctx, uid, st.ID, mk(3, "trip_one")); err != nil {
		t.Fatal(err)
	}
	if status(3, "trip_one") != "unlinked" || status(1, "trip_one") != "linked" {
		t.Fatalf("unlink day 3: d3=%s d1=%s", status(3, "trip_one"), status(1, "trip_one"))
	}
	// A negative day is refused by the table.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO trip_plan_links (saved_trip_id, user_id, day_index, trip_id, status, started_at, ended_at, plan_snapshot)
		VALUES ($1, $2, -1, 'x', 'unlinked', now(), now(), '{}'::jsonb)`, st.ID, uid); err == nil {
		t.Fatal("a negative day_index was accepted")
	}
}
