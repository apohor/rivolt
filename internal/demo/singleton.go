package demo

import (
	"context"
	"database/sql"

	"github.com/apohor/rivolt/internal/singleton"
)

// onOneReplica runs pass only on the replica that takes the advisory lock named by job, so the
// demo loops re-seed once per tick across the cluster rather than once per pod. A replica that
// finds it held (or cannot reach the database) skips this tick; the passes are idempotent, so the
// next tick catches up whichever pod runs it.
func onOneReplica(ctx context.Context, d *sql.DB, job string, pass func()) {
	_, _ = singleton.Run(ctx, d, "demo_"+job, singleton.Key{Name: "rivolt-demo:" + job}, func(context.Context) error {
		pass()
		return nil
	})
}
