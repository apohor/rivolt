//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
)

// The inactive-account purge in Postgres: who is warned, who is deleted, and
// that a vehicle or a sign-in landing in between always wins.
func TestInactiveAccountPurge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	// The first user in an empty table is promoted to admin by the bootstrap trigger.
	if _, err := db.EnsureUser(ctx, pool, "bootstrap-admin"); err != nil {
		t.Fatal(err)
	}
	user := func(name, mail string, age time.Duration) uuid.UUID {
		t.Helper()
		id, err := db.EnsureUserFull(ctx, pool, name, mail, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - make_interval(secs => $2) WHERE id = $1`, id, age.Seconds()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	day := 24 * time.Hour
	due := user("due", "due@example.com", 100*day)         // warn-eligible
	young := user("young", "young@example.com", 40*day)    // too young
	noMail := user("nomail", "", 100*day)                  // nothing to warn
	withCar := user("withcar", "car@example.com", 200*day) // connected a vehicle
	admin := user("adm", "adm@example.com", 200*day)
	disabled := user("dis", "dis@example.com", 200*day)
	recent := user("recent", "recent@example.com", 200*day) // signed in lately
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-1')`, withCar); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET disabled = TRUE WHERE id = $1`, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET last_login_at = now() - interval '5 days' WHERE id = $1`, recent); err != nil {
		t.Fatal(err)
	}
	_ = young
	_ = noMail

	warn, err := db.ListInactiveWarnDue(ctx, pool, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(warn) != 1 || warn[0].UserID != due || warn[0].Email != "due@example.com" {
		t.Fatalf("warn due = %+v, want only the 100-day account with an email", warn)
	}
	if w, d, wait, err := db.CountInactiveCandidates(ctx, pool); err != nil || w != 1 || d != 0 || wait != 0 {
		t.Errorf("counts = %d/%d/%d, err %v; want 1/0/0", w, d, wait, err)
	}

	// The warning: a lease first, recorded only after the send.
	if ok, err := db.ClaimInactiveWarning(ctx, pool, due); err != nil || !ok {
		t.Fatalf("claim = %v, err %v", ok, err)
	}
	if ok, _ := db.ClaimInactiveWarning(ctx, pool, due); ok {
		t.Error("a second claim won inside the lease")
	}
	if left, _ := db.ListInactiveDeleteDue(ctx, pool, 10); len(left) != 0 {
		t.Errorf("deletion due before any warning: %v", left)
	}
	if _, ok, _ := db.DeleteInactiveAccount(ctx, pool, due); ok {
		t.Fatal("deleted an account that was never warned")
	}
	if err := db.MarkInactiveWarned(ctx, pool, due); err != nil {
		t.Fatal(err)
	}
	if w, _ := db.ListInactiveWarnDue(ctx, pool, 50); len(w) != 0 {
		t.Errorf("warned account listed again: %+v", w)
	}

	// 29 days after the warning and 129 days old: still inside the grace.
	age := func(id uuid.UUID, warnedAgo, createdAgo time.Duration) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `UPDATE users SET inactive_warned_at = now() - make_interval(secs => $2), created_at = now() - make_interval(secs => $3) WHERE id = $1`,
			id, warnedAgo.Seconds(), createdAgo.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	age(due, 29*day, 129*day)
	if _, ok, _ := db.DeleteInactiveAccount(ctx, pool, due); ok {
		t.Fatal("deleted inside the 30-day grace")
	}
	// Past the grace but the clock is only 110 days: not yet.
	age(due, 31*day, 110*day)
	if _, ok, _ := db.DeleteInactiveAccount(ctx, pool, due); ok {
		t.Fatal("deleted before day 120")
	}

	// Signing in cancels: a real session create resets the clock and the warning.
	store, err := sessions.New(pool, bytes.Repeat([]byte("p"), 32))
	if err != nil {
		t.Fatal(err)
	}
	age(due, 31*day, 130*day)
	if _, _, err := store.Create(ctx, due, time.Hour, sessions.CreateOpts{}); err != nil {
		t.Fatal(err)
	}
	var warned sql.NullTime
	if err := pool.QueryRowContext(ctx, `SELECT inactive_warned_at FROM users WHERE id = $1`, due).Scan(&warned); err != nil || warned.Valid {
		t.Fatalf("a sign-in must clear the warning: %+v, err %v", warned, err)
	}
	if _, ok, _ := db.DeleteInactiveAccount(ctx, pool, due); ok {
		t.Fatal("deleted an account that just signed in")
	}
	if w, _ := db.ListInactiveWarnDue(ctx, pool, 50); len(w) != 0 {
		t.Errorf("an account that just signed in is warn-due again: %+v", w)
	}

	// Connecting a vehicle after the warning wins at the last moment, even
	// when the list was built before it: the delete re-checks under a lock.
	age(due, 31*day, 130*day)
	if _, err := pool.ExecContext(ctx, `UPDATE users SET last_login_at = NULL WHERE id = $1`, due); err != nil {
		t.Fatal(err)
	}
	ids, err := db.ListInactiveDeleteDue(ctx, pool, 10)
	if err != nil || len(ids) != 1 || ids[0] != due {
		t.Fatalf("delete due = %v, err %v", ids, err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-late')`, due); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.DeleteInactiveAccount(ctx, pool, due); ok {
		t.Fatal("deleted an account that connected a vehicle after the list was built")
	}
	if n, err := db.CancelInactiveWarnings(ctx, pool); err != nil || n != 1 {
		t.Errorf("cancelled %d (err %v), want the stale warning of the connected account", n, err)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM vehicles WHERE user_id = $1`, due); err != nil {
		t.Fatal(err)
	}

	// The real thing: warned 31 days ago, 130 days old, no vehicle, never signed in. Deleted
	// through the account-delete cascade (its data and signup request go too).
	age(due, 31*day, 130*day)
	if _, err := pool.ExecContext(ctx, `UPDATE users SET inactive_warned_at = now() - interval '31 days' WHERE id = $1`, due); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO signup_requests (email, status) VALUES ('due@example.com','approved')`); err != nil {
		t.Fatal(err)
	}
	name, ok, err := db.DeleteInactiveAccount(ctx, pool, due)
	if err != nil || !ok || name != "due" {
		t.Fatalf("delete = %q %v, err %v", name, ok, err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id = $1`, due).Scan(&n); err != nil || n != 0 {
		t.Errorf("user row still there: %d, err %v", n, err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, due).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions survived the cascade: %d, err %v", n, err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM signup_requests WHERE lower(email) = 'due@example.com'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the signup request outlived the account: %d, err %v", n, err)
	}
	// Everyone else is untouched: the admin, the disabled, the connected, the recent.
	for name, id := range map[string]uuid.UUID{"admin": admin, "disabled": disabled, "with a vehicle": withCar, "recent sign-in": recent, "young": young, "no email": noMail} {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id = $1`, id).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s was deleted", name)
		}
	}
	// And none of them is a candidate even once warned long ago.
	if _, err := pool.ExecContext(ctx, `UPDATE users SET inactive_warned_at = now() - interval '200 days', created_at = now() - interval '400 days' WHERE id IN ($1,$2,$3,$4)`, admin, disabled, withCar, recent); err != nil {
		t.Fatal(err)
	}
	if ids, _ := db.ListInactiveDeleteDue(ctx, pool, 10); len(ids) != 0 {
		t.Errorf("a protected account is delete-due: %v", ids)
	}
}
