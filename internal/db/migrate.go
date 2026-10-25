package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// migrationFS holds every schema + data migration Rivolt ships. Files
// are applied in lexical order of their basename, so date-prefixed
// names (YYYY-MM-DD-…) or zero-padded numeric prefixes (0001_…) both
// produce the right apply order.
//
// Adding a new migration is "drop a .sql file in internal/db/migrations/
// and rebuild". Nothing else to touch, except that one altering
// vehicle_state sets a bounded lock_timeout first (see
// migrate_locktimeout_test.go). Hitting that timeout fails the
// migration, so Open fails and the server exits at startup; the pod
// restarts and retries. That is the intended trade: a delayed boot
// instead of every vehicle_state insert queued behind the ALTER's lock.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockID is the session-level pg_advisory_lock key Migrate
// uses to serialise concurrent boots. Hex bytes spell "rivoltdm";
// any stable int64 unique to this codebase would do, but a recognisable
// constant makes it easier to spot in pg_locks during a stuck boot.
const migrationLockID int64 = 0x7269766f6c74646d

// Migrate runs every unapplied migration in migrationFS against db.
// The migrations table is self-bootstrapped on first boot so fresh
// Postgres databases come up clean.
//
// Multi-pod safety: two pods booting concurrently against the same
// database both call Migrate. We serialise them with a session-level
// pg_advisory_lock — the second caller blocks until the first
// finishes, then re-reads the migrations table and finds everything
// already applied. The lock is held on a dedicated connection so
// migrations themselves run on the pool unaffected, and is released
// (best-effort) on a background context so a cancelled ctx doesn't
// leak the lock for the rest of the conn's lifetime.
func Migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration conn: %w", err)
	}
	defer conn.Close()
	if err := acquireMigrationLock(ctx, conn); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS migrations (
			id         TEXT PRIMARY KEY,
			applied_at BIGINT NOT NULL,
			note       TEXT
		)`); err != nil {
		return fmt.Errorf("bootstrap migrations table: %w", err)
	}
	steps, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}
	applied, err := loadApplied(ctx, db)
	if err != nil {
		return fmt.Errorf("load applied migrations: %w", err)
	}
	for _, m := range steps {
		if applied[m.id] {
			continue
		}
		if err := run(ctx, db, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.id, err)
		}
	}
	return nil
}

// migrationLockWait bounds how long Migrate waits for another migrator to finish (a var so tests can shorten it). A migrator that
// hangs holding the lock fails the next one visibly instead of wedging every boot or job behind it.
var (
	migrationLockWait = 5 * time.Minute
	migrationLockPoll = 2 * time.Second
)

// acquireMigrationLock takes the session-level advisory lock on conn, polling pg_try_advisory_lock until migrationLockWait. On
// timeout the error names the holder's backend pid (nothing else about it: no query text).
func acquireMigrationLock(ctx context.Context, conn *sql.Conn) error {
	deadline := time.Now().Add(migrationLockWait)
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationLockID).Scan(&got); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if got {
			return nil
		}
		if !time.Now().Before(deadline) {
			var pid sql.NullInt64
			// A bigint advisory key is stored as classid (high 32 bits) and objid (low 32 bits).
			_ = conn.QueryRowContext(ctx, `SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted AND objsubid = 1
				AND classid::bigint = $1 AND objid::bigint = $2 LIMIT 1`,
				int64(uint32(uint64(migrationLockID)>>32)), int64(uint32(uint64(migrationLockID)&0xffffffff))).Scan(&pid)
			if pid.Valid {
				return fmt.Errorf("another migrator (backend pid %d) has held the migration lock for over %s", pid.Int64, migrationLockWait)
			}
			return fmt.Errorf("the migration lock was not free after %s", migrationLockWait)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("acquire migration lock: %w", ctx.Err())
		case <-time.After(migrationLockPoll):
		}
	}
}

// SchemaState is what a database has against what this binary ships.
type SchemaState struct {
	Pending []string // shipped here, not applied there (oldest first)
	Unknown []string // applied there, not shipped here: the database is ahead (a rolled-back binary)
}

// CheckSchema reads, and changes nothing. A database with no migrations table has everything pending.
func CheckSchema(ctx context.Context, d *sql.DB) (SchemaState, error) {
	steps, err := loadMigrations()
	if err != nil {
		return SchemaState{}, fmt.Errorf("load embedded migrations: %w", err)
	}
	var reg sql.NullString
	if err := d.QueryRowContext(ctx, `SELECT to_regclass('public.migrations')::text`).Scan(&reg); err != nil {
		return SchemaState{}, err
	}
	applied := map[string]bool{}
	if reg.Valid {
		if applied, err = loadApplied(ctx, d); err != nil {
			return SchemaState{}, err
		}
	}
	var st SchemaState
	shipped := map[string]bool{}
	for _, m := range steps {
		shipped[m.id] = true
		if !applied[m.id] {
			st.Pending = append(st.Pending, m.id)
		}
	}
	for id := range applied {
		if !shipped[id] {
			st.Unknown = append(st.Unknown, id)
		}
	}
	sort.Strings(st.Unknown)
	return st, nil
}

// schemaPoll is how often RequireSchema looks again while the schema is behind (a var for tests).
var schemaPoll = 5 * time.Second

// RequireSchema is the server's startup check when it does not migrate itself: every migration this binary ships must already be
// applied. A schema that is behind is polled for up to wait (a migration job may be finishing), then it is an error. A schema
// that is AHEAD (applied migrations this binary does not ship) is fine: that is a rollback to an older image, which the
// expand-only rule for migrations makes safe, and it is logged.
func RequireSchema(ctx context.Context, d *sql.DB, wait time.Duration, log *slog.Logger) error {
	deadline := time.Now().Add(wait)
	warned := false
	for {
		st, err := CheckSchema(ctx, d)
		if err != nil {
			return fmt.Errorf("check schema: %w", err)
		}
		if len(st.Unknown) > 0 && !warned {
			log.Warn("database schema is ahead of this binary (a rollback?)", "unknown_migrations", len(st.Unknown), "newest", st.Unknown[len(st.Unknown)-1])
			warned = true
		}
		if len(st.Pending) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w (%d pending, next %s)", ErrSchemaBehind, len(st.Pending), st.Pending[0])
		}
		log.Info("schema is behind this binary, waiting for the migration job", "pending", len(st.Pending), "next", st.Pending[0])
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(schemaPoll):
		}
	}
}

// ErrSchemaBehind names the fix in one line; the server prints it and exits.
var ErrSchemaBehind = errors.New(`schema is behind this binary: run "rivolt migrate", or set RIVOLT_MIGRATE_ON_START=true for a single-node install`)

// MigrateOnStart reports whether the server migrates the schema itself at startup (RIVOLT_MIGRATE_ON_START). Unset means yes for
// now: the chart turns it off where a migration Job runs, and a later release flips the default.
func MigrateOnStart(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("RIVOLT_MIGRATE_ON_START"))) {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

type migration struct {
	id  string
	sql string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := fs.ReadFile(migrationFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out = append(out, migration{
			id:  strings.TrimSuffix(e.Name(), ".sql"),
			sql: string(body),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

func loadApplied(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// run applies one migration inside a single transaction. The SQL body
// may contain many statements; Postgres's simple-query protocol (which
// pgx uses for `Exec`) runs them serially. A failure rolls everything
// back and the migration will retry on the next boot.
func run(ctx context.Context, db *sql.DB, m migration) error {
	started := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	body := strings.TrimSpace(m.sql)
	if body == "" {
		return fmt.Errorf("empty migration body")
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO migrations (id, applied_at, note) VALUES ($1, $2, $3)`,
		m.id, time.Now().Unix(), firstSQLLine(body),
	); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("db migration applied", "id", m.id, "ms", time.Since(started).Milliseconds())
	return nil
}

