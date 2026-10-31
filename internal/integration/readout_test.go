//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
)

// The Monday readout (scripts/readout) run against fixtures: part 1 under a read-only role, part 2 shown to be closed to it, and part 2
// as the owner. Plain fake ids and addresses throughout. The statements are the real files, with psql's variables replaced and its
// meta-commands dropped.

type readoutQuery struct {
	sql  string
	cols []string
}

// readoutStatements turns a readout .sql file into plain statements for the week [from, to).
func readoutStatements(t *testing.T, file, from, to string) []string {
	t.Helper()
	b, err := os.ReadFile("../../scripts/readout/" + file)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer(":'from_ts'", "'"+from+"'", ":'to_ts'", "'"+to+"'").Replace(string(b))
	var keep []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, `\`) || strings.HasPrefix(strings.TrimSpace(l), "--") {
			continue
		}
		keep = append(keep, l)
	}
	var out []string
	for _, s := range strings.Split(strings.Join(keep, "\n"), ";") {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// firstRow runs a statement and returns its first row as column -> value (int64, string or nil).
func firstRow(ctx context.Context, t *testing.T, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, stmt string) map[string]any {
	t.Helper()
	rows, err := q.QueryContext(ctx, stmt)
	if err != nil {
		t.Fatalf("%v\n%s", err, stmt)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		return nil
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for i, c := range cols {
		out[c] = vals[i]
	}
	return out
}

func wantInts(t *testing.T, section string, got map[string]any, want map[string]int64) {
	t.Helper()
	for col, w := range want {
		if g, ok := got[col].(int64); !ok || g != w {
			t.Errorf("%s: %s = %v, want %d", section, col, got[col], w)
		}
	}
}

func TestMondayReadoutAgainstFixtures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	week := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) // a Monday
	in, prev := week.Add(40*time.Hour), week.AddDate(0, 0, -4)
	long := week.AddDate(0, -3, 0)
	fromTS, toTS := week.Format(time.RFC3339), week.AddDate(0, 0, 7).Format(time.RFC3339)

	user := func(name string, created time.Time) uuid.UUID {
		t.Helper()
		id, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = $2 WHERE id = $1`, id, created); err != nil {
			t.Fatal(err)
		}
		return id
	}
	vehicle := func(uid uuid.UUID, id string, created time.Time) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, created_at) VALUES ($1, $2, $3)`, uid, id, created); err != nil {
			t.Fatal(err)
		}
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	drive := func(uid uuid.UUID, veh, id string, at time.Time) {
		t.Helper()
		d := drives.Drive{ID: id, VehicleID: veh, StartedAt: at, EndedAt: at.Add(30 * time.Minute), StartSoCPct: 80, EndSoCPct: 70,
			DistanceMi: 5, EnergyUsedKWh: 2, Source: "live"}
		if err := drives.NewFactory(pool, resolvers).For(uid).Upsert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	charge := func(uid uuid.UUID, veh, id string, at time.Time) {
		t.Helper()
		c := charges.Charge{ID: id, VehicleID: veh, StartedAt: at, EndedAt: at.Add(40 * time.Minute), StartSoCPct: 30, EndSoCPct: 60,
			EnergyAddedKWh: 20, MaxPowerKW: 11, FinalState: "charging_complete", Source: "live"}
		if err := charges.NewFactory(pool, resolvers).For(uid).Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	demo := user(db.DemoUsername, in) // must be in no number
	a := user("readout-a", in)        // signed up this week, connected, drove
	b := user("readout-b", in)        // signed up this week, nothing else
	c := user("readout-c", prev)      // last week's sign-up, active in both weeks
	d := user("readout-d", long)      // an old account, one imported vehicle, drove this week
	vehicle(demo, "rv-readout-demo", in)
	drive(demo, "rv-readout-demo", "readout-demo-drive", in)
	vehicle(a, "rv-readout-a", in)
	drive(a, "rv-readout-a", "readout-a-drive", in)
	if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id) VALUES ($1, 'rivian.session', '\x00', 'test')`, a); err != nil {
		t.Fatal(err)
	}
	_ = b
	vehicle(c, "rv-readout-c", prev)
	drive(c, "rv-readout-c", "readout-c-drive-prev", prev)
	charge(c, "rv-readout-c", "readout-c-charge", in)
	vehicle(d, "electrafi_readout_d", long)
	drive(d, "electrafi_readout_d", "readout-d-drive", in)

	// Part 2's tables.
	ev := func(ts time.Time, anon, name, props string) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO events (ts, anon_id, name, props) VALUES ($1, $2, $3, $4::jsonb)`, ts, anon, name, props); err != nil {
			t.Fatal(err)
		}
	}
	ev(in, "anon-1", "landing_view", `{"referrer_host":"news.example","utm_source":"tw"}`)
	ev(in, "anon-1", "landing_view", `{"referrer_host":"news.example","utm_source":"tw"}`)
	ev(in, "anon-2", "landing_view", `{"referrer_host":"news.example","utm_source":"tw"}`)
	ev(in, "anon-3", "landing_view", `{}`)
	ev(week.Add(-time.Hour), "anon-4", "landing_view", `{"referrer_host":"before.example"}`) // outside the week
	ev(in, "anon-5", "plan_run", `{"stops_bucket":"1"}`)
	ev(in, "anon-6", "plan_run", `{"stops_bucket":"2"}`)
	ev(in, "anon-7", "statement_share", `{"kind":"png"}`)
	ev(in, "anon-8", "statement_share", `{"kind":"csv"}`)
	for _, uid := range []uuid.UUID{a, d} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES ($1, 'https://push.example/'||$1::text, 'k', 'a')`, uid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.ExecContext(ctx, `UPDATE push_subscriptions SET on_milestones = TRUE WHERE user_id = $1`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO engagement_prefs (user_id, weekly_recap) VALUES ($1, TRUE)`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO statement_email_prefs (user_id, enabled) VALUES ($1, TRUE)`, c); err != nil {
		t.Fatal(err)
	}

	// Part 1 as a read-only role with the production allowlist (BYPASSRLS, like the CNPG-managed role).
	role := "readout_ro_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := pool.ExecContext(ctx, `CREATE ROLE `+role+` NOLOGIN BYPASSRLS`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.ExecContext(context.Background(), `DROP OWNED BY `+role)
		_, _ = pool.ExecContext(context.Background(), `DROP ROLE `+role)
	})
	if ok, err := db.GrantReadonly(ctx, pool, role); err != nil || !ok {
		t.Fatalf("GrantReadonly = %v, %v", ok, err)
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+role); err != nil {
		t.Fatal(err)
	}
	roStmts := readoutStatements(t, "monday_ro.sql", fromTS, toTS)
	if len(roStmts) != 5 {
		t.Fatalf("monday_ro.sql has %d statements, want 5", len(roStmts))
	}
	connQ := connQuerier{conn}
	wantInts(t, "sign-ups", firstRow(ctx, t, connQ, roStmts[1]), map[string]int64{"this_week": 2, "previous_week": 1, "accounts_total": 4})
	wantInts(t, "connected", firstRow(ctx, t, connQ, roStmts[2]), map[string]int64{
		"first_vehicle_this_week": 1, "first_vehicle_previous_week": 1, "connected_now": 1, "this_weeks_signups_connected_by_now": 1})
	wantInts(t, "first data", firstRow(ctx, t, connQ, roStmts[3]), map[string]int64{
		"first_data_this_week": 2, "first_data_previous_week": 1, "this_weeks_signups_with_data_by_now": 1})
	wantInts(t, "weekly active", firstRow(ctx, t, connQ, roStmts[4]), map[string]int64{
		"active_this_week": 3, "active_previous_week": 1, "active_both_weeks": 1})

	// Part 2 is closed to that role, which is why it is marked owner-side.
	ownerStmts := readoutStatements(t, "monday_owner.sql", fromTS, toTS)
	for _, i := range []int{0, 2, 4} { // landing totals, plan_run/statement_share, alerts: events / push_* / engagement_prefs
		if rows, err := conn.QueryContext(ctx, ownerStmts[i]); err == nil {
			rows.Close()
			t.Errorf("owner-side statement %d ran as the read-only role; it must be closed to it", i)
		} else if !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("owner-side statement %d failed as the role for another reason: %v", i, err)
		}
	}
	_, _ = conn.ExecContext(ctx, `RESET ROLE`)

	// Part 2 as the owner.
	if len(ownerStmts) != 6 {
		t.Fatalf("monday_owner.sql has %d statements, want 6", len(ownerStmts))
	}
	wantInts(t, "landing totals", firstRow(ctx, t, pool, ownerStmts[0]), map[string]int64{"views_total": 4, "distinct_browsers": 3})
	top := firstRow(ctx, t, pool, ownerStmts[1])
	if top["referrer_host"] != "news.example" || top["utm_source"] != "tw" || top["views"] != int64(3) {
		t.Errorf("top landing source = %v", top)
	}
	rows, err := pool.QueryContext(ctx, ownerStmts[2])
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for rows.Next() {
		var name string
		var events, people int64
		if err := rows.Scan(&name, &events, &people); err != nil {
			t.Fatal(err)
		}
		got[name] = events*10 + people
	}
	rows.Close()
	if got["plan_run"] != 22 || got["statement_share"] != 22 {
		t.Errorf("plan_run / statement_share (events*10+people) = %v, want 22 and 22", got)
	}
	wantInts(t, "alerts", firstRow(ctx, t, pool, ownerStmts[4]), map[string]int64{"any_switch_on": 2, "opted_in_extra": 1, "on_web": 2, "on_iphone": 0})
	wantInts(t, "email and recap switches", firstRow(ctx, t, pool, ownerStmts[5]), map[string]int64{
		"weekly_recap_on": 1, "trip_recap_on": 0, "statement_email_on": 1, "r2_arrival_reminder_set": 0})

	// Counts only: nothing the readout selects looks like an id, an address or a place.
	for _, f := range []string{"monday_ro.sql", "monday_owner.sql"} {
		b, _ := os.ReadFile("../../scripts/readout/" + f)
		for _, bad := range []*regexp.Regexp{regexp.MustCompile(`(?i)\bSELECT\b[^;]*\b(email|display_name|username|ip|lat|lon|endpoint|p256dh|auth)\b\s*(,|\bFROM\b)`)} {
			if bad.Match(b) {
				t.Errorf("%s selects an identifying column", f)
			}
		}
	}
}

type connQuerier struct{ c *sql.Conn }

func (q connQuerier) QueryContext(ctx context.Context, s string, a ...any) (*sql.Rows, error) {
	return q.c.QueryContext(ctx, s, a...)
}
