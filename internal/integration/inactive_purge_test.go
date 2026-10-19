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

// The inactive-account purge in Postgres: who is warned, who is deleted, that
// a vehicle connected in between always wins, and that signing in does not
// cancel it.
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
		if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - make_interval(secs => $2), email_verified = TRUE WHERE id = $1`, id, age.Seconds()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	day := 24 * time.Hour
	// An address the identity provider never verified is out of the job's
	// scope entirely: never mailed a warning, so never deleted either.
	unverified, err := db.EnsureUserFull(ctx, pool, "unverified", "unv@example.com", "unverified")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - interval '200 days', email_verified = FALSE,
	        inactive_warned_at = now() - interval '60 days' WHERE id = $1`, unverified); err != nil {
		t.Fatal(err)
	}
	due := user("due", "due@example.com", 100*day)                // warn-eligible
	young := user("young", "young@example.com", 40*day)           // too young
	noMail := user("nomail", "", 100*day)                         // nothing to warn
	withCar := user("withcar", "car@example.com", 200*day)        // connected a vehicle
	sessionOnly := user("sessiononly", "so@example.com", 200*day) // connected Rivian, no vehicle row (a reservation holder, a failed prime)
	admin := user("adm", "adm@example.com", 200*day)
	disabled := user("dis", "dis@example.com", 200*day)
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-1')`, withCar); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id) VALUES ($1, 'rivian.session', '\x00', 'test')`, sessionOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET disabled = TRUE WHERE id = $1`, disabled); err != nil {
		t.Fatal(err)
	}
	_ = young
	_ = noMail
	if ok, _ := db.ClaimInactiveWarning(ctx, pool, unverified); ok {
		t.Fatal("claimed a warning for an unverified address")
	}
	if ids, _ := db.ListInactiveDeleteDue(ctx, pool, 10); len(ids) != 0 {
		t.Fatalf("an unverified account (warned long ago) is delete-due: %v", ids)
	}
	if _, ok, _ := db.DeleteInactiveAccount(ctx, pool, unverified); ok {
		t.Fatal("deleted an unverified account")
	}

	warn, err := db.ListInactiveWarnDue(ctx, pool, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(warn) != 1 || warn[0].UserID != due || warn[0].Email != "due@example.com" {
		t.Fatalf("warn due = %+v, want only the 100-day account with an email", warn)
	}
	// The stored-session user is never warn-due however old, and a stale warning on one is cleared.
	if _, err := pool.ExecContext(ctx, `UPDATE users SET inactive_warned_at = now() - interval '40 days' WHERE id = $1`, sessionOnly); err != nil {
		t.Fatal(err)
	}
	if ids, _ := db.ListInactiveDeleteDue(ctx, pool, 10); len(ids) != 0 {
		t.Fatalf("a user with a stored Rivian session is delete-due: %v", ids)
	}
	if _, ok, _ := db.DeleteInactiveAccount(ctx, pool, sessionOnly); ok {
		t.Fatal("deleted a user with a stored Rivian session")
	}
	if n, err := db.CancelInactiveWarnings(ctx, pool); err != nil || n != 1 {
		t.Fatalf("cancelled %d (err %v), want the stale warning of the session-only user", n, err)
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

	// Signing in does NOT cancel: a real session create leaves the warning and
	// the eligibility alone (only connecting a vehicle cancels).
	store, err := sessions.New(pool, bytes.Repeat([]byte("p"), 32))
	if err != nil {
		t.Fatal(err)
	}
	age(due, 31*day, 130*day)
	if _, _, err := store.Create(ctx, due, time.Hour, sessions.CreateOpts{}); err != nil {
		t.Fatal(err)
	}
	var warned sql.NullTime
	if err := pool.QueryRowContext(ctx, `SELECT inactive_warned_at FROM users WHERE id = $1`, due).Scan(&warned); err != nil || !warned.Valid {
		t.Fatalf("a sign-in must not clear the warning: %+v, err %v", warned, err)
	}
	if ids, err := db.ListInactiveDeleteDue(ctx, pool, 10); err != nil || len(ids) != 1 || ids[0] != due {
		t.Fatalf("after a sign-in delete due = %v, err %v; want the account still due", ids, err)
	}

	// Connecting a vehicle after the warning wins at the last moment, even
	// when the list was built before it: the delete re-checks under a lock.
	age(due, 31*day, 130*day)
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

	// The real thing: warned 31 days ago, 130 days old, no vehicle. Deleted
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
	// Everyone else is untouched: the admin, the disabled, the connected.
	for name, id := range map[string]uuid.UUID{"admin": admin, "disabled": disabled, "with a vehicle": withCar, "stored Rivian session and no vehicle": sessionOnly, "young": young, "no email": noMail} {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id = $1`, id).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s was deleted", name)
		}
	}
	// And none of them is a candidate even once warned long ago.
	if _, err := pool.ExecContext(ctx, `UPDATE users SET inactive_warned_at = now() - interval '200 days', created_at = now() - interval '400 days' WHERE id IN ($1,$2,$3,$4)`, admin, disabled, withCar, sessionOnly); err != nil {
		t.Fatal(err)
	}
	if ids, _ := db.ListInactiveDeleteDue(ctx, pool, 10); len(ids) != 0 {
		t.Errorf("a protected account is delete-due: %v", ids)
	}
}

// A never-connected account with no verified address cannot be warned, so the
// job deletes it at day 120 with no warning (privacy.md, "Retention"); an account
// that connected Rivian, an admin, a disabled account and a verified one are never
// touched by this arm, and a row that verified its address since the list was built
// wins under the lock.
func TestUnwarnableAccountPurge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if _, err := db.EnsureUser(ctx, pool, "bootstrap-admin-2"); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	// The policy is already in force for this test (see TestUnwarnableGrandfathered).
	old := db.UnwarnablePolicyStart
	db.UnwarnablePolicyStart = time.Unix(0, 0)
	t.Cleanup(func() { db.UnwarnablePolicyStart = old })
	mk := func(name, mail string, verified bool, age time.Duration) uuid.UUID {
		t.Helper()
		id, err := db.EnsureUserFull(ctx, pool, name, mail, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - make_interval(secs => $2), email_verified = $3 WHERE id = $1`, id, age.Seconds(), verified); err != nil {
			t.Fatal(err)
		}
		return id
	}
	unverifiedOld := mk("unv-old", "unv-old@example.com", false, 200*day)
	noEmailOld := mk("noemail-old", "", true, 200*day)
	unverifiedYoung := mk("unv-young", "unv-young@example.com", false, 100*day)
	verifiedOld := mk("ver-old", "ver-old@example.com", true, 200*day) // the warned path, not this arm
	withCar := mk("unv-car", "unv-car@example.com", false, 200*day)
	withSession := mk("unv-sess", "unv-sess@example.com", false, 200*day)
	admin := mk("unv-adm", "unv-adm@example.com", false, 200*day)
	disabled := mk("unv-dis", "unv-dis@example.com", false, 200*day)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-unv-1')`, withCar)
	exec(`INSERT INTO user_secrets (user_id, name, ciphertext, kek_id) VALUES ($1, 'rivian.session', '\x00', 'test')`, withSession)
	exec(`UPDATE users SET role = 'admin' WHERE id = $1`, admin)
	exec(`UPDATE users SET disabled = TRUE WHERE id = $1`, disabled)

	ids, err := db.ListUnwarnableDeleteDue(ctx, pool, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if len(ids) != 2 || !got[unverifiedOld] || !got[noEmailOld] {
		t.Fatalf("delete due = %v, want exactly the unverified and the email-less account over 120 days", ids)
	}
	if n, err := db.CountUnwarnableDue(ctx, pool); err != nil || n != 2 {
		t.Errorf("dry-run count = %d (err %v), want 2", n, err)
	}
	// The admin page schedules it: signup + 120 days, no warning.
	rows, err := db.ListUsersForAdmin(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == unverifiedYoung {
			if r.PurgeAt == nil || r.PurgeAt.Before(time.Now().Add(15*day)) || r.PurgeAt.After(time.Now().Add(25*day)) {
				t.Errorf("young unverified account purge_at = %v, want about 20 days out (signup + 120 d)", r.PurgeAt)
			}
		}
		if r.ID == admin || r.ID == disabled || r.ID == withCar || r.ID == withSession {
			if r.PurgeAt != nil {
				t.Errorf("protected account %s has a purge date %v", r.Username, r.PurgeAt)
			}
		}
	}
	// Verifying the address since the list was built wins under the lock.
	exec(`UPDATE users SET email_verified = TRUE WHERE id = $1`, unverifiedOld)
	if _, ok, _ := db.DeleteUnwarnableAccount(ctx, pool, unverifiedOld); ok {
		t.Fatal("deleted an account that verified its address since the list was built")
	}
	exec(`UPDATE users SET email_verified = FALSE WHERE id = $1`, unverifiedOld)
	// The real thing, with the cascade.
	name, ok, err := db.DeleteUnwarnableAccount(ctx, pool, unverifiedOld)
	if err != nil || !ok || name != "unv-old" {
		t.Fatalf("delete = %q %v, err %v", name, ok, err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id = $1`, unverifiedOld).Scan(&n); err != nil || n != 0 {
		t.Errorf("user row still there: %d, err %v", n, err)
	}
	// Everyone else is untouched, even when asked directly.
	for name, id := range map[string]uuid.UUID{"young": unverifiedYoung, "verified": verifiedOld, "vehicle": withCar, "stored session": withSession, "admin": admin, "disabled": disabled} {
		if _, ok, _ := db.DeleteUnwarnableAccount(ctx, pool, id); ok {
			t.Errorf("%s was deleted by the unwarnable arm", name)
		}
	}
}

// Accounts that signed up under the earlier privacy text were promised a warning,
// so the no-warning deletion counts its 120 days from the later of signup and the
// day the new text took effect.
func TestUnwarnableGrandfathered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	// The first user is made an admin; keep it out of the way.
	if _, err := db.EnsureUser(ctx, pool, "bootstrap-admin-3"); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	oldStart := db.UnwarnablePolicyStart
	t.Cleanup(func() { db.UnwarnablePolicyStart = oldStart })
	id, err := db.EnsureUserFull(ctx, pool, "gf", "gf@example.com", "gf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - interval '200 days', email_verified = FALSE WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	purgeAt := func() time.Time {
		rows, err := db.ListUsersForAdmin(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == id {
				if r.PurgeAt == nil {
					t.Fatal("no purge_at")
				}
				return *r.PurgeAt
			}
		}
		t.Fatal("user not listed")
		return time.Time{}
	}
	// Policy took effect 10 days ago: 200 days old, but the clock starts 10 days ago.
	db.UnwarnablePolicyStart = time.Now().Add(-10 * day)
	if ids, err := db.ListUnwarnableDeleteDue(ctx, pool, 10); err != nil || len(ids) != 0 {
		t.Fatalf("due = %v (err %v), want none: 10 days into the grace", ids, err)
	}
	if n, _ := db.CountUnwarnableDue(ctx, pool); n != 0 {
		t.Errorf("dry-run count = %d, want 0", n)
	}
	if _, ok, _ := db.DeleteUnwarnableAccount(ctx, pool, id); ok {
		t.Fatal("deleted before policy start + 120 days")
	}
	if d := time.Until(purgeAt()) - 110*day; d > time.Minute || d < -time.Minute {
		t.Errorf("purge_at = %v from now, want about 110 days", time.Until(purgeAt()))
	}
	// Policy took effect 121 days ago: due.
	db.UnwarnablePolicyStart = time.Now().Add(-121 * day)
	if ids, err := db.ListUnwarnableDeleteDue(ctx, pool, 10); err != nil || len(ids) != 1 {
		t.Fatalf("due = %v (err %v), want the account", ids, err)
	}
}
