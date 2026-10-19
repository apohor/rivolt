package samples

import (
	"errors"
	"fmt"
	"time"
)

// Which sample times the store accepts. vehicle_state is partitioned by month and
// writing a sample creates the month's partition, so a timestamp from the year 1
// or 9999 would create a partition for it (an import with scattered dates could
// create thousands, each taking a lock on the parent). Every writer goes through
// prepareBatch, which refuses samples outside the window and batches that span
// too many months; the SQL helper has the same bounds.
var (
	// WindowStart is the earliest sample time accepted: before Rivian's first
	// deliveries and before any tool that could export data.
	WindowStart = time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC)
	// WindowAhead is how far past now a sample may be dated (clock skew).
	WindowAhead = 24 * time.Hour
)

// MaxBatchMonths is the most distinct months one batch may span (WindowStart to
// now is about 100).
const MaxBatchMonths = 120

var (
	// ErrSampleOutOfWindow reports a sample dated before WindowStart or more than
	// WindowAhead after now.
	ErrSampleOutOfWindow = errors.New("samples: a sample is dated outside the accepted window")
	// ErrTooManyMonths reports a batch spanning more than MaxBatchMonths months.
	ErrTooManyMonths = errors.New("samples: batch spans too many months")
)

// TimeInWindow reports whether t is a sample time the store accepts at now.
func TimeInWindow(t, now time.Time) bool {
	return !t.Before(WindowStart) && !t.After(now.Add(WindowAhead))
}

// nowFn is the clock the window is judged against; tests replace it.
var nowFn = time.Now

// checkBatch validates a batch before anything is written: every time inside the
// window, and no more than MaxBatchMonths distinct months. It returns the set of
// months for the partition step.
func checkBatch(batch []Sample, now time.Time) (map[time.Time]struct{}, error) {
	months := map[time.Time]struct{}{}
	for _, v := range batch {
		if !TimeInWindow(v.At, now) {
			return nil, fmt.Errorf("%w (year %d)", ErrSampleOutOfWindow, v.At.UTC().Year())
		}
		u := v.At.UTC()
		months[time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)] = struct{}{}
		if len(months) > MaxBatchMonths {
			return nil, ErrTooManyMonths
		}
	}
	return months, nil
}
