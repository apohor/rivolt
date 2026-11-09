// Package backfill moves data after a schema change, in the background (zero-impact deploys, G6b). A migration changes schema only: a table-wide
// UPDATE inside one holds row locks in a long transaction, bloats the table and holds up the migration job before every rollout (migrate_lint_test.go
// refuses one from migration 0110 on, unless the file carries "-- da-ack: backfill-in-migration <reason>").
//
// A Job is a named, idempotent step over a table by keyset: given the cursor it processes at most `limit` rows after it and says where it got to.
// The Runner runs jobs on one pod at a time (a Postgres advisory lock, internal/singleton), a batch at a time, each batch in its own short
// transaction together with the update of the job's row in `backfills`, so the data and the progress commit or roll back as one. A drain or a
// crash between batches loses nothing and repeats nothing; the next pod (or the next pass) resumes from the stored cursor.
//
// Rules for a job (DA's rulings; the review checks them):
//   - the new value is computed INSIDE the UPDATE statement from the row's own current columns (SET new = f(old) WHERE new IS DISTINCT FROM f(old)),
//     never read in Go and written back: a recorder update that lands between the read and the write would be overwritten with a stale value. On
//     the recorder's tables (drives, charges, vehicle_state, ...) a job writes only new or still-unfilled columns, never a column the recorder
//     keeps writing;
//   - idempotent (a second run over a row changes nothing), bounded (LIMIT; one batch finishes well inside the statement timeout) and key-ordered;
//   - never assumed done by readers: code that reads what a backfill fills treats the old shape as valid until `done`, and a "-- contract:" removal
//     of the old shape waits for it via contract-after;
//   - registered by a DA-acked PR that carries a row-count test on a fixture.
//
// It runs on the system pool (the one that serves sweeps, the monitor's writes and migrations), never the RLS request pool. A job is a maintenance
// job like the sample thinner: it touches every user's rows in key order and calls nothing outside Postgres, so it is not a group owner's write (the
// fence lint lists it as such).
//
// Pausing: RIVOLT_BACKFILL=0 stops every job (the runner is on by default, with an empty registry); a row in `flags` named backfill.<job>.paused
// with value {"paused": true} stops that one job, checked before every batch (a malformed row also pauses it: the safe reading). Progress is kept
// either way. Default pace: 1000 rows per batch and one batch per 10 seconds (about 3 hours per million rows), per job via BatchSize and Interval.
package backfill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/apohor/rivolt/internal/singleton"
)

// Result is what one batch did.
type Result struct {
	// Next is the cursor to resume from (the last key processed); unchanged when the batch found nothing.
	Next string
	// Rows is how many rows the batch changed.
	Rows int
	// Done: nothing is left after Next. A job that cannot tell returns Done when a batch changes no row.
	Done bool
}

// Job is one backfill. Name is permanent (it keys the progress row): never rename a job that has started.
type Job struct {
	Name string
	// BatchSize is the rows per batch; 0 is DefaultBatch. Interval is the wait between this job's batches; 0 is the runner's Pause.
	BatchSize int
	Interval  time.Duration
	// Step processes one batch inside tx (already bounded by lock_timeout and statement_timeout) after cursor ("" at the start).
	Step func(ctx context.Context, tx *sql.Tx, cursor string, limit int) (Result, error)
}

// Defaults of a Runner.
const (
	DefaultBatch        = 1000
	DefaultPause        = 10 * time.Second
	DefaultIdle         = 10 * time.Minute
	DefaultFailedRetry  = 15 * time.Minute
	defaultLockTimeout  = "5s"
	defaultStmtTimeout  = "30s"
	passProgressBackoff = time.Second // between passes while a job is still running
)

