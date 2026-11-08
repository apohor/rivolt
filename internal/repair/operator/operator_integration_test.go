//go:build integration

package operator_test

import (
	"context"
	"database/sql"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair/operator"
	"github.com/apohor/rivolt/internal/testpg"
)

const repairPassword = "repair-test-pw"

// digest is a stand-in for the approved dry run's sha256.
const digest = "abababababababababababababababababababababababababababababababab"

// fixture is a migrated database with the repair role created and granted, an owner pool (the migration role) and a charge to work on.
type fixture struct {
	owner     *sql.DB
	repairDSN string
	user      uuid.UUID
	vehicle   uuid.UUID
	charge    uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dsn := testpg.DSN(ctx, t)
	owner, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	// Roles are cluster-wide, so a database made by an earlier test of this binary already has it. With no role there is nothing to grant.
	var have bool
	if err := owner.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rivolt_repair')`).Scan(&have); err != nil {
		t.Fatal(err)
	}
	if !have {
		if ok, err := db.GrantRepair(ctx, owner); err != nil || ok {
			t.Fatalf("grants without the role: ok=%v err=%v", ok, err)
		}
		if _, err := owner.ExecContext(ctx, `CREATE ROLE rivolt_repair LOGIN BYPASSRLS PASSWORD '`+repairPassword+`'`); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ { // idempotent
		if ok, err := db.GrantRepair(ctx, owner); err != nil || !ok {
			t.Fatalf("grants: ok=%v err=%v", ok, err)
		}
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("rivolt_repair", repairPassword)
	f := &fixture{owner: owner, repairDSN: u.String()}
	if f.user, err = db.EnsureUser(ctx, owner, "operator-test"); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'rv-op-1') RETURNING id`, f.user).Scan(&f.vehicle); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-48 * time.Hour)
	if err := owner.QueryRowContext(ctx, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, energy_added_kwh)
		VALUES ($1, $2, 'c-op-1', $3, $4, 'live', 10) RETURNING id`, f.user, f.vehicle, now, now.Add(time.Hour)).Scan(&f.charge); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) energy(t *testing.T) float64 {
	t.Helper()
	var e float64
	if err := f.owner.QueryRow(`SELECT energy_added_kwh FROM charges WHERE id = $1`, f.charge).Scan(&e); err != nil {
		t.Fatal(err)
	}
	return e
}

// The repair role cannot write a guarded table without an open operator action, whatever else it is allowed; the owner is unaffected.
func TestRepairRoleWritesNeedAnOpenAction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	repair, err := db.OpenNoMigrate(ctx, f.repairDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer repair.Close()

	// No action at all.
	if _, err := repair.ExecContext(ctx, `UPDATE charges SET energy_added_kwh = 11 WHERE id = $1`, f.charge); err == nil || !strings.Contains(err.Error(), "operator action") {
		t.Fatalf("update with no action: err=%v, want a refusal naming the operator action", err)
	}
	// The owner's own writes are not guarded: the trigger only fires for the repair role.
	if _, err := f.owner.ExecContext(ctx, `UPDATE charges SET energy_added_kwh = 12 WHERE id = $1`, f.charge); err != nil || f.energy(t) != 12 {
		t.Fatalf("owner update: err=%v energy=%v", err, f.energy(t))
	}
	// A reading role: SELECT works, the secrets stay closed, and so does the audit table (only its functions are open).
	var n int
	if err := repair.QueryRowContext(ctx, `SELECT count(*) FROM charges`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("repair select: n=%d err=%v", n, err)
	}
	for _, closed := range []string{"user_secrets", "sessions", "operator_actions", "app_settings"} {
		if err := repair.QueryRowContext(ctx, `SELECT count(*) FROM `+closed).Scan(&n); err == nil {
			t.Errorf("the repair role can read %s", closed)
		}
	}
	// And it cannot write a table outside the repairs' list.
	if _, err := repair.ExecContext(ctx, `UPDATE users SET username = username WHERE id = $1`, f.user); err == nil {
		t.Error("the repair role can write users")
	}
}

