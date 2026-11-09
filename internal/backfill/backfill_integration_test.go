//go:build integration

package backfill_test

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/backfill"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// setup opens a database, builds a table of n rows whose v is NULL, and returns the pool.
func setup(t *testing.T, n int) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	for _, q := range []string{
		`DROP TABLE IF EXISTS bf_test`,
		`CREATE TABLE bf_test (id bigint PRIMARY KEY, v int, touched int NOT NULL DEFAULT 0)`,
		`INSERT INTO bf_test (id) SELECT g FROM generate_series(1, ` + strconv.Itoa(n) + `) g`,
		`DELETE FROM backfills`,
	} {
		if _, err := pool.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

// fillJob sets v = 1 on the rows after the cursor, in key order, counting every touch so a repeated row shows.
func fillJob(name string, fail *atomic.Int32) backfill.Job {
	return backfill.Job{
		Name: name, BatchSize: 500,
		Step: func(ctx context.Context, tx *sql.Tx, cursor string, limit int) (backfill.Result, error) {
			if fail != nil && fail.Add(-1) >= 0 {
				return backfill.Result{}, errors.New("boom")
			}
			after, _ := strconv.ParseInt(cursor, 10, 64)
			var last sql.NullInt64
			var n int
			err := tx.QueryRowContext(ctx, `
				WITH b AS (SELECT id FROM bf_test WHERE id > $1 AND v IS NULL ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED),
				u AS (UPDATE bf_test t SET v = 1, touched = touched + 1 FROM b WHERE t.id = b.id RETURNING t.id)
				SELECT count(*), max(id) FROM u`, after, limit).Scan(&n, &last)
			if err != nil {
				return backfill.Result{}, err
			}
			if n == 0 {
				return backfill.Result{Next: cursor, Done: true}, nil
			}
			return backfill.Result{Next: strconv.FormatInt(last.Int64, 10), Rows: n}, nil
		},
	}
}

func counts(t *testing.T, pool *sql.DB) (unfilled, repeated int) {
	t.Helper()
	if err := pool.QueryRow(`SELECT count(*) FILTER (WHERE v IS NULL), count(*) FILTER (WHERE touched > 1) FROM bf_test`).Scan(&unfilled, &repeated); err != nil {
		t.Fatal(err)
	}
	return
}

func progress(t *testing.T, pool *sql.DB, name string) (state string, rows int64) {
	t.Helper()
	if err := pool.QueryRow(`SELECT state, rows_done FROM backfills WHERE name = $1`, name).Scan(&state, &rows); err != nil {
		t.Fatal(err)
	}
	return
}

// A job runs to the end in batches, changes every row once, and its row says done with the count.
func TestJobRunsToTheEnd(t *testing.T) {
	pool := setup(t, 3000)
	r := &backfill.Runner{DB: pool, Jobs: []backfill.Job{fillJob("fill", nil)}, Pause: time.Millisecond}
	if ran, working, err := r.Pass(context.Background()); err != nil || !ran || working {
		t.Fatalf("pass: ran %v working %v err %v", ran, working, err)
	}
	if u, rep := counts(t, pool); u != 0 || rep != 0 {
		t.Errorf("unfilled %d repeated %d, want 0 0", u, rep)
	}
	if st, rows := progress(t, pool, "fill"); st != backfill.StateDone || rows != 3000 {
		t.Errorf("progress %s %d, want done 3000", st, rows)
	}
	// Another pass over a done job does nothing.
	if _, working, err := r.Pass(context.Background()); err != nil || working {
		t.Errorf("a done job is still working: %v %v", working, err)
	}
}

// A drain in the middle (the context ends after a few batches) loses nothing and repeats nothing: the next run resumes from the stored cursor.
func TestKilledMidRunResumes(t *testing.T) {
	pool := setup(t, 5000)
	var batches atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	job := fillJob("fill", nil)
	inner := job.Step
	job.Step = func(c context.Context, tx *sql.Tx, cur string, limit int) (backfill.Result, error) {
		res, err := inner(c, tx, cur, limit)
		if batches.Add(1) == 3 {
			cancel() // the pod drains right after the third batch is built; the batch's own commit may or may not land
		}
		return res, err
	}
	r := &backfill.Runner{DB: pool, Jobs: []backfill.Job{job}, Pause: time.Millisecond}
	_, _, _ = r.Pass(ctx)
	st, rows := progress(t, pool, "fill")
	if st == backfill.StateDone || rows >= 5000 {
		t.Fatalf("the run was not interrupted: %s %d", st, rows)
	}
	if u, _ := counts(t, pool); int64(u) != 5000-rows {
		t.Fatalf("progress says %d rows done but %d are unfilled: the data and the progress must move together", rows, u)
	}
	// The next pod picks up where it stopped.
	r2 := &backfill.Runner{DB: pool, Jobs: []backfill.Job{fillJob("fill", nil)}, Pause: time.Millisecond}
	if _, working, err := r2.Pass(context.Background()); err != nil || working {
		t.Fatalf("resume: working %v err %v", working, err)
	}
	if u, rep := counts(t, pool); u != 0 || rep != 0 {
		t.Errorf("after the resume unfilled %d repeated %d, want 0 0", u, rep)
	}
	if st, rows := progress(t, pool, "fill"); st != backfill.StateDone || rows != 5000 {
		t.Errorf("progress %s %d, want done 5000", st, rows)
	}
}

// Two pods racing: one holds the lock and runs the job, the other skips; the result is the same as one pod.
func TestTwoPodsOneRuns(t *testing.T) {
	pool := setup(t, 4000)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ranCount := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &backfill.Runner{DB: pool, Jobs: []backfill.Job{fillJob("fill", nil)}, Pause: 5 * time.Millisecond}
			if ran, _, err := r.Pass(context.Background()); err != nil {
				t.Error(err)
			} else if ran {
				mu.Lock()
				ranCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if u, rep := counts(t, pool); u != 0 || rep != 0 {
		t.Errorf("unfilled %d repeated %d, want 0 0", u, rep)
	}
	if ranCount < 1 || ranCount > 2 {
		t.Errorf("%d pods ran the pass", ranCount)
	}
	if st, rows := progress(t, pool, "fill"); st != backfill.StateDone || rows != 4000 {
		t.Errorf("progress %s %d", st, rows)
	}
}

// A failing batch rolls back with its progress, marks the job failed with the error, waits out the retry pause, and is retried to the end.
func TestFailureIsRecordedAndRetried(t *testing.T) {
	pool := setup(t, 1500)
	var fail atomic.Int32
	fail.Store(1)
	now := time.Now()
	r := &backfill.Runner{DB: pool, Jobs: []backfill.Job{fillJob("fill", &fail)}, Pause: time.Millisecond, FailedRetry: time.Minute,
		Now: func() time.Time { return now }}
	if _, working, err := r.Pass(context.Background()); err != nil || !working {
		t.Fatalf("first pass: working %v err %v", working, err)
	}
	var msg string
	if err := pool.QueryRow(`SELECT error FROM backfills WHERE name = 'fill'`).Scan(&msg); err != nil || msg != "boom" {
		t.Fatalf("recorded error %q (%v), want boom", msg, err)
	}
	if st, rows := progress(t, pool, "fill"); st != backfill.StateFailed || rows != 0 {
		t.Fatalf("progress %s %d, want failed 0", st, rows)
	}
	// Within the retry pause it is not tried again.
	if _, working, _ := r.Pass(context.Background()); !working {
		t.Fatal("a failed job inside its pause should still count as working")
	}
	if u, _ := counts(t, pool); u != 1500 {
		t.Fatalf("a failed job inside its pause touched rows: %d unfilled", u)
	}
	// After the pause it runs to the end.
	now = now.Add(2 * time.Minute)
	if _, working, err := r.Pass(context.Background()); err != nil || working {
		t.Fatalf("retry: working %v err %v", working, err)
	}
	if u, rep := counts(t, pool); u != 0 || rep != 0 {
		t.Errorf("after the retry unfilled %d repeated %d", u, rep)
	}
	if st, rows := progress(t, pool, "fill"); st != backfill.StateDone || rows != 1500 {
		t.Errorf("progress %s %d, want done 1500", st, rows)
	}
}

// A job's pause flag (a row in flags, backfill.<job>.paused) holds that job and no other, progress is kept, a malformed flag also pauses it, and
// clearing the flag lets it finish.
func TestPauseFlagHoldsOneJob(t *testing.T) {
	pool := setup(t, 2000)
	set := func(v string) {
		if _, err := pool.Exec(`INSERT INTO flags (name, value, updated_by) VALUES ('backfill.fill.paused', $1::jsonb, 'test')
			ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value`, v); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(`DELETE FROM flags WHERE name = 'backfill.fill.paused'`) })
	other := fillJob("other", nil)
	other.Step = func(ctx context.Context, tx *sql.Tx, cursor string, limit int) (backfill.Result, error) {
		return backfill.Result{Next: cursor, Done: true}, nil // a job with nothing to do: finishes at once
	}
	r := &backfill.Runner{DB: pool, Jobs: []backfill.Job{fillJob("fill", nil), other}, Pause: time.Millisecond}

	set(`{"paused": true}`)
	if _, working, err := r.Pass(context.Background()); err != nil || !working {
		t.Fatalf("paused pass: working %v err %v", working, err)
	}
	if u, _ := counts(t, pool); u != 2000 {
		t.Fatalf("a paused job changed rows: %d unfilled", u)
	}
	if st, _ := progress(t, pool, "other"); st != backfill.StateDone {
		t.Errorf("the other job was held by fill's flag: %s", st)
	}
	set(`"yes"`) // malformed: the safe reading is paused
	_, _, _ = r.Pass(context.Background())
	if u, _ := counts(t, pool); u != 2000 {
		t.Fatalf("a malformed flag did not pause the job: %d unfilled", u)
	}
	set(`{"paused": false}`)
	if _, working, err := r.Pass(context.Background()); err != nil || working {
		t.Fatalf("resumed pass: working %v err %v", working, err)
	}
	if u, rep := counts(t, pool); u != 0 || rep != 0 {
		t.Errorf("after the flag cleared: unfilled %d repeated %d", u, rep)
	}
}
