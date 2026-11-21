//go:build integration

package entitlements

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// `rivolt grant-pro` against Postgres: one account by email, refusals that write nothing, one entitlement row and one audit row in a
// transaction, a dry run that writes nothing, the plan reading Pro until the end date and free after it with the row kept, a paid plan never
// shadowed, a revoke that only touches the grant, and an output that never carries the address.
func TestGrantProAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	demoID := db.UserIDFor(db.DemoUsername)

	mk := func(name, email string) uuid.UUID {
		t.Helper()
		id, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE users SET email = $2, email_verified = TRUE WHERE id = $1`, id, email); err != nil {
			t.Fatal(err)
		}
		return id
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	creator := "Creator." + suffix + "@grant.example.test"
	cid := mk("grant-creator-"+suffix, creator)
	paid := mk("grant-paid-"+suffix, "paid."+suffix+"@grant.example.test")
	disabled := mk("grant-off-"+suffix, "off."+suffix+"@grant.example.test")
	dupA, dupB := "dup."+suffix+"@grant.example.test", "DUP."+suffix+"@grant.example.test"
	mk("grant-dup-a-"+suffix, dupA)
	mk("grant-dup-b-"+suffix, dupB)
	if _, err := pool.ExecContext(ctx, `UPDATE users SET disabled = TRUE WHERE id = $1`, disabled); err != nil {
		t.Fatal(err)
	}
	// An account with history older than the free window: it must survive a grant ending.
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, pack_kwh) VALUES ($1,$2,135) RETURNING id`, cid, "rv-grant-"+suffix).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source) VALUES ($1,$2,$3,$4,$5,5,'live')`,
		cid, vid, "grant-old-"+suffix, now.AddDate(-2, 0, 0), now.AddDate(-2, 0, 0).Add(20*time.Minute)); err != nil {
		t.Fatalf("seed an old drive: %v", err)
	}

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	audits := func(uid uuid.UUID, action string) int {
		return count(`SELECT count(*) FROM admin_audit WHERE target_user_id = $1 AND action = $2`, uid, action)
	}
	grantRows := func(uid uuid.UUID) int {
		return count(`SELECT count(*) FROM entitlements WHERE user_id = $1 AND source = 'grant'`, uid)
	}
	run := func(p GrantParams) (int, string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := RunGrantPro(ctx, pool, &out, &errOut, demoID, now, p)
		for _, s := range []string{out.String(), errOut.String()} {
			if strings.Contains(strings.ToLower(s), strings.ToLower(creator)) || strings.Contains(s, "grant.example.test") {
				t.Errorf("output carries the address:\n%s", s)
			}
		}
		return code, out.String(), errOut.String()
	}
	until := now.AddDate(0, 0, 90).Format("2006-01-02")
	base := GrantParams{Email: creator, Until: until, Reason: "creator program test"}

	// Refusals write nothing: no account, two accounts, the demo, a disabled account, a bad date, no reason.
	cases := map[string]GrantParams{
		"no account":   {Email: "nobody." + suffix + "@grant.example.test", Until: until, Reason: "x"},
		"two accounts": {Email: dupA, Until: until, Reason: "x"},
		"disabled":     {Email: "off." + suffix + "@grant.example.test", Until: until, Reason: "x"},
		"past date":    {Email: creator, Until: now.AddDate(0, 0, -1).Format("2006-01-02"), Reason: "x"},
		"401 days":     {Email: creator, Until: now.AddDate(0, 0, GrantMaxDays+1).Format("2006-01-02"), Reason: "x"},
		"no reason":    {Email: creator, Until: until},
	}
	for name, p := range cases {
		if code, _, errOut := run(p); code == 0 || errOut == "" {
			t.Errorf("%s: exit %d, want a refusal", name, code)
		}
	}
	if grantRows(cid) != 0 || audits(cid, AuditGrant) != 0 {
		t.Fatal("a refusal wrote a row")
	}

	// A dry run says what it would do and writes nothing.
	dry := base
	dry.DryRun = true
	code, out, _ := run(dry)
	if code != 0 || !strings.Contains(out, "plan: grant Pro until "+until) || !strings.Contains(out, "dry run: nothing written") {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if grantRows(cid) != 0 || audits(cid, AuditGrant) != 0 {
		t.Fatal("a dry run wrote a row")
	}

	// The real run writes one entitlement row and one audit row (with the reason), and the account reads as Pro.
	if code, out, errOut := run(base); code != 0 {
		t.Fatalf("grant: exit %d\n%s\n%s", code, out, errOut)
	}
	if grantRows(cid) != 1 || audits(cid, AuditGrant) != 1 {
		t.Fatalf("rows after a grant: entitlement %d, audit %d, want 1 and 1", grantRows(cid), audits(cid, AuditGrant))
	}
	var reason, untilStored, access string
	if err := pool.QueryRowContext(ctx, `SELECT details->>'reason', details->>'until', access_email FROM admin_audit WHERE target_user_id = $1 AND action = $2`, cid, AuditGrant).Scan(&reason, &untilStored, &access); err != nil {
		t.Fatal(err)
	}
	if reason != base.Reason || untilStored != until || access != grantAuditSource {
		t.Errorf("audit details: reason %q until %q access %q", reason, untilStored, access)
	}
	s := New(pool, Config{Billing: true})
	s.now = func() time.Time { return now }
	st, err := s.State(ctx, cid)
	if err != nil || !st.Pro || st.Source != SourceGrant {
		t.Fatalf("after the grant: %+v %v, want Pro by grant", st, err)
	}
	if _, free := s.Window(ctx, cid); free {
		t.Error("a granted account has a history window")
	}
	// The same grant again is refused (it ends at the same time) and writes no second audit row.
	if code, _, _ := run(base); code == 0 || audits(cid, AuditGrant) != 1 {
		t.Errorf("a repeat grant: exit %d, audit rows %d", code, audits(cid, AuditGrant))
	}

	// Past the end date the account is free again and nothing is deleted: the grant row and the old drive are still there, the window
	// hides history instead.
	s.Invalidate(cid)
	s.now = func() time.Time { return now.AddDate(0, 0, 91) }
	st, _ = s.State(ctx, cid)
	if st.Pro || st.EndedSource != SourceGrant || st.EndedAt == nil {
		t.Errorf("the day after: %+v, want free with a grant lapse", st)
	}
	if _, free := s.Window(ctx, cid); !free {
		t.Error("an account whose grant ended has no history window")
	}
	if grantRows(cid) != 1 || count(`SELECT count(*) FROM drives WHERE user_id = $1`, cid) != 1 {
		t.Error("ending a grant deleted something")
	}

	// A new grant after it ended reuses the one row (one per source) and brings Pro back.
	again := base
	again.Until = now.AddDate(0, 0, 200).Format("2006-01-02")
	if code, out, errOut := run(again); code != 0 || grantRows(cid) != 1 {
		t.Fatalf("regrant: exit %d, rows %d\n%s%s", code, grantRows(cid), out, errOut)
	}
	s.Invalidate(cid)
	s.now = func() time.Time { return now }
	if st, _ := s.State(ctx, cid); !st.Pro || st.Source != SourceGrant {
		t.Errorf("after the regrant: %+v", st)
	}

	// A subscription started during the grant is the plan; the grant stays beside it and does not shadow or double it. The checkout takes
	// its billing start from the grant's end (FreeUntil).
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end, external_id) VALUES ($1, 'stripe', 'active', $2, $3)`,
		cid, now.AddDate(0, 1, 0), "sub_grant_"+suffix); err != nil {
		t.Fatal(err)
	}
	s.Invalidate(cid)
	if st, _ := s.State(ctx, cid); !st.Pro || st.Source != SourceStripe {
		t.Errorf("with a subscription: %+v, want stripe", st)
	}
	if end, ok, err := s.FreeUntil(ctx, cid); err != nil || !ok || end.Format("2006-01-02") != again.Until {
		t.Errorf("FreeUntil = %v %v %v, want the grant's end %s", end, ok, err, again.Until)
	}

	// An account with a running paid plan is never granted over it, and the refusal says what it found.
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end, external_id) VALUES ($1, 'stripe', 'active', $2, $3)`,
		paid, now.AddDate(0, 6, 0), "sub_paid_"+suffix); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := run(GrantParams{Email: "paid." + suffix + "@grant.example.test", Until: until, Reason: "x"})
	if code != 1 || !strings.Contains(errOut, "paid stripe plan") || grantRows(paid) != 0 {
		t.Errorf("grant over a paid plan: exit %d, %q, grant rows %d", code, errOut, grantRows(paid))
	}

	// A running founding year is refused too: its end-of-year notice would be false beside a grant.
	fnd := mk("grant-founding-"+suffix, "founding."+suffix+"@grant.example.test")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'founding', 'active', $2)`, fnd, now.AddDate(0, 0, 30)); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = run(GrantParams{Email: "founding." + suffix + "@grant.example.test", Until: until, Reason: "x"})
	if code != 1 || !strings.Contains(errOut, "founding Pro") || grantRows(fnd) != 0 {
		t.Errorf("grant over a founding year: exit %d, %q, grant rows %d", code, errOut, grantRows(fnd))
	}

	// Revoke: ends only the grant row, writes its own audit row, and the account's other rows are untouched. A dry run writes nothing.
	rev := GrantParams{Email: creator, Reason: "creator program ended", Revoke: true, DryRun: true}
	if code, out, _ := run(rev); code != 0 || !strings.Contains(out, "plan: end the grant") {
		t.Fatalf("revoke dry run: %d\n%s", code, out)
	}
	var status string
	pool.QueryRowContext(ctx, `SELECT status FROM entitlements WHERE user_id = $1 AND source = 'grant'`, cid).Scan(&status)
	if status != "active" || audits(cid, AuditRevoke) != 0 {
		t.Fatalf("revoke dry run changed something: %s, %d audit rows", status, audits(cid, AuditRevoke))
	}
	rev.DryRun = false
	if code, out, errOut := run(rev); code != 0 {
		t.Fatalf("revoke: %d\n%s%s", code, out, errOut)
	}
	pool.QueryRowContext(ctx, `SELECT status FROM entitlements WHERE user_id = $1 AND source = 'grant'`, cid).Scan(&status)
	var stripeStatus string
	pool.QueryRowContext(ctx, `SELECT status FROM entitlements WHERE user_id = $1 AND source = 'stripe'`, cid).Scan(&stripeStatus)
	if status != "ended" || stripeStatus != "active" || audits(cid, AuditRevoke) != 1 {
		t.Errorf("after the revoke: grant %s, stripe %s, revoke audit rows %d", status, stripeStatus, audits(cid, AuditRevoke))
	}
	// Nothing left to revoke: refused. A paid-only account has no grant row to revoke either.
	if code, _, _ := run(rev); code == 0 {
		t.Error("a second revoke was accepted")
	}
	if code, _, _ := run(GrantParams{Email: "paid." + suffix + "@grant.example.test", Reason: "x", Revoke: true}); code == 0 {
		t.Error("a revoke on an account with no grant was accepted")
	}
	if status := func() string {
		var s string
		pool.QueryRowContext(ctx, `SELECT status FROM entitlements WHERE user_id = $1 AND source = 'stripe'`, paid).Scan(&s)
		return s
	}(); status != "active" {
		t.Errorf("a revoke touched a paid row: %s", status)
	}
}
