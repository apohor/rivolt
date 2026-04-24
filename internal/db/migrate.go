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

// Migrate runs every unapplied migration in migrationFS against db.
// The migrations table is self-bootstrapped on first boot so fresh
// Postgres databases come up clean.
func Migrate(ctx context.Context, db *sql.DB) error {
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