// firstSQLLine stores a human-readable hint in migrations.note so a
// `SELECT id, note FROM migrations` tells you what each one did
// without cross-referencing the source file.
func firstSQLLine(body string) string {
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		if len(t) > 200 {
			t = t[:200] + "…"
		}
		return t
	}
	return ""
}

// LatestEmbedded is the id of the newest migration this binary ships.
func LatestEmbedded() (string, error) {
	steps, err := loadMigrations()
	if err != nil || len(steps) == 0 {
		return "", fmt.Errorf("load embedded migrations: %v", err)
	}
	return steps[len(steps)-1].id, nil
}

// LatestApplied is the newest migration id recorded in the database
// ("" when the migrations table does not exist). Read-only.
func LatestApplied(ctx context.Context, d *sql.DB) (string, error) {
	var id sql.NullString
	var reg sql.NullString
	if err := d.QueryRowContext(ctx, `SELECT to_regclass('public.migrations')::text`).Scan(&reg); err != nil {
		return "", err
	}
	if !reg.Valid {
		return "", nil
	}
	if err := d.QueryRowContext(ctx, `SELECT max(id) FROM migrations`).Scan(&id); err != nil {
		return "", err
	}
	return id.String, nil
}

// RequireTables verifies, read-only, that every named table exists, and
// returns an error that prints the database's and the binary's latest
// migration when one does not. Repair commands call it instead of
// migrating.
func RequireTables(ctx context.Context, d *sql.DB, tables ...string) error {
	var missing []string
	for _, t := range tables {
		var reg sql.NullString
		if err := d.QueryRowContext(ctx, `SELECT to_regclass('public.' || $1)::text`, t).Scan(&reg); err != nil {
			return err
		}
		if !reg.Valid {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	have, _ := LatestApplied(ctx, d)
	want, _ := LatestEmbedded()
	return fmt.Errorf("database schema lacks %s (database at migration %q, binary at %q); not migrating", strings.Join(missing, ", "), have, want)
}

// TableExists reports whether a table exists in the public schema.
func TableExists(ctx context.Context, d *sql.DB, name string) (bool, error) {
	var reg sql.NullString
	if err := d.QueryRowContext(ctx, `SELECT to_regclass('public.' || $1)::text`, name).Scan(&reg); err != nil {
		return false, err
	}
	return reg.Valid, nil
}
