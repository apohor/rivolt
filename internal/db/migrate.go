package db

import (
	"context"
	"database/sql"
	"embed"
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
// and rebuild". Nothing else to touch.
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
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
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
	slog.Info("db migration applied", "id", m.id)
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
