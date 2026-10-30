package demo

import (
	"context"
	"database/sql"
)

// onOneReplica runs pass only on the replica that takes the advisory lock named by job, so the
// demo loops re-seed once per tick across the cluster rather than once per pod. A replica that
// finds it held (or cannot reach the database) skips this tick; the passes are idempotent, so the
// next tick catches up whichever pod runs it.
func onOneReplica(ctx context.Context, d *sql.DB, job string, pass func()) {
	conn, err := d.Conn(ctx)
	if err != nil {
		return
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, "rivolt-demo:"+job).Scan(&locked); err != nil || !locked {
		return
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, "rivolt-demo:"+job) //nolint:errcheck
	pass()
}
