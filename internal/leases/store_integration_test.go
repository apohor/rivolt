//go:build integration

// Postgres-backed Store tests. The fake-store tests in leases_test.go
// pin the Coordinator's diff logic; this file pins the SQL itself —
// specifically the load-bearing multi-replica safety net:
//
//   - A healthy lease held by pod A blocks pod B's Acquire.
//   - Once pod A's lease expires, pod B steals it on the next Acquire.
//   - Renew is pod-scoped: bumping A's leases does not touch B's, and
//     does not return B's vehicle IDs.
//   - Release / ReleaseAll never reach across pods.
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
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/testpg"
)

func pgDSN(ctx context.Context, t *testing.T) string {
	t.Helper()
	return testpg.DSN(ctx, t)
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

// mkUserVehicle inserts a user, a vehicle, and optionally a stored
// Rivian session, returning the user id. Mirrors the real shape the
// connect flow writes (session row first, then the vehicles row).
func mkUserVehicle(ctx context.Context, t *testing.T, pool *sql.DB, username, rivianVehicleID string, withSession bool) {
	t.Helper()
	var uid string
	if err := pool.QueryRowContext(ctx,
		`INSERT INTO users (id, username, email)
		 VALUES (gen_random_uuid(), $1, $1 || '@example.test') RETURNING id`,
		username,
	).Scan(&uid); err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	if withSession {
		if _, err := pool.ExecContext(ctx,
			`INSERT INTO user_secrets (user_id, name, ciphertext, kek_id)
			 VALUES ($1, 'rivian.session', '\x00', 'test')`, uid,
		); err != nil {
			t.Fatalf("insert session %s: %v", username, err)
		}
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2)`,
		uid, rivianVehicleID,
	); err != nil {
		t.Fatalf("insert vehicle %s: %v", rivianVehicleID, err)
	}
}

func subscribableSet(ctx context.Context, t *testing.T, pool *sql.DB) map[string]bool {
	t.Helper()
	ids, err := db.ListSubscribableVehicleIDs(ctx, pool)
	if err != nil {
		t.Fatalf("ListSubscribableVehicleIDs: %v", err)
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// TestListSubscribableVehicleIDs pins the lease coordinator's
// authoritative set: only vehicles whose owner has a stored Rivian
// session are subscribable. If this SQL ever silently returns a vehicle
// with no session (or drops one that has a session), the coordinator
// either keeps recording a disconnected user or tears down every live
// subscription — so lock the exact membership.
func TestListSubscribableVehicleIDs(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()

	mkUserVehicle(ctx, t, pool, "connected", "01-connected", true)
	mkUserVehicle(ctx, t, pool, "no-session", "01-nosession", false)
	mkUserVehicle(ctx, t, pool, "electrafi", "electrafi-abc123", true)

	got := subscribableSet(ctx, t, pool)
	if !got["01-connected"] {
		t.Error("vehicle with a stored session must be subscribable")
	}
	if got["01-nosession"] {
		t.Error("vehicle whose owner has no session must NOT be subscribable")
	}
	if got["electrafi-abc123"] {
		t.Error("synthetic electrafi- import rows must be excluded")
	}

	// The logout case: deleting the session row must drop the vehicle
	// from the set on the very next call, which is what makes disconnect
	// reap the lease across replicas.
	if _, err := pool.ExecContext(ctx,
		`DELETE FROM user_secrets WHERE name = 'rivian.session'
		   AND user_id = (SELECT user_id FROM vehicles WHERE rivian_vehicle_id = '01-connected')`,
	); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if subscribableSet(ctx, t, pool)["01-connected"] {
		t.Error("after logout (session deleted) the vehicle must leave the subscribable set")
	}
}

// ---- user-affine leases: one pod holds all of a user's vehicles -------------

func seedUserVehicles(ctx context.Context, t *testing.T, pool *sql.DB, user string, rivianIDs ...string) {
	t.Helper()
	uid, err := db.EnsureUser(ctx, pool, user)
	if err != nil {
		t.Fatalf("EnsureUser %s: %v", user, err)
	}
	for _, id := range rivianIDs {
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,$2)`, uid, id); err != nil {
			t.Fatalf("vehicle %s: %v", id, err)
		}
	}
}

