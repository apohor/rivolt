// Package retention runs the daily sweeps that delete personal data once it
// has outlived its purpose: expired sessions (IP and User-Agent) and waitlist
// requests.
package retention

import (
	"context"
	"log/slog"
	"math/rand"
	"time"
)

// Job is one sweep: it deletes what is past retention and reports how many
// rows it removed.
type Job struct {
	Name string
	Run  func(ctx context.Context) (int64, error)
}

// RunOnce runs every job, logging the count. A failing job is logged and does
// not stop the others.
func RunOnce(ctx context.Context, log *slog.Logger, jobs []Job) {
	for _, j := range jobs {
		n, err := j.Run(ctx)
		if err != nil {
			log.Warn("retention: sweep failed", "job", j.Name, "err", err.Error())
			continue
		}
		log.Info("retention: sweep done", "job", j.Name, "deleted", n)
	}
}

// Run sweeps once shortly after start (a random delay under 10 minutes so
// replicas spread out) and then every 24 hours until ctx ends. The deletes are
// idempotent, so two pods sweeping is harmless.
func Run(ctx context.Context, log *slog.Logger, jobs []Job) {
	run(ctx, log, jobs, func(d time.Duration) <-chan time.Time { return time.After(d) },
		time.Duration(rand.Int63n(int64(10*time.Minute))))
}

func run(ctx context.Context, log *slog.Logger, jobs []Job, after func(time.Duration) <-chan time.Time, first time.Duration) {
	wait := first
	for {
		select {
		case <-ctx.Done():
			return
		case <-after(wait):
		}
		RunOnce(ctx, log, jobs)
		wait = 24 * time.Hour
	}
}
