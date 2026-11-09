package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Non-transactional migrations (zero-impact deploys, G6a part 3). Every migration runs in one transaction, which cannot hold CREATE INDEX
// CONCURRENTLY: the only way to add an index to a big live table without blocking its writes. A file whose first line is
//
//	-- migrate:no-transaction
//
// is run outside a transaction instead, and must be exactly ONE statement of the form
//
//	CREATE [UNIQUE] INDEX CONCURRENTLY IF NOT EXISTS <name> ON <table> ...;
//
// (nothing else is allowed here, so the runner can check it: a second statement would turn the call into an implicit transaction, and without IF
// NOT EXISTS a re-run after a crash would fail). The runner then:
//   - sets lock_timeout 5 s on that connection (the wait for the brief locks CONCURRENTLY takes) and statement_timeout noTxStatementTimeout (CONCURRENTLY
//     waits for every older transaction, which lock_timeout does not bound); the migrate Job's deadline (1200 s) is longer than that;
//   - first looks for an INVALID index of that name, which a failed earlier attempt leaves behind (IF NOT EXISTS would skip it for ever), drops it
//     with DROP INDEX CONCURRENTLY, logs it and counts it (IndexRepairs, rivolt_migrate_index_repaired_total);
//   - records the migration afterwards, in a separate statement: a crash between the two just re-runs an idempotent statement.
const (
	noTxDirective = "-- migrate:no-transaction"
	// noTxStatementTimeout is below the migrate Job's activeDeadlineSeconds (1200 s) with a margin for the rest of the Job.
	noTxStatementTimeout = 15 * time.Minute
	noTxLockTimeout      = 5 * time.Second
)

var indexRepaired atomic.Int64

// IndexRepairs is how many invalid index leftovers the runner has dropped in this process.
func IndexRepairs() int64 { return indexRepaired.Load() }

var reNoTxStatement = regexp.MustCompile(`(?is)^create\s+(?:unique\s+)?index\s+concurrently\s+if\s+not\s+exists\s+("?[A-Za-z_][A-Za-z0-9_]*"?)\s+on\s+\S`)

func isNoTx(body string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	return strings.TrimSpace(first) == noTxDirective
}

// noTxIndexName checks a no-transaction migration body and returns the index it creates.
func noTxIndexName(body string) (string, error) {
	var stmt []string
	for _, ln := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(ln); t != "" && !strings.HasPrefix(t, "--") {
			stmt = append(stmt, ln)
		}
	}
	sqlText := strings.TrimSpace(strings.Join(stmt, "\n"))
	if strings.ContainsAny(sqlText, "'$") || strings.Contains(sqlText, "/*") {
		return "", fmt.Errorf("a no-transaction migration may not contain a quote, a dollar quote or a block comment (it is checked, not parsed)")
	}
	if strings.Count(sqlText, ";") != 1 || !strings.HasSuffix(sqlText, ";") {
		return "", fmt.Errorf("a no-transaction migration is exactly one statement, ending in a semicolon")
	}
	m := reNoTxStatement.FindStringSubmatch(sqlText)
	if m == nil {
		return "", fmt.Errorf("a no-transaction migration is CREATE [UNIQUE] INDEX CONCURRENTLY IF NOT EXISTS <name> ON <table> ...;")
	}
	return strings.Trim(m[1], `"`), nil
}

// runNoTx applies one no-transaction migration (see above).
func runNoTx(ctx context.Context, db *sql.DB, m migration) error {
	started := time.Now()
	name, err := noTxIndexName(m.sql)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET lock_timeout = '%dms'", noTxLockTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("set lock_timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET statement_timeout = '%dms'", noTxStatementTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("set statement_timeout: %w", err)
	}
	defer func() {
		// Back to the pool's defaults, whatever happened (the connection goes back to the pool).
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "RESET lock_timeout")
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "RESET statement_timeout")
	}()

	var valid sql.NullBool
	err = conn.QueryRowContext(ctx, `SELECT i.indisvalid FROM pg_class c JOIN pg_index i ON i.indexrelid = c.oid
		WHERE c.relkind = 'i' AND c.relname = $1 AND c.relnamespace = 'public'::regnamespace`, name).Scan(&valid)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("look for a leftover index %s: %w", name, err)
	}
	if err == nil && valid.Valid && !valid.Bool {
		slog.Warn("dropping an invalid index left by a failed attempt, to create it again", "migration", m.id, "index", name)
		if _, err := conn.ExecContext(ctx, `DROP INDEX CONCURRENTLY IF EXISTS public.`+quoteIdent(name)); err != nil {
			return fmt.Errorf("drop the invalid index %s: %w", name, err)
		}
		indexRepaired.Add(1)
	}

	if _, err := conn.ExecContext(ctx, strings.TrimSpace(m.sql)); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO migrations (id, applied_at, note) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		m.id, time.Now().Unix(), firstSQLLine(strings.TrimSpace(m.sql)),
	); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	slog.Info("db migration applied", "id", m.id, "ms", time.Since(started).Milliseconds(), "no_transaction", true)
	return nil
}
