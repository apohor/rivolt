//go:build integration

// Trips store integration tests — run against a real Postgres via
// testcontainers because the bugs we ship here (v0.18.58 NULL panic,
// v0.18.78 RawBytes regression) are driver-level, not query-logic
// level. A sqlmock-style fake would have passed both.
//
// Run with:
//
//	go test -tags=integration ./internal/trips/...
//
// Set RIVOLT_SKIP_INTEGRATION=1 to skip on CI runners without Docker.
package trips_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
	"github.com/apohor/rivolt/internal/trips"
)

// jsonEq compares two JSON byte slices for semantic equality. Postgres
// JSONB re-orders keys and adds whitespace after colons, so byte-equal
// is wrong; deep-equal on decoded values is what we actually want.
func jsonEq(t *testing.T, label string, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: invalid JSON in got: %v (%s)", label, err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: invalid JSON in want: %v (%s)", label, err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s mismatch:\n  got:  %s\n  want: %s", label, got, want)
	}
}

// pgDSN spins up postgres:16-alpine and returns a host-reachable
// DSN. Same pattern as internal/integration/boot_to_record_test.go;
// container is torn down via t.Cleanup.
func pgDSN(ctx context.Context, t *testing.T) string {
	t.Helper()
	return testpg.DSN(ctx, t)
}

// setupStore brings up Postgres, runs every embedded migration, seeds
// a test user, and returns a Store bound to that user. Shared by
// every test in this file.
func setupStore(t *testing.T) *trips.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	dsn := pgDSN(ctx, t)
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "tripstest")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	store, err := trips.OpenStore(pool, uid)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return store
}

// TestCreateAndGet_AllFields covers the happy path: Plan + Advice
// both populated. Exercises Create's RETURNING scan and Get's
// SELECT scan against non-NULL JSONB columns.
func TestCreateAndGet_AllFields(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	inputs := json.RawMessage(`{"origin":"Austin","destination":"Dallas"}`)
	plan := json.RawMessage(`{"distance_mi":195,"legs":[{"id":"a"}]}`)
	advice := json.RawMessage(`{"summary":"go for it","priority":"low"}`)

	created, err := store.Create(ctx, "Austin → Dallas", inputs, plan, advice)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("Create returned nil ID")
	}
	if created.Name != "Austin → Dallas" {
		t.Errorf("Name: got %q", created.Name)
	}
	jsonEq(t, "Plan after Create", created.Plan, plan)
	jsonEq(t, "Advice after Create", created.Advice, advice)

	got, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("Get ID mismatch: %s vs %s", got.ID, created.ID)
	}
	jsonEq(t, "Plan after Get", got.Plan, plan)
	jsonEq(t, "Advice after Get", got.Advice, advice)
}

// TestCreateAndGet_NullPlanAdvice is the regression test for
// v0.18.58 (NULL into *json.RawMessage panic) AND v0.18.78
// (sql.RawBytes rejected by Row.Scan). The trip is saved with
// nothing-but-inputs and read back — both bugs would surface here.
func TestCreateAndGet_NullPlanAdvice(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	created, err := store.Create(
		ctx,
		"unplanned",
		json.RawMessage(`{"origin":"home"}`),
		nil, // plan = NULL
		nil, // advice = NULL
	)
	if err != nil {
		t.Fatalf("Create with NULL plan/advice: %v", err)
	}
	if len(created.Plan) != 0 {
		t.Errorf("Plan should be nil/empty on NULL row, got %s", created.Plan)
	}
	if len(created.Advice) != 0 {
		t.Errorf("Advice should be nil/empty on NULL row, got %s", created.Advice)
	}
	got, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get NULL row: %v", err)
	}
	if len(got.Plan) != 0 || len(got.Advice) != 0 {
		t.Errorf("Get on NULL row leaked bytes: plan=%s advice=%s", got.Plan, got.Advice)
	}
}