// States of a job's row.
const (
	StatePending = "pending"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

// Runner runs the jobs. The zero value of every field but DB and Jobs is usable.
type Runner struct {
	DB   *sql.DB
	Jobs []Job
	Log  *slog.Logger
	// Pause is the wait between batches when the job sets no Interval (DefaultPause); Idle the wait between passes when every job is done or failed (DefaultIdle);
	// FailedRetry how long a failed job waits before it is tried again (DefaultFailedRetry).
	Pause       time.Duration
	Idle        time.Duration
	FailedRetry time.Duration
	Metrics     *Metrics
	// Now and Sleep are for tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) {
	if r.Sleep != nil {
		r.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Run passes until ctx ends. A pass runs every job that is not done, on the pod that holds the lock (the others skip it and look again later).
func (r *Runner) Run(ctx context.Context) {
	if r == nil || r.DB == nil || len(r.Jobs) == 0 {
		return
	}
	for ctx.Err() == nil {
		ran, working, err := r.Pass(ctx)
		if err != nil && ctx.Err() == nil {
			r.log().Warn("backfill pass failed", "err", err.Error())
		}
		wait := orDefault(r.Idle, DefaultIdle)
		if ran && working {
			wait = passProgressBackoff
		}
		r.sleep(ctx, wait)
	}
}

// Pass runs one pass under the lock. ran is whether this pod held the lock; working whether some job still has work (it was running, or
// failed and will be retried).
func (r *Runner) Pass(ctx context.Context) (ran, working bool, err error) {
	ran, err = singleton.Run(ctx, r.DB, "backfill", singleton.Key{Name: "backfill"}, func(ctx context.Context) error {
		for _, j := range r.Jobs {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			st, err := r.runJob(ctx, j)
			if err != nil {
				return err
			}
			if st != StateDone {
				working = true
			}
		}
		return nil
	})
	return ran, working, err
}

type state struct {
	state     string
	cursor    string
	rowsDone  int64
	batchSize int
	updatedAt time.Time
	startedAt sql.NullTime
}

func (r *Runner) load(ctx context.Context, name string) (state, error) {
	if _, err := r.DB.ExecContext(ctx, `INSERT INTO backfills (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, name); err != nil {
		return state{}, err
	}
	var s state
	err := r.DB.QueryRowContext(ctx,
		`SELECT state, cursor, rows_done, batch_size, updated_at, started_at FROM backfills WHERE name = $1`, name).
		Scan(&s.state, &s.cursor, &s.rowsDone, &s.batchSize, &s.updatedAt, &s.startedAt)
	return s, err
}

// runJob runs job batches until it is done, fails, or ctx ends, and returns the state it left the job in.
func (r *Runner) runJob(ctx context.Context, j Job) (string, error) {
	s, err := r.load(ctx, j.Name)
	if err != nil {
		return "", err
	}
	r.Metrics.observeState(j.Name, s)
	switch s.state {
	case StateDone:
		return StateDone, nil
	case StateFailed:
		if r.now().Sub(s.updatedAt) < orDefault(r.FailedRetry, DefaultFailedRetry) {
			return StateFailed, nil
		}
	}
	limit := j.BatchSize
	if limit <= 0 {
		limit = s.batchSize
	}
	if limit <= 0 {
		limit = DefaultBatch
	}
	cursor := s.cursor
	for ctx.Err() == nil {
		if r.paused(ctx, j.Name) {
			r.Metrics.observePaused(j.Name, true)
			return StateRunning, nil // paused by its flag: progress kept, looked at again next pass
		}
		r.Metrics.observePaused(j.Name, false)
		started := r.now()
		res, err := r.batch(ctx, j, cursor, limit)
		if err != nil {
			if ctx.Err() != nil {
				return StateRunning, nil // a drain between or inside a batch: nothing committed, resume later
			}
			r.log().Warn("backfill batch failed", "job", j.Name, "cursor", cursor, "err", err.Error())
			r.fail(ctx, j.Name, err)
			return StateFailed, nil
		}
		r.Metrics.observeBatch(j.Name, res.Rows, r.now().Sub(started))
		cursor = res.Next
		if res.Done {
			r.log().Info("backfill done", "job", j.Name)
			return StateDone, nil
		}
		r.sleep(ctx, orDefault(j.Interval, orDefault(r.Pause, DefaultPause)))
	}
	return StateRunning, nil
}

// paused reads the job's pause flag (a row in flags named backfill.<job>.paused, value {"paused": true}). No row is not paused; a row that cannot
// be read as a boolean pauses the job (the safe side), and so does a database error reading it.
func (r *Runner) paused(ctx context.Context, name string) bool {
	var raw []byte
	err := r.DB.QueryRowContext(ctx, `SELECT value FROM flags WHERE name = $1`, "backfill."+name+".paused").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		if ctx.Err() == nil {
			r.log().Warn("backfill: could not read the pause flag; pausing the job", "job", name, "err", err.Error())
		}
		return true
	}
	var v struct {
		Paused *bool `json:"paused"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.Paused == nil {
		r.log().Warn("backfill: the pause flag is malformed; pausing the job", "job", name)
		return true
	}
	return *v.Paused
}

// batch runs one batch and records its progress in the same transaction.
func (r *Runner) batch(ctx context.Context, j Job, cursor string, limit int) (Result, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '`+defaultLockTimeout+`'`); err != nil {
		return Result{}, err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '`+defaultStmtTimeout+`'`); err != nil {
		return Result{}, err
	}
	res, err := j.Step(ctx, tx, cursor, limit)
	if err != nil {
		return Result{}, err
	}
	if res.Rows < 0 || (res.Next == "" && cursor != "" && !res.Done) {
		return Result{}, fmt.Errorf("job %s returned an unusable result %+v", j.Name, res)
	}
	next := res.Next
	if next == "" {
		next = cursor
	}
	st := StateRunning
	if res.Done {
		st = StateDone
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE backfills
		   SET state = $2, cursor = $3, rows_done = rows_done + $4, batch_size = $5, error = '',
		       started_at = COALESCE(started_at, now()), updated_at = now(),
		       done_at = CASE WHEN $2 = 'done' THEN now() ELSE NULL END
		 WHERE name = $1`, j.Name, st, next, res.Rows, limit)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	res.Next = next
	return res, nil
}

// fail records a failed batch on the job's row (outside the batch transaction, which rolled back).
func (r *Runner) fail(ctx context.Context, name string, cause error) {
	msg := cause.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	msg = strings.ToValidUTF8(msg, "")
	if _, err := r.DB.ExecContext(context.WithoutCancel(ctx), `
		UPDATE backfills SET state = 'failed', error = $2, started_at = COALESCE(started_at, now()), updated_at = now() WHERE name = $1`,
		name, msg); err != nil && !errors.Is(err, context.Canceled) {
		r.log().Warn("backfill: could not record a failure", "job", name, "err", err.Error())
	}
	r.Metrics.observeFailure(name)
}

// Metrics are the backfill series. Register Collectors with the metrics registry; a nil *Metrics ignores every call.
type Metrics struct {
	rows    *prometheus.CounterVec
	batch   *prometheus.HistogramVec
	failed  *prometheus.CounterVec
	done    *prometheus.GaugeVec
	rowsNow *prometheus.GaugeVec
	age     *prometheus.GaugeVec
	paused  *prometheus.GaugeVec
	Now     func() time.Time
}

// NewMetrics builds the series.
func NewMetrics() *Metrics {
	return &Metrics{
		rows:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "rivolt_backfill_rows_total", Help: "Rows changed by background backfill jobs."}, []string{"job"}),
		batch:   prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "rivolt_backfill_batch_seconds", Help: "Seconds one backfill batch took.", Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30}}, []string{"job"}),
		failed:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "rivolt_backfill_failures_total", Help: "Backfill batches that failed."}, []string{"job"}),
		done:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "rivolt_backfill_done", Help: "1 when the backfill job has finished, else 0."}, []string{"job"}),
		rowsNow: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "rivolt_backfill_rows_done", Help: "Rows a backfill job has changed so far (from its stored progress)."}, []string{"job"}),
		paused:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "rivolt_backfill_paused", Help: "1 while the job's pause flag (flags backfill.<job>.paused) holds it."}, []string{"job"}),
		age:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "rivolt_backfill_age_seconds", Help: "Seconds since a backfill job started, while it is not done (0 once done or before it starts); alert when it stays high."}, []string{"job"}),
	}
}

// Collectors are what the metrics registry registers.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.rows, m.batch, m.failed, m.done, m.rowsNow, m.age, m.paused}
}

func (m *Metrics) observeBatch(job string, rows int, d time.Duration) {
	if m == nil {
		return
	}
	m.rows.WithLabelValues(job).Add(float64(rows))
	m.batch.WithLabelValues(job).Observe(d.Seconds())
}

func (m *Metrics) observePaused(job string, paused bool) {
	if m == nil {
		return
	}
	v := 0.0
	if paused {
		v = 1
	}
	m.paused.WithLabelValues(job).Set(v)
}

func (m *Metrics) observeFailure(job string) {
	if m == nil {
		return
	}
	m.failed.WithLabelValues(job).Inc()
}

func (m *Metrics) observeState(job string, s state) {
	if m == nil {
		return
	}
	d := 0.0
	if s.state == StateDone {
		d = 1
	}
	m.done.WithLabelValues(job).Set(d)
	m.rowsNow.WithLabelValues(job).Set(float64(s.rowsDone))
	age := 0.0
	if s.state != StateDone && s.startedAt.Valid {
		now := time.Now
		if m.Now != nil {
			now = m.Now
		}
		age = now().Sub(s.startedAt.Time).Seconds()
	}
	m.age.WithLabelValues(job).Set(age)
}
