package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// runMigrate is `rivolt migrate [--check]`: the one place that changes the schema in a deployment that runs migrations as a
// separate job (RIVOLT_MIGRATE_ON_START=false on the server). It connects with a retry (a fresh database may still be starting),
// takes the migration lock (waiting up to 5 minutes for another migrator), applies what is pending and exits. --check changes
// nothing and exits 1 when anything is pending. It logs migration ids and timings only: never SQL text or row data.
func runMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	check := fs.Bool("check", false, "report pending migrations and change nothing; exit 1 if any are pending")
	connectWait := fs.Duration("connect-wait", 5*time.Minute, "how long to keep retrying the database connection")
	timeout := fs.Duration("timeout", 15*time.Minute, "give up after this long overall")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	pool, err := db.ConnectWithRetry(ctx, postgresDSN(), *connectWait, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate: postgres:", err)
		return 1
	}
	defer pool.Close()

	if *check {
		st, err := db.CheckSchema(ctx, pool)
		if err != nil {
			fmt.Fprintln(os.Stderr, "migrate --check:", err)
			return 1
		}
		if len(st.Unknown) > 0 {
			logger.Warn("database is ahead of this binary", "unknown_migrations", len(st.Unknown))
		}
		if len(st.Pending) > 0 {
			fmt.Printf("%d pending: %v\n", len(st.Pending), st.Pending)
			return 1
		}
		fmt.Println("schema is current")
		return 0
	}
	started := time.Now()
	before, err := db.CheckSchema(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}
	if err := db.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}
	logger.Info("migrate done", "applied", len(before.Pending), "ms", time.Since(started).Milliseconds())
	return 0
}
