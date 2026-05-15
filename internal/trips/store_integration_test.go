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
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/trips"
)

// pgDSN spins up postgres:16-alpine and returns a host-reachable
// DSN. Same pattern as internal/integration/boot_to_record_test.go;
// container is torn down via t.Cleanup.
func pgDSN(ctx context.Context, t *testing.T) string {
	t.Helper()
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "rivolt",
			"POSTGRES_PASSWORD": "rivolt",
			"POSTGRES_DB":       "rivolt_test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Terminate(context.Background())
	})
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	return fmt.Sprintf("postgres://rivolt:rivolt@%s:%s/rivolt_test?sslmode=disable", host, port.Port())
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
	if string(created.Plan) != string(plan) {
		t.Errorf("Plan roundtrip: got %s want %s", created.Plan, plan)
	}
	if string(created.Advice) != string(advice) {
		t.Errorf("Advice roundtrip: got %s want %s", created.Advice, advice)
	}

	got, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID || string(got.Plan) != string(plan) || string(got.Advice) != string(advice) {
		t.Errorf("Get roundtrip mismatch: %+v", got)
	}
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
	if updated.Name != "renamed" || string(updated.Plan) != `{"second":true}` || string(updated.Advice) != `{"hint":"ok"}` {
		t.Errorf("Update roundtrip wrong: %+v", updated)
	}

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
