//go:build integration

// Postgres-backed Store tests. The fake-store tests in leases_test.go
// pin the Coordinator's diff logic; this file pins the SQL itself —
// specifically the load-bearing multi-replica safety net:
//
//   * A healthy lease held by pod A blocks pod B's Acquire.
//   * Once pod A's lease expires, pod B steals it on the next Acquire.
//   * Renew is pod-scoped: bumping A's leases does not touch B's, and
//     does not return B's vehicle IDs.
//   * Release / ReleaseAll never reach across pods.
//
// These are exactly the invariants that, if broken, cause two pods to
// subscribe to the same vehicle (duplicate WS traffic, doubled
// telemetry inserts) or zero pods (silent data loss).
//
// Run with: go test -tags=integration ./internal/leases/...
package leases_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/leases"
)

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
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
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

func setup(t *testing.T) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// expireLease backdates a single row's expires_at so the next Acquire
// from a peer treats it as up-for-grabs. The TTL is 2 minutes and the
// test would otherwise have to sleep; reaching into the row keeps the
// suite fast.
func expireLease(ctx context.Context, t *testing.T, pool *sql.DB, vehicleID string) {
	t.Helper()
	if _, err := pool.ExecContext(ctx,
		`UPDATE subscription_leases SET expires_at = now() - interval '1 minute' WHERE vehicle_id = $1`,
		vehicleID,
	); err != nil {
		t.Fatalf("expire %s: %v", vehicleID, err)
	}
}

// TestAcquire_FreshAndIdempotent: first Acquire wins; re-acquiring
// a vehicle this pod already owns is idempotent and still returns true
// (the ON CONFLICT predicate accepts pod_id = EXCLUDED.pod_id).
func TestAcquire_FreshAndIdempotent(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	a, err := leases.NewStore(pool, "pod-a")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ok, err := a.Acquire(ctx, "v1")
	if err != nil || !ok {
		t.Fatalf("first Acquire: ok=%v err=%v", ok, err)
	}
	ok, err = a.Acquire(ctx, "v1")
	if err != nil || !ok {
		t.Fatalf("idempotent re-Acquire: ok=%v err=%v", ok, err)
	}
}

// TestAcquire_BlockedByHealthyPeer is the linchpin. A healthy lease
// held by pod A must block pod B's Acquire — otherwise two pods
// subscribe to the same vehicle.
func TestAcquire_BlockedByHealthyPeer(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	a, _ := leases.NewStore(pool, "pod-a")
	b, _ := leases.NewStore(pool, "pod-b")

	if ok, err := a.Acquire(ctx, "v1"); err != nil || !ok {
		t.Fatalf("A Acquire: ok=%v err=%v", ok, err)
	}
	ok, err := b.Acquire(ctx, "v1")
	if err != nil {
		t.Fatalf("B Acquire err: %v", err)
	}
	if ok {
		t.Fatal("B stole a healthy lease — two pods would subscribe to v1")
	}
}

// TestAcquire_StealsExpiredPeer covers the crash-recovery path. Pod A
// crashes (its row is now stale); pod B's next Acquire takes over.
// Without this, a crashed pod's vehicles stay unsubscribed for the
// full TTL.
func TestAcquire_StealsExpiredPeer(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	a, _ := leases.NewStore(pool, "pod-a")
	b, _ := leases.NewStore(pool, "pod-b")

	if ok, _ := a.Acquire(ctx, "v1"); !ok {
		t.Fatal("A Acquire")
	}
	expireLease(ctx, t, pool, "v1")

	ok, err := b.Acquire(ctx, "v1")
	if err != nil {
		t.Fatalf("B Acquire: %v", err)
	}
	if !ok {
		t.Fatal("B failed to steal an expired lease — crashed pod's vehicles would be orphaned")
	}
	// And A's next Renew must not see v1 anymore.
	owned, err := a.Renew(ctx)
	if err != nil {
		t.Fatalf("A Renew: %v", err)
	}
	for _, v := range owned {
		if v == "v1" {
			t.Errorf("A still sees v1 after losing it to B: %v", owned)
		}
	}
}

