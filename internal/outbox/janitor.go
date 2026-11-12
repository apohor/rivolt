package outbox

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/apohor/rivolt/internal/singleton"
)

// Retention, as the design fixes it: sent rows 7 days, failed rows 30 days. A pending row that nothing consumed within its kind's MaxAge plus PendingGrace is expired, so
// shadow rows written before any handler exists do not stay forever.
const (
	SentTTL   = 7 * 24 * time.Hour
	FailedTTL = 30 * 24 * time.Hour
)

// Janitor purges the outbox on a schedule, in limited batches, under a singleton lock.
type Janitor struct {
	store *Store
	db    *sql.DB

	Interval  time.Duration // between runs
	BatchSize int           // rows per DELETE
	MaxBatch  int           // batches per run (the next run continues)
	Pause     time.Duration // between batches

	purged  *prometheus.CounterVec
	pending *prometheus.GaugeVec
	oldest  prometheus.Gauge
}

// NewJanitor returns a janitor. reg may be nil (tests).
func NewJanitor(db *sql.DB, store *Store, reg prometheus.Registerer) *Janitor {
	j := &Janitor{
		store: store, db: db,
		Interval: time.Hour, BatchSize: 1000, MaxBatch: 50, Pause: 100 * time.Millisecond,
		purged: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "rivolt_outbox_purged_total",
			Help: "Outbox rows deleted by retention, by state (sent, failed, expired = pending and never consumed)."}, []string{"state"}),
		pending: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "rivolt_outbox_pending",
			Help: "Outbox rows neither sent nor failed, by kind, as of the last janitor tick on this pod."}, []string{"kind"}),
		oldest: prometheus.NewGauge(prometheus.GaugeOpts{Name: "rivolt_outbox_oldest_pending_seconds",
			Help: "Age of the oldest pending outbox row (0 when none), as of the last janitor tick on this pod."}),
	}
	for _, st := range []string{"sent", "failed", "expired"} {
		j.purged.WithLabelValues(st).Add(0)
	}
	for k := range kinds {
		j.pending.WithLabelValues(k).Set(0)
	}
	if reg != nil {
		reg.MustRegister(j.purged, j.pending, j.oldest)
	}
	return j
}

// Once runs one tick: the pending gauges on every pod, the purge on the pod that holds the singleton lock.
func (j *Janitor) Once(ctx context.Context) error {
	if err := j.observe(ctx); err != nil {
		slog.WarnContext(ctx, "outbox gauges failed", "err", err)
	}
	_, err := singleton.Run(ctx, j.db, "outbox_purge", singleton.Key{Name: "rivolt:outbox_purge"}, j.purge)
	return err
}

func (j *Janitor) observe(ctx context.Context) error {
	rows, err := j.db.QueryContext(ctx, `SELECT kind, count(*) FROM outbox WHERE sent_at IS NULL AND failed_at IS NULL GROUP BY kind`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var k string
		var n float64
		if err := rows.Scan(&k, &n); err != nil {
			return err
		}
		seen[k] = true
		j.pending.WithLabelValues(k).Set(n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for k := range kinds {
		if !seen[k] {
			j.pending.WithLabelValues(k).Set(0)
		}
	}
	var age float64
	if err := j.db.QueryRowContext(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0) FROM outbox WHERE sent_at IS NULL AND failed_at IS NULL`).Scan(&age); err != nil {
		return err
	}
	j.oldest.Set(age)
	return nil
}

func (j *Janitor) purge(ctx context.Context) error {
	for i := 0; i < j.MaxBatch; i++ {
		now := time.Now()
		p, err := j.store.PurgeBatch(ctx, now.Add(-SentTTL), now.Add(-FailedTTL), j.BatchSize)
		if err != nil {
			return err
		}
		j.purged.WithLabelValues("sent").Add(float64(p.Sent))
		j.purged.WithLabelValues("failed").Add(float64(p.Failed))
		j.purged.WithLabelValues("expired").Add(float64(p.Expired))
		if p.Total() < int64(j.BatchSize) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(j.Pause):
		}
	}
	return nil
}

// Run ticks until ctx ends, starting with one tick now.
func (j *Janitor) Run(ctx context.Context) {
	for {
		if err := j.Once(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "outbox purge failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(j.Interval):
		}
	}
}