// TestUpdate covers both shapes: setting plan/advice to fresh JSON
// AND clearing them (NULL again). Exercises the same Row.Scan path
// that crashed in v0.18.78 — Update's RETURNING never made it to
// the caller before this fix.
func TestUpdate(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	created, err := store.Create(
		ctx,
		"to update",
		json.RawMessage(`{"v":1}`),
		json.RawMessage(`{"first":true}`),
		nil,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update with new plan + advice.
	updated, err := store.Update(
		ctx,
		created.ID,
		"renamed",
		json.RawMessage(`{"v":2}`),
		json.RawMessage(`{"second":true}`),
		json.RawMessage(`{"hint":"ok"}`),
	)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "renamed" {
		t.Errorf("Update name: got %q want renamed", updated.Name)
	}
	jsonEq(t, "Plan after Update", updated.Plan, []byte(`{"second":true}`))
	jsonEq(t, "Advice after Update", updated.Advice, []byte(`{"hint":"ok"}`))

	// Update again, clearing plan + advice. Without the v0.18.78 fix
	// this would 500 with the RawBytes error.
	cleared, err := store.Update(
		ctx,
		created.ID,
		"renamed again",
		json.RawMessage(`{"v":3}`),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Update clearing plan/advice: %v", err)
	}
	if len(cleared.Plan) != 0 || len(cleared.Advice) != 0 {
		t.Errorf("Cleared row still has plan/advice: %+v", cleared)
	}
}

// TestUpdate_NotFound asserts Update returns ErrNotFound for an id
// that doesn't exist (or belongs to another user).
func TestUpdate_NotFound(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	_, err := store.Update(
		ctx,
		uuid.New(),
		"ghost",
		json.RawMessage(`{}`),
		nil,
		nil,
	)
	if !errors.Is(err, trips.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// TestList exercises the Rows.Scan path (vs Row.Scan in Get/Create/
// Update). Ensures the same []byte handling works there too —
// catches the case where someone "fixes" Row.Scan and breaks
// Rows.Scan or vice versa.
func TestList(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := store.Create(
			ctx,
			fmt.Sprintf("trip-%d", i),
			json.RawMessage(`{"i":1}`),
			nil,
			nil,
		)
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		// Small gap so updated_at can sort deterministically.
		time.Sleep(10 * time.Millisecond)
	}

	got, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 trips, got %d", len(got))
	}
	// List returns most-recently-updated first.
	if got[0].Name != "trip-2" || got[2].Name != "trip-0" {
		t.Errorf("List ordering wrong: %s, %s, %s", got[0].Name, got[1].Name, got[2].Name)
	}
}

// TestDelete + Get-after-Delete completes the CRUD ring.
func TestDelete(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	created, err := store.Create(ctx, "doomed", json.RawMessage(`{}`), nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, created.ID); !errors.Is(err, trips.ErrNotFound) {
		t.Errorf("Get after Delete: want ErrNotFound, got %v", err)
	}
	if err := store.Delete(ctx, created.ID); !errors.Is(err, trips.ErrNotFound) {
		t.Errorf("second Delete: want ErrNotFound, got %v", err)
	}
}

// A phone token must not be able to store without bound: Create refuses the (MaxPerUser+1)th trip, atomically, per account; an
// edit at the cap still works, and deleting one makes room.
func TestCreateStopsAtThePerUserCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "cap-a")
	b, _ := db.EnsureUser(ctx, pool, "cap-b")
	sa, _ := trips.OpenStore(pool, a)
	sb, _ := trips.OpenStore(pool, b)
	inputs := json.RawMessage(`{"n":1}`)
	var first uuid.UUID
	for i := 0; i < trips.MaxPerUser; i++ {
		tr, err := sa.Create(ctx, fmt.Sprintf("trip %03d", i), inputs, nil, nil)
		if err != nil {
			t.Fatalf("trip %d: %v", i, err)
		}
		if i == 0 {
			first = tr.ID
		}
	}
	if _, err := sa.Create(ctx, "one too many", inputs, nil, nil); !errors.Is(err, trips.ErrLimit) {
		t.Fatalf("past the cap: %v, want ErrLimit", err)
	}
	if _, err := sb.Create(ctx, "someone else's first", inputs, nil, nil); err != nil {
		t.Errorf("another account is limited by this one: %v", err)
	}
	if _, err := sa.Update(ctx, first, "renamed at the cap", inputs, nil, nil); err != nil {
		t.Errorf("an edit at the cap was refused: %v", err)
	}
	if err := sa.Delete(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := sa.Create(ctx, "room again", inputs, nil, nil); err != nil {
		t.Errorf("after a delete: %v", err)
	}
}