// The full path through operator.Open: an action is opened with a role label and a reason, the pool carries it, the writes land and are
// counted by the guard, and closing the run ends the action: a later write on the same connection settings is refused.
func TestOperatorPathAuditsARun(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	getenv := func(k string) string {
		return map[string]string{operator.EnvDSN: f.repairDSN, operator.EnvRole: "lead", operator.EnvReason: "test run", operator.EnvDigest: digest}[k]
	}
	run, err := operator.Open(ctx, "repair-test", true, "", getenv, "ignored", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Pool.ExecContext(ctx, `UPDATE charges SET energy_added_kwh = 13 WHERE id = $1`, f.charge); err != nil {
		t.Fatalf("update inside the action: %v", err)
	}
	if _, err := run.Pool.ExecContext(ctx, `INSERT INTO charges_deleted_backup (batch_id, kind, charge_id, user_id, row, reason) VALUES ($1, 'charge', $2, $3, '{}', 'x')`,
		uuid.New(), f.charge, f.user); err != nil {
		t.Fatalf("backup insert inside the action: %v", err)
	}
	var role, tool, reason string
	var written int
	var ended sql.NullTime
	q := `SELECT role_label, tool, reason, rows_written, ended_at FROM operator_actions ORDER BY started_at DESC LIMIT 1`
	if err := f.owner.QueryRowContext(ctx, q).Scan(&role, &tool, &reason, &written, &ended); err != nil {
		t.Fatal(err)
	}
	if role != "lead" || tool != "repair-test" || reason != "test run" || written != 2 || ended.Valid {
		t.Fatalf("open action: role=%q tool=%q reason=%q written=%d ended=%v, want lead, repair-test, test run, 2, open", role, tool, reason, written, ended.Valid)
	}
	if err := run.Done(nil); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := f.owner.QueryRowContext(ctx, `SELECT outcome, ended_at IS NOT NULL FROM operator_actions ORDER BY started_at DESC LIMIT 1`).Scan(&outcome, new(bool)); err != nil || outcome != "ok" {
		t.Fatalf("closed action: outcome=%q err=%v", outcome, err)
	}
	// The run is over: the same pool is refused.
	if _, err := run.Pool.ExecContext(ctx, `UPDATE charges SET energy_added_kwh = 14 WHERE id = $1`, f.charge); err == nil {
		t.Error("a write after the action ended was allowed")
	}
	_ = run.Pool.Close()
	if f.energy(t) != 13 {
		t.Errorf("energy %v, want the run's 13", f.energy(t))
	}
}

// An action that is too old, unknown, or from another run does not authorise a write; the begin and end functions validate their input.
func TestOperatorActionBounds(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	repair, err := db.OpenNoMigrate(ctx, f.repairDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer repair.Close()
	var id string
	if err := repair.QueryRowContext(ctx, `SELECT rivolt_operator_begin('owner', 'repair-test', 'abc', 'bounds', '`+digest+`')::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	with := func(action string) (*sql.DB, error) {
		return db.OpenNoMigrateWith(ctx, f.repairDSN, map[string]string{"rivolt.operator_action": action})
	}
	try := func(action string) error {
		p, err := with(action)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		_, err = p.ExecContext(ctx, `UPDATE charges SET energy_added_kwh = 15 WHERE id = $1`, f.charge)
		return err
	}
	if err := try(id); err != nil {
		t.Fatalf("a fresh open action: %v", err)
	}
	for name, action := range map[string]string{"unknown id": uuid.NewString(), "not a uuid": "nope", "empty": ""} {
		if err := try(action); err == nil {
			t.Errorf("%s authorised a write", name)
		}
	}
	// Twelve hours is the longest an action stays good.
	if _, err := f.owner.ExecContext(ctx, `UPDATE operator_actions SET started_at = now() - interval '13 hours' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := try(id); err == nil {
		t.Error("an action older than 12 hours authorised a write")
	}
	// Input is validated: a name is not a role, an outcome is ok or failed, an action closes once.
	if err := repair.QueryRowContext(ctx, `SELECT rivolt_operator_begin('Alex', 't', '', 'r', '`+digest+`')::text`).Scan(&id); err == nil {
		t.Error("a person's name was accepted as the role label")
	}
	if err := repair.QueryRowContext(ctx, `SELECT rivolt_operator_begin('lead', 't', '', '', '`+digest+`')::text`).Scan(&id); err == nil {
		t.Error("an empty reason was accepted")
	}
	var id2 string
	if err := repair.QueryRowContext(ctx, `SELECT rivolt_operator_begin('lead', 't', '', 'r', '`+digest+`')::text`).Scan(&id2); err != nil {
		t.Fatal(err)
	}
	if _, err := repair.ExecContext(ctx, `SELECT rivolt_operator_end($1::uuid, 'maybe')`, id2); err == nil {
		t.Error("an outcome other than ok or failed was accepted")
	}
	if _, err := repair.ExecContext(ctx, `SELECT rivolt_operator_end($1::uuid, 'ok')`, id2); err != nil {
		t.Fatal(err)
	}
	if _, err := repair.ExecContext(ctx, `SELECT rivolt_operator_end($1::uuid, 'ok')`, id2); err == nil {
		t.Error("an action closed twice")
	}
	// Only the repair role may call the functions (PUBLIC lost EXECUTE); the guard lists match the grants.
	var ok bool
	if err := f.owner.QueryRowContext(ctx, `SELECT has_function_privilege('public', 'rivolt_operator_begin(text,text,text,text,text)', 'EXECUTE')`).Scan(&ok); err != nil || ok {
		t.Errorf("PUBLIC can execute rivolt_operator_begin: %v (%v)", ok, err)
	}
}

// The digest of the approved dry run is required and checked: tied to every audited run.
func TestOperatorBeginRequiresTheApprovedDigest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	repair, err := db.OpenNoMigrate(ctx, f.repairDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer repair.Close()
	var id string
	for name, d := range map[string]string{"empty": "", "short": "abc", "upper case": strings.Repeat("AB", 32)} {
		if err := repair.QueryRowContext(ctx, `SELECT rivolt_operator_begin('lead', 't', '', 'r', $1)::text`, d).Scan(&id); err == nil {
			t.Errorf("%s digest was accepted", name)
		}
	}
	if err := repair.QueryRowContext(ctx, `SELECT rivolt_operator_begin('lead', 't', '', 'r', $1)::text`, digest).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := f.owner.QueryRowContext(ctx, `SELECT input_sha256 FROM operator_actions WHERE id = $1`, id).Scan(&stored); err != nil || stored != digest {
		t.Errorf("stored digest %q (%v), want %q", stored, err, digest)
	}
}
