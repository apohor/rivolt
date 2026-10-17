package retention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRunOnceKeepsGoingAfterAFailingJob(t *testing.T) {
	var ran []string
	RunOnce(context.Background(), quiet, []Job{
		{"a", func(context.Context) (int64, error) { ran = append(ran, "a"); return 0, errors.New("boom") }},
		{"b", func(context.Context) (int64, error) { ran = append(ran, "b"); return 3, nil }},
	})
	if len(ran) != 2 {
		t.Fatalf("ran %v, want both jobs", ran)
	}
}

// The loop sweeps after the first delay, then every 24 hours, and stops with
// the context.
func TestRunSweepsDailyUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var waits []time.Duration
	sweeps := 0
	after := func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
	run(ctx, quiet, []Job{{"x", func(context.Context) (int64, error) {
		sweeps++
		if sweeps == 3 {
			cancel()
		}
		return 0, nil
	}}}, after, 5*time.Minute)
	if sweeps != 3 || waits[0] != 5*time.Minute || waits[1] != 24*time.Hour || waits[2] != 24*time.Hour {
		t.Errorf("sweeps %d waits %v", sweeps, waits)
	}
}
