package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// The squashed baseline (migrations v2, step 5). baseline/0000_baseline.sql is the schema, seed rows and grants of every migration file
// of one release tag, made by .github/scripts/migration-baseline.sh from that tag's files (CI proves it equals their replay). Its header
// names the tag ("-- baseline-of:") and the ids it stands for ("-- covers:"). Migrate treats it like this:
//
//   - a database with no migrations recorded gets the baseline in one transaction, and every covered id is recorded as applied;
//   - a database that has every covered id gets nothing: not one write;
//   - a database with some of them, or none of them next to others, is refused, naming the tag to upgrade to first.
//
// The covered files may still sit in migrations/ (they are never run for a database that has their ids) or be gone; the runner needs only the ids.
//
//go:embed baseline/0000_baseline.sql
var baselineSQL string

// ErrBaselinePartial is a database that has part of what the baseline covers: it is older than the tag the baseline was made from.
var ErrBaselinePartial = errors.New("database is older than the migration baseline")

type baseline struct {
	tag    string
	covers []string
	sql    string
}

// loadBaseline reads the header of the embedded baseline.
func loadBaseline() (baseline, error) { return parseBaseline(baselineSQL) }

func parseBaseline(text string) (baseline, error) {
	b := baseline{sql: text}
	for _, ln := range strings.Split(text, "\n") {
		if !strings.HasPrefix(ln, "--") {
			break
		}
		switch {
		case strings.HasPrefix(ln, "-- baseline-of: "):
			b.tag = strings.TrimSpace(strings.TrimPrefix(ln, "-- baseline-of: "))
		case strings.HasPrefix(ln, "-- covers: "):
			b.covers = strings.Fields(strings.TrimPrefix(ln, "-- covers: "))
		}
	}
	if b.tag == "" || len(b.covers) == 0 {
		return baseline{}, errors.New(`baseline header lacks "-- baseline-of:" or "-- covers:"`)
	}
	sort.Strings(b.covers)
	return b, nil
}

// settleBaseline applies the baseline to an empty database, leaves a complete one alone, and refuses a partial one. It returns the applied set
// as it is afterwards.
func settleBaseline(ctx context.Context, d *sql.DB, b baseline, applied map[string]bool) (map[string]bool, error) {
	var missing []string
	for _, id := range b.covers {
		if !applied[id] {
			missing = append(missing, id)
		}
	}
	switch {
	case len(missing) == 0:
		return applied, nil
	case len(applied) > 0:
		return nil, fmt.Errorf("%w: %d of the %d migrations it covers are missing (first %s); upgrade to release %s or later first, then to this one",
			ErrBaselinePartial, len(missing), len(b.covers), missing[0], b.tag)
	}
	if err := applyBaseline(ctx, d, b); err != nil {
		return nil, fmt.Errorf("apply baseline of %s: %w", b.tag, err)
	}
	return loadApplied(ctx, d)
}

// applyBaseline runs the baseline and records the covered ids in one transaction on a connection of its own: the dump sets session
// settings (an empty search_path among them), so the connection is reset before it goes back to the pool, and dropped if that fails.
func applyBaseline(ctx context.Context, d *sql.DB, b baseline) (err error) {
	started := time.Now()
	conn, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if _, rerr := conn.ExecContext(context.Background(), "RESET ALL"); rerr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Migrate created the bookkeeping table (empty) before it knew the database was fresh; the dump creates it again, with every column.
	if _, err := tx.ExecContext(ctx, `DROP TABLE public.migrations`); err != nil {
		return fmt.Errorf("drop empty bookkeeping table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, b.sql); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	now := time.Now().Unix()
	for _, id := range b.covers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO public.migrations (id, applied_at, note) VALUES ($1, $2, $3)`, id, now, "baseline of "+b.tag); err != nil {
			return fmt.Errorf("record %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("db baseline applied", "baseline_of", b.tag, "covers", len(b.covers), "ms", time.Since(started).Milliseconds())
	return nil
}
