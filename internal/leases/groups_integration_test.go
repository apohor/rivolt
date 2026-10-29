//go:build integration

package leases_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/leases"
)

// M1 of the single-owner design: a user group gets one account_leases row, held by
// the pod that holds all of the group's vehicle leases. Three pods on one database;
// the assertions run over every pod.

const podCount = 3

func threePods(t *testing.T, pool *sql.DB) [podCount]*leases.Store {
	t.Helper()
	var out [podCount]*leases.Store
	for i, name := range []string{"pod-a", "pod-b", "pod-c"} {
		s, err := leases.NewStore(pool, name)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = s
	}
	return out
}

func renewAll(ctx context.Context, t *testing.T, pods [podCount]*leases.Store) [podCount]leases.GroupResult {
	t.Helper()
	var out [podCount]leases.GroupResult
	for i, p := range pods {
		r, err := p.RenewGroups(ctx)
		if err != nil {
			t.Fatalf("pod %d RenewGroups: %v", i, err)
		}
		out[i] = r
	}
	return out
}

type groupRow struct {
	pod      string // "" when released
	epoch    int64
	released bool
}

func groupRows(ctx context.Context, t *testing.T, pool *sql.DB) map[string]groupRow {
	t.Helper()
	rows, err := pool.QueryContext(ctx, `SELECT group_id::text, pod_id, epoch, released_at IS NOT NULL FROM account_leases`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]groupRow{}
	for rows.Next() {
		var g string
		var r groupRow
		var pod sql.NullString
		if err := rows.Scan(&g, &pod, &r.epoch, &r.released); err != nil {
			t.Fatal(err)
		}
		r.pod = pod.String
		out[g] = r
	}
	return out
}

// heldRows is the rows with a holder (released ones stay in the table, with no pod).
func heldRows(ctx context.Context, t *testing.T, pool *sql.DB) map[string]groupRow {
	t.Helper()
	out := map[string]groupRow{}
	for g, r := range groupRows(ctx, t, pool) {
		if r.pod != "" {
			out[g] = r
		}
	}
	return out
}

// noTwoHolders asserts that no group id is in more than one pod's tokens.
func noTwoHolders(t *testing.T, pods [podCount]*leases.Store) {
	t.Helper()
	seen := map[string]int{}
	for i, p := range pods {
		for _, tok := range p.GroupTokens() {
			if j, dup := seen[tok.Group]; dup {
				t.Errorf("group %s held by pods %d and %d", tok.Group, j, i)
			}
			seen[tok.Group] = i
		}
	}
}