func twoPods(t *testing.T, pool *sql.DB) (*leases.Store, *leases.Store) {
	t.Helper()
	a, err := leases.NewStore(pool, "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := leases.NewStore(pool, "pod-b")
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

// A pod that holds one of a user's vehicles keeps the others: the peer is refused
// them (Rivian's gateway takes one connection per session token), while a
// different user's vehicle is free for the peer.
func TestAcquire_UserVehiclesStayOnOnePod(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "ua-alice", "ua-a1", "ua-a2")
	seedUserVehicles(ctx, t, pool, "ua-bob", "ua-b1")
	a, b := twoPods(t, pool)

	if ok, err := a.Acquire(ctx, "ua-a1"); err != nil || !ok {
		t.Fatalf("pod-a first vehicle: ok=%v err=%v", ok, err)
	}
	if ok, err := b.Acquire(ctx, "ua-a2"); err != nil || ok {
		t.Fatalf("pod-b took the second vehicle of a user pod-a holds: ok=%v err=%v", ok, err)
	}
	if ok, err := a.Acquire(ctx, "ua-a2"); err != nil || !ok {
		t.Fatalf("pod-a second vehicle: ok=%v err=%v", ok, err)
	}
	if ok, err := b.Acquire(ctx, "ua-b1"); err != nil || !ok {
		t.Fatalf("pod-b other user's vehicle: ok=%v err=%v", ok, err)
	}
}

// Both pods racing for both of a user's vehicles never end up with one each.
func TestAcquire_RaceNeverSplitsAUser(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	for round := 0; round < 15; round++ {
		u := "ua-race-" + string(rune('a'+round))
		v1, v2 := u+"-1", u+"-2"
		seedUserVehicles(ctx, t, pool, u, v1, v2)
		a, b := twoPods(t, pool)
		var wg sync.WaitGroup
		for _, p := range []*leases.Store{a, b} {
			for _, v := range []string{v1, v2} {
				wg.Add(1)
				go func() { defer wg.Done(); _, _ = p.Acquire(ctx, v) }()
			}
		}
		wg.Wait()
		var pods int
		if err := pool.QueryRowContext(ctx, `SELECT count(DISTINCT pod_id) FROM subscription_leases WHERE vehicle_id IN ($1,$2)`, v1, v2).Scan(&pods); err != nil {
			t.Fatal(err)
		}
		if pods > 1 {
			t.Fatalf("round %d: the user's vehicles are on %d pods", round, pods)
		}
	}
}

// When the holder's leases expire the peer takes the user over, one vehicle at a
// time as each lapses.
func TestAcquire_UserHandoverAfterExpiry(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "ua-hand", "ua-h1", "ua-h2")
	a, b := twoPods(t, pool)
	for _, v := range []string{"ua-h1", "ua-h2"} {
		if ok, _ := a.Acquire(ctx, v); !ok {
			t.Fatalf("pod-a %s", v)
		}
	}
	// h1 lapses first; h2 is still healthy, so the peer is still refused h1.
	expireLease(ctx, t, pool, "ua-h1")
	if ok, _ := b.Acquire(ctx, "ua-h1"); ok {
		t.Fatal("pod-b took h1 while pod-a still holds a healthy lease on h2")
	}
	expireLease(ctx, t, pool, "ua-h2")
	for _, v := range []string{"ua-h1", "ua-h2"} {
		if ok, err := b.Acquire(ctx, v); err != nil || !ok {
			t.Fatalf("pod-b %s after both lapsed: ok=%v err=%v", v, ok, err)
		}
	}
}

