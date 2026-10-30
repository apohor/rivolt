package fence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// OK marks where a write statement takes the lease check, in its WHERE clause (or the WHERE of an
// INSERT … SELECT). Without a token in the context Exec replaces it with TRUE and the statement
// runs as it always did; with one it becomes the result of rivolt_lease_ok, evaluated once at the
// start of the statement.
const OK = "{{ok}}"

// Querier is what Exec runs on: a *sql.DB or a *sql.Tx.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Exec runs a data-modifying statement (INSERT, UPDATE or DELETE, without its own RETURNING) that
// carries OK where the lease check belongs, and returns the rows it affected.
//
// With no token in ctx it is the plain statement (OK is TRUE). With a token the statement is
// wrapped so the check and the write are one statement: rivolt_lease_ok is a MATERIALIZED CTE (one
// evaluation, at statement_timestamp()), and the statement says in its own result whether it was
// refused, so a refusal (ErrFenced) is told apart from a write that matched no row. The token's
// three values are appended after the caller's arguments.
//
// A guarded statement always runs bounded: on a *sql.DB it gets its own InTx (2 s cap, 2 s
// statement and 1 s idle-in-transaction timeouts); on a *sql.Tx the caller's transaction must
// already be one (InTx or BeginBounded). So one that passed the lease check and then waits on a
// lock is cancelled by the server, and an old owner's last commit lands within the bound the new
// owner's warm-up covers, whichever caller forgot.
func Exec(ctx context.Context, q Querier, stmt string, args ...any) (int64, error) {
	if !strings.Contains(stmt, OK) {
		return 0, fmt.Errorf("fence.Exec: statement has no %s placeholder", OK)
	}
	tok, guarded := From(ctx)
	if !guarded {
		res, err := q.ExecContext(ctx, strings.ReplaceAll(stmt, OK, "TRUE"), args...)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	if db, ok := q.(*sql.DB); ok {
		var count int64
		err := InTx(ctx, db, func(tctx context.Context, tx *sql.Tx) error {
			var err error
			count, err = Exec(tctx, tx, stmt, args...)
			return err
		})
		return count, err
	}
	n := len(args)
	wrapped := fmt.Sprintf(`WITH ok AS MATERIALIZED (SELECT rivolt_lease_ok($%d::uuid, $%d::text, $%d::bigint) AS ok),
	w AS (%s RETURNING 1)
	SELECT (SELECT ok FROM ok), (SELECT count(*) FROM w)`,
		n+1, n+2, n+3, strings.ReplaceAll(stmt, OK, "(SELECT ok FROM ok)"))
	all := append(append([]any(nil), args...), tok.Group, tok.Pod, tok.Epoch)
	var allowed bool
	var count int64
	if err := q.QueryRowContext(ctx, wrapped, all...).Scan(&allowed, &count); err != nil {
		return 0, err
	}
	if !allowed {
		return 0, ErrFenced
	}
	return count, nil
}