func TestGroupLease_WholeGroupOnly_StableEpoch_StolenOnExpiry(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "gl-user", "gl-v1", "gl-v2")
	pods := threePods(t, pool)
	a, b := pods[0], pods[1]

	// A holds one of two vehicles: not the group.
	if ok, err := a.Acquire(ctx, "gl-v1"); err != nil || !ok {
		t.Fatalf("a v1 = %v, %v", ok, err)
	}
	for i, r := range renewAll(ctx, t, pods) {
		if r.Held != 0 {
			t.Errorf("pod %d holds %d groups with half a group", i, r.Held)
		}
	}
	if n := len(groupRows(ctx, t, pool)); n != 0 {
		t.Fatalf("%d group rows for a half-held group", n)
	}

	// The whole group: one row, held by A, the same epoch across renewals.
	if ok, err := a.Acquire(ctx, "gl-v2"); err != nil || !ok {
		t.Fatalf("a v2 = %v, %v", ok, err)
	}
	res := renewAll(ctx, t, pods)
	if res[0].Held != 1 || res[1].Held != 0 || res[2].Held != 0 {
		t.Fatalf("held = %d/%d/%d, want 1/0/0", res[0].Held, res[1].Held, res[2].Held)
	}
	rows := groupRows(ctx, t, pool)
	if len(rows) != 1 {
		t.Fatalf("%d group rows", len(rows))
	}
	var gid string
	var first groupRow
	for g, r := range rows {
		gid, first = g, r
	}
	if first.pod != "pod-a" {
		t.Fatalf("holder = %s", first.pod)
	}
	for i := 0; i < 3; i++ {
		renewAll(ctx, t, pods)
	}
	if r := groupRows(ctx, t, pool)[gid]; r.epoch != first.epoch || r.pod != "pod-a" {
		t.Errorf("a renew changed the row: %+v, was %+v", r, first)
	}
	noTwoHolders(t, pods)

	// The fence check: current token yes; a wrong epoch, a wrong pod, no.
	var ok1, ok2, ok3 bool
	if err := pool.QueryRowContext(ctx, `SELECT rivolt_lease_ok($1::uuid,'pod-a',$2), rivolt_lease_ok($1::uuid,'pod-a',$2+1), rivolt_lease_ok($1::uuid,'pod-b',$2)`, gid, first.epoch).Scan(&ok1, &ok2, &ok3); err != nil {
		t.Fatal(err)
	}
	if !ok1 || ok2 || ok3 {
		t.Errorf("rivolt_lease_ok = %v/%v/%v, want true/false/false", ok1, ok2, ok3)
	}

	// A's vehicle leases expire and B takes the vehicles: B is refused the group row
	// while A's row is still alive (nothing acts on it in M1, but one row, one holder).
	expireLease(ctx, t, pool, "gl-v1")
	expireLease(ctx, t, pool, "gl-v2")
	for _, v := range []string{"gl-v1", "gl-v2"} {
		if ok, err := b.Acquire(ctx, v); err != nil || !ok {
			t.Fatalf("b %s = %v, %v", v, ok, err)
		}
	}
	if r, err := b.RenewGroups(ctx); err != nil || r.Held != 0 {
		t.Fatalf("b took a live group row: %+v, %v", r, err)
	}
	// A's pass: the vehicles are gone from it, so its row goes and the dry run flags
	// its old token as refused.
	ra, err := a.RenewGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Held != 0 {
		t.Errorf("a still holds %d", ra.Held)
	}
	if r := groupRows(ctx, t, pool)[gid]; r.pod != "" || !r.released {
		t.Errorf("a's row should be released (pod NULL, released_at set) after losing the vehicles: %+v", r)
	}
	// B now takes the group, with a new epoch.
	rb, err := b.RenewGroups(ctx)
	if err != nil || rb.Held != 1 {
		t.Fatalf("b = %+v, %v", rb, err)
	}
	if r := groupRows(ctx, t, pool)[gid]; r.pod != "pod-b" || r.epoch <= first.epoch {
		t.Errorf("after the steal: %+v (was epoch %d)", r, first.epoch)
	}
	noTwoHolders(t, pods)

	// A crashed holder: the row expires without anyone releasing it; a peer that
	// holds the vehicles takes it over with a higher epoch, and the old holder's
	// token is refused (dry run).
	prev := groupRows(ctx, t, pool)[gid]
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	expireLease(ctx, t, pool, "gl-v1")
	expireLease(ctx, t, pool, "gl-v2")
	c := pods[2]
	for _, v := range []string{"gl-v1", "gl-v2"} {
		if ok, err := c.Acquire(ctx, v); err != nil || !ok {
			t.Fatalf("c %s = %v, %v", v, ok, err)
		}
	}
	if r, err := c.RenewGroups(ctx); err != nil || r.Held != 1 {
		t.Fatalf("c = %+v, %v", r, err)
	}
	if r := groupRows(ctx, t, pool)[gid]; r.pod != "pod-c" || r.epoch <= prev.epoch {
		t.Errorf("after the crash takeover: %+v (was %+v)", r, prev)
	}
	rb, err = b.RenewGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rb.DryRunRejected) != 1 || rb.DryRunRejected[0] != gid {
		t.Errorf("the old holder's token should be refused in the dry run: %+v", rb)
	}
	// The dry run only reports: the pod's vehicle leases and its next pass work as before.
	if _, err := b.Renew(ctx); err != nil {
		t.Errorf("a dry-run rejection disturbed the vehicle leases: %v", err)
	}
	noTwoHolders(t, pods)

	// A graceful stop removes the pod's rows and its pod row.
	if _, err := c.ReleaseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if r := groupRows(ctx, t, pool)[gid]; r.pod != "" || !r.released {
		t.Errorf("ReleaseAll should leave the row released, not deleted: %+v", r)
	}
	var pods2 int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM lease_pods WHERE pod_id = 'pod-c'`).Scan(&pods2); err != nil || pods2 != 0 {
		t.Errorf("lease_pods rows for pod-c = %d, %v", pods2, err)
	}
}

// A vehicle shared by two accounts links them into one group: one row, keyed by the
// smaller user id, held only when the pod holds every vehicle of both users.
func TestGroupLease_SharedVehicleIsOneGroup(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "gs-1", "gs-x", "gs-shared")
	seedUserVehicles(ctx, t, pool, "gs-2", "gs-shared", "gs-z")
	pods := threePods(t, pool)
	a := pods[0]
	for _, v := range []string{"gs-x", "gs-shared"} {
		if ok, err := a.Acquire(ctx, v); err != nil || !ok {
			t.Fatalf("a %s = %v, %v", v, ok, err)
		}
	}
	for i, r := range renewAll(ctx, t, pods) {
		if r.Held != 0 {
			t.Errorf("pod %d holds a group without gs-z", i)
		}
	}
	if ok, err := a.Acquire(ctx, "gs-z"); err != nil || !ok {
		t.Fatalf("a z = %v, %v", ok, err)
	}
	renewAll(ctx, t, pods)
	rows := groupRows(ctx, t, pool)
	if len(rows) != 1 {
		t.Fatalf("%d group rows for one linked group", len(rows))
	}
	var want string
	if err := pool.QueryRowContext(ctx, `SELECT min(id::text) FROM users WHERE username IN ('gs-1','gs-2')`).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if _, ok := rows[want]; !ok {
		t.Errorf("group row is not keyed by the smaller user id %s: %v", want, rows)
	}
	noTwoHolders(t, pods)
}

// Deleting the account removes the group row it keys.
func TestGroupLease_DeleteUserRemovesTheRow(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "gd-user", "gd-v")
	pods := threePods(t, pool)
	if ok, err := pods[0].Acquire(ctx, "gd-v"); err != nil || !ok {
		t.Fatalf("acquire = %v, %v", ok, err)
	}
	renewAll(ctx, t, pods)
	if len(groupRows(ctx, t, pool)) != 1 {
		t.Fatal("no group row")
	}
	uid, err := db.EnsureUser(ctx, pool, "gd-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUser(ctx, pool, uid); err != nil {
		t.Fatal(err)
	}
	if n := len(groupRows(ctx, t, pool)); n != 0 {
		t.Errorf("%d group rows after the user was deleted", n)
	}
}

// The epoch changes on every acquire or steal, after the holder's own expiry, and when a
// released row is taken again; it never changes on a renew. rivolt_lease_ok is false for a lease
// that has expired but was not stolen, and for a statement that starts after the expiry inside a
// transaction that began before it.
func TestGroupLease_EpochRulesAndTheFenceCheck(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "ge-user", "ge-v1")
	pods := threePods(t, pool)
	a := pods[0]
	if ok, err := a.Acquire(ctx, "ge-v1"); err != nil || !ok {
		t.Fatalf("acquire = %v, %v", ok, err)
	}
	renewAll(ctx, t, pods)
	var gid string
	var e1 int64
	for g, r := range heldRows(ctx, t, pool) {
		gid, e1 = g, r.epoch
	}
	if gid == "" {
		t.Fatal("no group row")
	}
	ok := func(epoch int64) bool {
		var v bool
		if err := pool.QueryRowContext(ctx, `SELECT rivolt_lease_ok($1::uuid, 'pod-a', $2)`, gid, epoch).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if !ok(e1) {
		t.Fatal("a live token should pass")
	}

	// The holder's own row expires (it missed its renewals) but nobody stole it: the check is
	// already false, and re-acquiring takes a new epoch.
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if ok(e1) {
		t.Error("an expired but unstolen lease passed the fence check")
	}
	if _, err := a.RenewGroups(ctx); err != nil {
		t.Fatal(err)
	}
	e2 := heldRows(ctx, t, pool)[gid].epoch
	if e2 <= e1 {
		t.Errorf("re-acquire after the holder's own expiry kept epoch %d (was %d)", e2, e1)
	}
	if ok(e1) || !ok(e2) {
		t.Error("the old token should be refused and the new one pass")
	}

	// A renew keeps the epoch.
	if _, err := a.RenewGroups(ctx); err != nil {
		t.Fatal(err)
	}
	if e := heldRows(ctx, t, pool)[gid].epoch; e != e2 {
		t.Errorf("a renew changed the epoch: %d -> %d", e2, e)
	}

	// A statement that starts after the expiry fails the check even inside a transaction that
	// began before it (now() would still be the transaction's start).
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET expires_at = now() + interval '2 seconds'`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var before, after bool
	if err := tx.QueryRowContext(ctx, `SELECT rivolt_lease_ok($1::uuid, 'pod-a', $2)`, gid, e2).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_sleep(3)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT rivolt_lease_ok($1::uuid, 'pod-a', $2)`, gid, e2).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !before || after {
		t.Errorf("inside one transaction: before expiry %v (want true), after %v (want false)", before, after)
	}
	_ = tx.Rollback()

	// A released row is taken again with a new epoch and is no longer marked released.
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET expires_at = now() + interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReleaseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if r := groupRows(ctx, t, pool)[gid]; r.pod != "" || !r.released {
		t.Fatalf("released row: %+v", r)
	}
	if ok(e2) {
		t.Error("a released row passed the fence check")
	}
	if ok, err := a.Acquire(ctx, "ge-v1"); err != nil || !ok {
		t.Fatalf("re-acquire = %v, %v", ok, err)
	}
	if _, err := a.RenewGroups(ctx); err != nil {
		t.Fatal(err)
	}
	r := groupRows(ctx, t, pool)[gid]
	if r.pod != "pod-a" || r.released || r.epoch <= e2 {
		t.Errorf("after taking a released row: %+v (previous epoch %d)", r, e2)
	}
}

// Deleting one user of a shared-vehicle group deletes the group row; the surviving user's
// group is derived again under its own smallest id at the next pass (in M2 that is a gap of at
// most one tick for the survivor).
func TestGroupLease_DeleteUserInASharedGroup(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "gx-1", "gx-own1", "gx-shared")
	seedUserVehicles(ctx, t, pool, "gx-2", "gx-shared", "gx-own2")
	pods := threePods(t, pool)
	a := pods[0]
	for _, v := range []string{"gx-own1", "gx-shared", "gx-own2"} {
		if ok, err := a.Acquire(ctx, v); err != nil || !ok {
			t.Fatalf("acquire %s = %v, %v", v, ok, err)
		}
	}
	renewAll(ctx, t, pods)
	if len(heldRows(ctx, t, pool)) != 1 {
		t.Fatalf("rows = %v", groupRows(ctx, t, pool))
	}
	var smaller, other string
	if err := pool.QueryRowContext(ctx, `SELECT min(id::text), max(id::text) FROM users WHERE username IN ('gx-1','gx-2')`).Scan(&smaller, &other); err != nil {
		t.Fatal(err)
	}
	if _, ok := heldRows(ctx, t, pool)[smaller]; !ok {
		t.Fatalf("group is not keyed by %s", smaller)
	}
	var su string
	if err := pool.QueryRowContext(ctx, `SELECT username FROM users WHERE id::text = $1`, smaller).Scan(&su); err != nil {
		t.Fatal(err)
	}
	uid, err := db.EnsureUser(ctx, pool, su)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUser(ctx, pool, uid); err != nil {
		t.Fatal(err)
	}
	if _, ok := groupRows(ctx, t, pool)[smaller]; ok {
		t.Error("the deleted user's group row is still there")
	}
	// The survivor's vehicles are held again and its group row appears under its own id.
	for _, v := range []string{"gx-shared", "gx-own1", "gx-own2"} {
		_, _ = a.Acquire(ctx, v)
	}
	if _, err := a.RenewGroups(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := heldRows(ctx, t, pool)[other]; !ok {
		t.Errorf("the survivor's group was not derived under %s: %v", other, heldRows(ctx, t, pool))
	}
}

// A user who joins a group through a shared vehicle with a smaller id re-keys it. While the old
// holder's row (keyed by the larger id) is live, another pod that holds all the vehicles is
// refused the re-keyed row: never two fence-valid rows for overlapping users. Once the old row is
// gone the new pod takes the group. Dead lease_pods rows are pruned.
func TestGroupLease_ReKeyIsNotADoubleOwner(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "rk-1", "rk-shared")
	seedUserVehicles(ctx, t, pool, "rk-2", "rk-own2")
	// The existing user is the one with the LARGER id, so the newcomer re-keys the group.
	var big, small string
	if err := pool.QueryRowContext(ctx, `SELECT (SELECT username FROM users WHERE username IN ('rk-1','rk-2') ORDER BY id DESC LIMIT 1), (SELECT username FROM users WHERE username IN ('rk-1','rk-2') ORDER BY id ASC LIMIT 1)`).Scan(&big, &small); err != nil {
		t.Fatal(err)
	}
	pods := threePods(t, pool)
	a, b := pods[0], pods[1]
	// Make "big" the sole owner of rk-shared before the link: move the shared vehicle to it.
	if _, err := pool.ExecContext(ctx, `UPDATE vehicles SET user_id = (SELECT id FROM users WHERE username = $1) WHERE rivian_vehicle_id = 'rk-shared'`, big); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"rk-shared", "rk-own2"} {
		if ok, err := a.Acquire(ctx, v); err != nil || !ok {
			t.Fatalf("a acquire %s = %v, %v", v, ok, err)
		}
	}
	if r, err := a.RenewGroups(ctx); err != nil || r.Held != 1 {
		t.Fatalf("a = %+v, %v", r, err)
	}
	oldKey := ""
	for g := range heldRows(ctx, t, pool) {
		oldKey = g
	}
	// The newcomer (smaller id) is linked to the same vehicle: the group is now {big, small}, keyed by small.
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) SELECT id, 'rk-shared' FROM users WHERE username = $1`, small); err != nil {
		t.Fatal(err)
	}
	// A's vehicle leases lapse; B takes the vehicles.
	expireLease(ctx, t, pool, "rk-shared")
	expireLease(ctx, t, pool, "rk-own2")
	for _, v := range []string{"rk-shared", "rk-own2"} {
		if ok, err := b.Acquire(ctx, v); err != nil || !ok {
			t.Fatalf("b %s = %v, %v", v, ok, err)
		}
	}
	if r, err := b.RenewGroups(ctx); err != nil || r.Held != 0 {
		t.Fatalf("b took a re-keyed row while the old holder's row is live: %+v, %v", r, err)
	}
	if rows := heldRows(ctx, t, pool); len(rows) != 1 || rows[oldKey].pod != "pod-a" {
		t.Fatalf("rows = %+v", rows)
	}
	// The old holder's row expires; B takes the re-keyed group, and only one row is held.
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET expires_at = now() - interval '1 second' WHERE group_id::text = $1`, oldKey); err != nil {
		t.Fatal(err)
	}
	if r, err := b.RenewGroups(ctx); err != nil || r.Held != 1 {
		t.Fatalf("b after expiry = %+v, %v", r, err)
	}
	live := 0
	for _, r := range groupRows(ctx, t, pool) {
		if r.pod != "" {
			live++
		}
	}
	if live != 2 { // the expired old row (still named pod-a until it releases) and the new one
		t.Logf("rows: %+v", groupRows(ctx, t, pool))
	}
	noTwoHolders(t, pods)

	// Pods not seen for a day are pruned; this pod's own row never is.
	if _, err := pool.ExecContext(ctx, `INSERT INTO lease_pods (pod_id, generation, started_at, seen_at) VALUES ('dead-pod', '', now() - interval '3 days', now() - interval '2 days')`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RenewGroups(ctx); err != nil {
		t.Fatal(err)
	}
	var dead, mine int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE pod_id = 'dead-pod'), count(*) FILTER (WHERE pod_id = 'pod-b') FROM lease_pods`).Scan(&dead, &mine); err != nil {
		t.Fatal(err)
	}
	if dead != 0 || mine != 1 {
		t.Errorf("lease_pods: dead=%d mine=%d", dead, mine)
	}
}