// TestRenew_PodScoped: Renew only touches and returns the calling
// pod's rows. A cross-pod refactor that drops the WHERE pod_id clause
// would silently extend peer leases.
func TestRenew_PodScoped(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	a, _ := leases.NewStore(pool, "pod-a")
	b, _ := leases.NewStore(pool, "pod-b")

	for _, v := range []string{"v1", "v2"} {
		if ok, err := a.Acquire(ctx, v); err != nil || !ok {
			t.Fatalf("A Acquire %s: ok=%v err=%v", v, ok, err)
		}
	}
	if ok, err := b.Acquire(ctx, "v3"); err != nil || !ok {
		t.Fatalf("B Acquire v3: ok=%v err=%v", ok, err)
	}

	ownedA, err := a.Renew(ctx)
	if err != nil {
		t.Fatalf("A Renew: %v", err)
	}
	sort.Strings(ownedA)
	if len(ownedA) != 2 || ownedA[0] != "v1" || ownedA[1] != "v2" {
		t.Errorf("A Renew returned wrong set: %v", ownedA)
	}

	ownedB, err := b.Renew(ctx)
	if err != nil {
		t.Fatalf("B Renew: %v", err)
	}
	if len(ownedB) != 1 || ownedB[0] != "v3" {
		t.Errorf("B Renew returned wrong set: %v", ownedB)
	}

	// Crucially: B's Renew must NOT have bumped A's lease. The only
	// observable proof is that A's row still has its original
	// expires_at — checking via Renew on a backdated lease is the
	// clean path.
	expireLease(ctx, t, pool, "v1")
	if _, err := b.Renew(ctx); err != nil {
		t.Fatalf("B Renew (post-expire): %v", err)
	}
	// If B's Renew bumped v1, this Acquire by a third pod would fail.
	c, _ := leases.NewStore(pool, "pod-c")
	ok, err := c.Acquire(ctx, "v1")
	if err != nil {
		t.Fatalf("C Acquire: %v", err)
	}
	if !ok {
		t.Fatal("C couldn't steal v1 — B's Renew leaked across pods")
	}
}

// TestRelease_PodScoped: Release/ReleaseAll only delete rows owned by
// the caller. A cross-pod Release would let one buggy pod take peers
// out of service.
func TestRelease_PodScoped(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	a, _ := leases.NewStore(pool, "pod-a")
	b, _ := leases.NewStore(pool, "pod-b")

	if ok, _ := a.Acquire(ctx, "v1"); !ok {
		t.Fatal("A Acquire v1")
	}
	if ok, _ := b.Acquire(ctx, "v2"); !ok {
		t.Fatal("B Acquire v2")
	}
	// A tries to release B's row — must be a no-op (DELETE…WHERE pod_id matches nothing).
	if err := a.Release(ctx, "v2"); err != nil {
		t.Fatalf("A Release v2: %v", err)
	}
	bOwned, _ := b.Renew(ctx)
	if len(bOwned) != 1 || bOwned[0] != "v2" {
		t.Errorf("A's cross-pod Release deleted B's row: B owns %v", bOwned)
	}

	// ReleaseAll only clears A's rows.
	n, err := a.ReleaseAll(ctx)
	if err != nil {
		t.Fatalf("A ReleaseAll: %v", err)
	}
	if n != 1 {
		t.Errorf("A ReleaseAll affected %d rows, want 1", n)
	}
	bOwned, _ = b.Renew(ctx)
	if len(bOwned) != 1 || bOwned[0] != "v2" {
		t.Errorf("A's ReleaseAll leaked: B owns %v", bOwned)
	}
	// And A now owns nothing.
	aOwned, _ := a.Renew(ctx)
	if len(aOwned) != 0 {
		t.Errorf("A still owns leases after ReleaseAll: %v", aOwned)
	}
}

// TestAcquire_RacingPeersOnExpiredLease pins the "Postgres serializes
// ON CONFLICT" claim in the package doc. Two pods both Acquire(v1)
// concurrently against an expired lease; exactly one must win.
func TestAcquire_RacingPeersOnExpiredLease(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orig, _ := leases.NewStore(pool, "pod-orig")
	if ok, _ := orig.Acquire(ctx, "v1"); !ok {
		t.Fatal("seed acquire")
	}
	expireLease(ctx, t, pool, "v1")

	a, _ := leases.NewStore(pool, "pod-a")
	b, _ := leases.NewStore(pool, "pod-b")

	type result struct {
		who string
		ok  bool
		err error
	}
	out := make(chan result, 2)
	start := make(chan struct{})
	for _, p := range []struct {
		who   string
		store *leases.Store
	}{{"a", a}, {"b", b}} {
		p := p
		go func() {
			<-start
			ok, err := p.store.Acquire(ctx, "v1")
			out <- result{p.who, ok, err}
		}()
	}
	close(start)
	r1 := <-out
	r2 := <-out

	wins := 0
	for _, r := range []result{r1, r2} {
		if r.err != nil {
			t.Fatalf("%s err: %v", r.who, r.err)
		}
		if r.ok {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("racing Acquire: %d wins, want 1 (Postgres serialization broken)", wins)
	}
}