// A split already in the table (left by a rollout, or by the per-vehicle rule) is
// reported to the pod with the greater id only, so one of them yields and the
// split converges instead of both releasing.
func TestSplitVehicles_OnlyTheGreaterPodYields(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "ua-split", "ua-s1", "ua-s2")
	a, b := twoPods(t, pool)
	for _, row := range [][2]string{{"ua-s1", "pod-a"}, {"ua-s2", "pod-b"}} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO subscription_leases (vehicle_id, pod_id, acquired_at, renewed_at, expires_at) VALUES ($1,$2,now(),now(),now()+interval '2 minutes')`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	gotA, err := a.SplitVehicles(ctx)
	if err != nil || len(gotA) != 0 {
		t.Fatalf("pod-a (lower id) split vehicles = %v, %v; want none", gotA, err)
	}
	gotB, err := b.SplitVehicles(ctx)
	if err != nil || len(gotB) != 1 || gotB[0] != "ua-s2" {
		t.Fatalf("pod-b split vehicles = %v, %v; want [ua-s2]", gotB, err)
	}
	// Once pod-b gives it up, pod-a can take it and the user is whole.
	if err := b.Release(ctx, "ua-s2"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Acquire(ctx, "ua-s2"); !ok {
		t.Fatal("pod-a could not take the released vehicle")
	}
	if got, _ := b.SplitVehicles(ctx); len(got) != 0 {
		t.Errorf("still split after healing: %v", got)
	}
}

// The same Rivian id under two accounts (the same VIN, seen on preview): each
// user is checked on its own, and the locks are taken in a fixed order so two
// pods acquiring the shared vehicle do not deadlock.
func TestAcquire_SameRivianIDUnderTwoUsers(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "ua-two-1", "ua-shared", "ua-own-1")
	seedUserVehicles(ctx, t, pool, "ua-two-2", "ua-shared", "ua-own-2")
	a, b := twoPods(t, pool)
	if ok, _ := a.Acquire(ctx, "ua-own-1"); !ok {
		t.Fatal("pod-a own-1")
	}
	// The shared id belongs to user 1 too, so pod-b is refused it.
	if ok, _ := b.Acquire(ctx, "ua-shared"); ok {
		t.Fatal("pod-b took a vehicle shared with a user pod-a holds")
	}
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, p := range []*leases.Store{a, b} {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = p.Acquire(ctx, "ua-shared"); _, _ = p.Acquire(ctx, "ua-own-2") }()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("acquires deadlocked")
	}
}

// A vehicle shared by two accounts must not be stranded. User A has {X, Y}, user B
// has {Y, Z}; pod-a holds X and pod-b holds Z. Linking users through the shared id
// puts A and B in one group, so Y is refused to both pods for now, and the
// greater pod (pod-b) is told to yield; after it does, pod-a takes Y and Z and the
// whole group is on one pod (review of #602).
func TestSharedVIN_GroupHealsInsteadOfStrandingTheSharedVehicle(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "sv-a", "sv-x", "sv-y")
	seedUserVehicles(ctx, t, pool, "sv-b", "sv-y", "sv-z")
	a, b := twoPods(t, pool)
	for _, row := range [][2]string{{"sv-x", "pod-a"}, {"sv-z", "pod-b"}} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO subscription_leases (vehicle_id, pod_id, acquired_at, renewed_at, expires_at) VALUES ($1,$2,now(),now(),now()+interval '2 minutes')`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	// Y is refused to both pods while the group is split.
	if ok, _ := a.Acquire(ctx, "sv-y"); ok {
		t.Fatal("pod-a took the shared vehicle while pod-b holds the group's other vehicle")
	}
	if ok, _ := b.Acquire(ctx, "sv-y"); ok {
		t.Fatal("pod-b took the shared vehicle while pod-a holds the group's other vehicle")
	}
	// The split is seen by the greater pod only, across the shared id.
	if got, err := a.SplitVehicles(ctx); err != nil || len(got) != 0 {
		t.Fatalf("pod-a split vehicles = %v, %v; want none", got, err)
	}
	got, err := b.SplitVehicles(ctx)
	if err != nil || len(got) != 1 || got[0] != "sv-z" {
		t.Fatalf("pod-b split vehicles = %v, %v; want [sv-z]", got, err)
	}
	// pod-b yields; pod-a now takes everything and the group is whole.
	if err := b.Release(ctx, "sv-z"); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"sv-y", "sv-z"} {
		if ok, err := a.Acquire(ctx, v); err != nil || !ok {
			t.Fatalf("pod-a %s after healing: ok=%v err=%v", v, ok, err)
		}
	}
	var pods int
	if err := pool.QueryRowContext(ctx, `SELECT count(DISTINCT pod_id) FROM subscription_leases WHERE vehicle_id IN ('sv-x','sv-y','sv-z')`).Scan(&pods); err != nil {
		t.Fatal(err)
	}
	if pods != 1 {
		t.Errorf("the group is on %d pods, want 1", pods)
	}
	if got, _ := b.SplitVehicles(ctx); len(got) != 0 {
		t.Errorf("still split: %v", got)
	}
}

// Users who share nothing stay separate groups: pod-b may hold one while pod-a
// holds another.
func TestSharedVIN_UnlinkedUsersAreNotGrouped(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "sv-solo-1", "sv-s1a", "sv-s1b")
	seedUserVehicles(ctx, t, pool, "sv-solo-2", "sv-s2a")
	a, b := twoPods(t, pool)
	if ok, _ := a.Acquire(ctx, "sv-s1a"); !ok {
		t.Fatal("pod-a s1a")
	}
	if ok, _ := b.Acquire(ctx, "sv-s2a"); !ok {
		t.Fatal("pod-b took an unrelated user's vehicle: grouping is too wide")
	}
	if got, _ := b.SplitVehicles(ctx); len(got) != 0 {
		t.Errorf("unlinked users reported as split: %v", got)
	}
}
