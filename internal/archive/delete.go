package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive/s3"
)

// DeleteWithin is the promise for queued archive deletions: the objects are
// gone within it (they are unreadable at once, the key going first). Pending
// rows older than it are reported overdue (an alert).
const DeleteWithin = 24 * time.Hour

// deletionKeep is how long a finished archive_deletions row is kept, so no
// user id lingers.
const deletionKeep = 30 * 24 * time.Hour

// Deleter removes the objects queued in archive_deletions (account deletion,
// history reset).
type Deleter struct {
	db    *sql.DB
	store *s3.Client
	env   string
	// ClockMargin is added to a request's time when picking the objects
	// written before it: MinIO's clock and Postgres's can differ a little,
	// and the margin errs toward deleting. Nothing new can be archived for
	// the user that soon (the reset removed their samples, and the archive
	// takes months 90 days old). Tests set 0.
	ClockMargin time.Duration
	// Report, when set, is told the run's totals.
	Report func(DeleteReport)
}

// DeleteReport is one deletion run.
type DeleteReport struct {
	Requests int // requests finished this run
	Objects  int // objects deleted
	Failed   int // requests that failed (retried next run)
	Overdue  int // pending requests older than DeleteWithin
}

// NewDeleter needs no KEK: it deletes ciphertext it never reads.
func NewDeleter(db *sql.DB, cfg Config) (*Deleter, error) {
	if db == nil {
		return nil, errors.New("archive: no database")
	}
	if !envName.MatchString(cfg.Env) {
		return nil, fmt.Errorf("archive: bad env %q", cfg.Env)
	}
	return &Deleter{
		db:    db,
		store: &s3.Client{Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey},
		env:         cfg.Env,
		ClockMargin: time.Minute,
	}, nil
}

// Run deletes the objects of pending requests, oldest first, and returns how
// many requests it finished. Each request deletes only objects written before
// it was made, so an archive written after a history reset (under the user's
// new key) stays. Replicas share the work (FOR UPDATE SKIP LOCKED). Finished
// rows older than 30 days are purged.
func (d *Deleter) Run(ctx context.Context) (int64, error) {
	var rep DeleteReport
	var firstErr error
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	rows, err := tx.QueryContext(ctx, `
		SELECT id, user_id, prefix, requested_at FROM archive_deletions
		 WHERE done_at IS NULL ORDER BY requested_at LIMIT 100
		   FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type request struct {
		id     int64
		user   uuid.UUID
		prefix string
		at     time.Time
	}
	var reqs []request
	for rows.Next() {
		var r request
		if err := rows.Scan(&r.id, &r.user, &r.prefix, &r.at); err != nil {
			rows.Close()
			return 0, err
		}
		reqs = append(reqs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range reqs {
		n, err := d.deletePrefix(ctx, r.user, r.prefix, r.at)
		rep.Objects += n
		if err != nil {
			rep.Failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("archive deletion %d: %w", r.id, err)
			}
			if _, uerr := tx.ExecContext(ctx, `UPDATE archive_deletions SET attempts = attempts + 1, last_error = $2 WHERE id = $1`, r.id, err.Error()); uerr != nil {
				return int64(rep.Requests), uerr
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE archive_deletions SET attempts = attempts + 1, last_error = NULL, done_at = NOW() WHERE id = $1`, r.id); err != nil {
			return int64(rep.Requests), err
		}
		rep.Requests++
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM archive_deletions WHERE done_at < NOW() - $1::interval`, fmt.Sprintf("%d seconds", int(deletionKeep.Seconds()))); err != nil {
		return int64(rep.Requests), err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM archive_deletions WHERE done_at IS NULL AND requested_at < NOW() - $1::interval`,
		fmt.Sprintf("%d seconds", int(DeleteWithin.Seconds()))).Scan(&rep.Overdue); err != nil {
		return int64(rep.Requests), err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if d.Report != nil {
		d.Report(rep)
	}
	return int64(rep.Requests), firstErr
}

// deletePrefix deletes the objects under the request's prefix that were last
// written before it was made. The prefix must be the user's own: a request
// can never reach another user's objects.
func (d *Deleter) deletePrefix(ctx context.Context, uid uuid.UUID, prefix string, before time.Time) (int, error) {
	full := d.env + "/" + prefix
	if full != UserPrefix(d.env, uid) {
		return 0, fmt.Errorf("prefix %q is not user %s's", prefix, uid)
	}
	cutoff := before.Add(d.ClockMargin)
	var keys []string
	if err := d.store.List(ctx, full, func(o s3.Object) error {
		if o.LastModified.IsZero() || o.LastModified.Before(cutoff) {
			keys = append(keys, o.Key)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	for i, k := range keys {
		if err := d.store.Delete(ctx, k); err != nil {
			return i, err
		}
	}
	return len(keys), nil
}

// Every runs fn every interval (the first run after a random part of it, so
// replicas don't start together) until ctx is done, logging failures.
func Every(ctx context.Context, interval time.Duration, name string, log *slog.Logger, fn func(context.Context) (int64, error)) {
	wait := time.Duration(rand.Int63n(int64(interval)))
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if n, err := fn(ctx); err != nil {
			log.Warn("archive job failed", "job", name, "done", n, "err", err.Error())
		}
		wait = interval
	}
}
