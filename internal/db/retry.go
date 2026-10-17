package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// errPermanent marks a failure that retrying cannot fix (bad config).
var errPermanent = errors.New("permanent")

// OpenWithRetry is Open for the server's startup path: it retries the
// dial and ping with backoff for up to wait before giving up. A new pod
// can briefly have no route to Postgres (NetworkPolicy programming lags
// behind pod start), and exiting there turns a rollout into restarts and
// 502s. The server only starts listening after this returns, so
// readiness stays false until the database is reachable. Migrations run
// once, after the first successful connect, and are not retried.
func OpenWithRetry(ctx context.Context, dsn string, wait time.Duration, log *slog.Logger) (*sql.DB, error) {
	var d *sql.DB
	err := retryUntil(ctx, wait, time.Sleep, func(attempt int, err error, next time.Duration) {
		log.Info("postgres not reachable yet, retrying", "attempt", attempt, "err", err.Error(), "retry_in", next.String())
	}, func() error {
		var e error
		d, e = OpenNoMigrate(ctx, dsn)
		if e != nil && strings.TrimSpace(dsn) == "" {
			return fmt.Errorf("%w: %v", errPermanent, e)
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, d); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// retryUntil calls op until it succeeds, returns a permanent error, the
// budget is spent or ctx ends. Backoff doubles from 1 s up to 8 s.
func retryUntil(ctx context.Context, budget time.Duration, sleep func(time.Duration), onRetry func(attempt int, err error, next time.Duration), op func() error) error {
	var spent time.Duration
	delay := time.Second
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if errors.Is(err, errPermanent) || ctx.Err() != nil || spent+delay > budget {
			return err
		}
		onRetry(attempt, err, delay)
		sleep(delay)
		spent += delay
		if delay < 8*time.Second {
			delay *= 2
		}
	}
}
