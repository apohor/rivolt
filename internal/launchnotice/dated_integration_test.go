//go:build integration

package launchnotice

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// The dated notices against real rows: the founding-year-end sweep gives every account in the window one notice (in-app for all, email for
// a verified address), extends a year found under 30 days from its end to 30 days from now in the same transaction that claims its email,
// and is idempotent; the renewal reminder is one per renewal period.
func TestDatedNoticesAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	user := func(name, mail string, verified, disabled bool) uuid.UUID {
		id, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE users SET email = $2, email_verified = $3, disabled = $4 WHERE id = $1`, id, mail, verified, disabled); err != nil {
			t.Fatal(err)
		}
		return id
	}
	founding := func(id uuid.UUID, end time.Time) {
		if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'founding', 'active', $2)`, id, end); err != nil {
			t.Fatal(err)
		}
	}
	day := func(n int) time.Time { return now.AddDate(0, 0, n) }
	normal := user("ded-normal", "normal@example.test", true, false)    // 40 days out: the normal path
	late := user("ded-late", "late@example.test", true, false)          // 20 days out: extended
	unverified := user("ded-unver", "unver@example.test", false, false) // in-app only
	far := user("ded-far", "far@example.test", true, false)             // 60 days out: not yet
	disabled := user("ded-dis", "dis@example.test", true, true)         // never
	paid := user("ded-paid", "paid@example.test", true, false)          // subscribed past the founding year
	expired := user("ded-exp", "exp@example.test", true, false)         // the year is over
	founding(normal, day(40))
	founding(late, day(20))
	founding(unverified, day(40))
	founding(far, day(60))
	founding(disabled, day(40))
	founding(paid, day(40))
	founding(expired, day(-1))
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'stripe', 'active', $2)`, paid, day(300)); err != nil {
		t.Fatal(err)
	}

	m := &recMail{}
	var extended []uuid.UUID
	job := &DatedJob{Store: PGDated{DB: pool}, Mailer: m, Now: func() time.Time { return now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnExtend: func(uid uuid.UUID) { extended = append(extended, uid) }}
	st, err := job.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Other tests share the database: look only at the accounts made here.
	mine := map[uuid.UUID]string{normal: "normal", late: "late", unverified: "unver", far: "far", disabled: "dis", paid: "paid", expired: "exp"}
	sentTo := map[string]string{}
	for _, msg := range m.msgs {
		sentTo[msg.To] = msg.Text
	}
	for _, want := range []string{"normal@example.test", "late@example.test", "paid@example.test"} {
		if _, ok := sentTo[want]; !ok {
			t.Errorf("no email to %s (sent to %d addresses; stats %+v)", want, len(sentTo), st)
		}
	}
	for _, not := range []string{"unver@example.test", "far@example.test", "dis@example.test", "exp@example.test"} {
		if _, ok := sentTo[not]; ok {
			t.Errorf("an email went to %s", not)
		}
	}
	if !strings.Contains(sentTo["paid@example.test"], "Your Rivolt Pro subscription continues; nothing changes for you on") {
		t.Errorf("the subscriber's email lacks its line:\n%s", sentTo["paid@example.test"])
	}
	if strings.Contains(sentTo["normal@example.test"], "subscription continues") || strings.Contains(sentTo["normal@example.test"], "We moved this date") {
		t.Errorf("the normal email carries a variant line:\n%s", sentTo["normal@example.test"])
	}

	// The late year was extended to at least 30 days from now, in the plan itself, and says so; the others are untouched.
	var lateEnd, normalEnd time.Time
	if err := pool.QueryRowContext(ctx, `SELECT current_period_end FROM entitlements WHERE user_id = $1 AND source = 'founding'`, late).Scan(&lateEnd); err != nil {
		t.Fatal(err)
	}
	if lateEnd.Sub(now) < MinNotice {
		t.Errorf("the late year ends in %v, want at least 30 days", lateEnd.Sub(now))
	}
	if err := pool.QueryRowContext(ctx, `SELECT current_period_end FROM entitlements WHERE user_id = $1 AND source = 'founding'`, normal).Scan(&normalEnd); err != nil || !normalEnd.Equal(day(40)) {
		t.Errorf("the normal year moved: %v (%v)", normalEnd, err)
	}
	if !strings.Contains(sentTo["late@example.test"], "We moved this date from") || !strings.Contains(sentTo["late@example.test"], long(lateEnd.Format("2006-01-02"))) {
		t.Errorf("the late email does not state the extended date:\n%s", sentTo["late@example.test"])
	}
	foundLate := false
	for _, u := range extended {
		foundLate = foundLate || u == late
		if u != late {
			t.Errorf("a year that was not late was reported extended: %s", mine[u])
		}
	}
	if !foundLate || st.Late < 1 {
		t.Errorf("the late year was not counted: %+v %v", st, extended)
	}

	// In-app notices exist for the unverified account too, none for the far, disabled and expired ones; email_sent_at only where one went.
	has := func(id uuid.UUID) (exists, sent bool) {
		var s *time.Time
		err := pool.QueryRowContext(ctx, `SELECT email_sent_at FROM account_notices WHERE user_id = $1 AND kind = $2`, id, KindFoundingEnd).Scan(&s)
		return err == nil, s != nil
	}
	for id, want := range map[uuid.UUID][2]bool{normal: {true, true}, late: {true, true}, paid: {true, true}, unverified: {true, false}, far: {false, false}, disabled: {false, false}, expired: {false, false}} {
		if e, s := has(id); e != want[0] || s != want[1] {
			t.Errorf("%s: notice exists=%v email sent=%v, want %v", mine[id], e, s, want)
		}
	}

	// Idempotent: a second run sends nothing more, extends nothing more.
	before := len(m.msgs)
	extended = nil
	if _, err := job.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.msgs) != before || len(extended) != 0 {
		t.Errorf("a second run sent %d more emails and extended %d years", len(m.msgs)-before, len(extended))
	}
	var lateEnd2 time.Time
	if err := pool.QueryRowContext(ctx, `SELECT current_period_end FROM entitlements WHERE user_id = $1 AND source = 'founding'`, late).Scan(&lateEnd2); err != nil || !lateEnd2.Equal(lateEnd) {
		t.Errorf("the late year moved again: %v -> %v", lateEnd, lateEnd2)
	}

	// A failed send releases the claim, so the next run retries it.
	retry := user("ded-retry", "retry@example.test", true, false)
	founding(retry, day(40))
	fm := &recMail{fail: true}
	fjob := &DatedJob{Store: PGDated{DB: pool}, Mailer: fm, Now: func() time.Time { return now }, Logger: job.Logger}
	if _, err := fjob.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, sent := has(retry); sent {
		t.Error("a failed send was recorded as sent")
	}
	if _, err := job.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, sent := has(retry); !sent {
		t.Error("the failed send was not retried")
	}

	// The renewal reminder: one per renewal period, even if the sweep runs again.
	renews := day(40)
	src := fakeRenewals{rs: []Renewal{{UserID: normal, Email: "normal@example.test", Verified: true, RenewsOn: renews, PriceLabel: "$34.99 a year", CancelURL: "https://billing.example.test/portal"}}}
	rm := &recMail{}
	rj := &RenewalJob{Source: src, Store: Store{DB: pool}, Mailer: rm, Now: func() time.Time { return now }}
	for i := 0; i < 2; i++ {
		if _, err := rj.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(rm.msgs) != 1 || !strings.Contains(rm.msgs[0].Text, "https://billing.example.test/portal") {
		t.Errorf("renewal reminders sent: %d", len(rm.msgs))
	}
}
