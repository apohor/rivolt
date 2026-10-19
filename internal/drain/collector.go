package drain

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// DefaultInterval is how often Run recomputes. The windows are 24 h
// and 7 d, so a quarter hour of lag is invisible; one fleet query per
// interval is cheap.
const DefaultInterval = 15 * time.Minute

// computeTimeout bounds one recompute so a slow query can't pile up.
const computeTimeout = 2 * time.Minute

// Collector serves the last computed snapshot to Prometheus. Run
// refreshes it; scrapes never touch the database. Series for a vehicle
// with no rows in the last 7 days disappear on the next refresh.
//
// One replica (the holder of a Postgres advisory lock) computes and
// exports the fleet-wide series; the others export nothing, so a pod that
// lost the lock can't keep serving stale numbers. The 7-day fleet scan is
// the cost, and it would otherwise double with every replica. Failover is
// within one interval after the leader pod goes away. Query the series
// aggregated, max by (vehicle, window), as the parked-drain Grafana dashboard
// does; never sum them.
//
// The vehicle label is the Rivian vehicle id. That's fine for the
// admin-only Grafana these feed, but keep these series off any public
// or shared dashboard.
type Collector struct {
	snap atomic.Pointer[snapshot]
	// elect tries to become the computing replica. Run sets it for the database;
	// tests replace it.
	elect func(ctx context.Context) (session, error)

	drain, parked, awake, observed, sleeps, quick, median *prometheus.Desc
	lastOK, duration                                      *prometheus.Desc
}

type snapshot struct {
	stats []Stat
	at    time.Time
	took  time.Duration
}

// NewCollector returns a Collector with no data yet.
func NewCollector() *Collector {
	l := []string{"vehicle", "window"}
	return &Collector{
		drain: prometheus.NewDesc("rivolt_vehicle_parked_drain_pct_per_day",
			"SoC lost per day while parked, over the window (absent with under 6 h / 24 h parked).", l, nil),
		parked: prometheus.NewDesc("rivolt_vehicle_parked_hours",
			"Parked hours the drain rate is measured over, in the window.", l, nil),
		awake: prometheus.NewDesc("rivolt_vehicle_idle_awake_hours_per_day",
			"Hours awake while parked and not charging, per 24 h of recorded power state.", l, nil),
		observed: prometheus.NewDesc("rivolt_vehicle_power_state_observed_hours",
			"Hours with a recorded power state in the window (coverage of the awake rate).", l, nil),
		sleeps: prometheus.NewDesc("rivolt_vehicle_sleeps",
			"Sleeps that ended in the window.", l, nil),
		quick: prometheus.NewDesc("rivolt_vehicle_quick_rewakes",
			"Sleeps that ended in the window after under 60 s (failed sleeps).", l, nil),
		median: prometheus.NewDesc("rivolt_vehicle_median_sleep_seconds",
			"Median length of the sleeps that ended in the window.", l, nil),
		lastOK: prometheus.NewDesc("rivolt_drain_metrics_last_success_timestamp_seconds",
			"When the drain metrics were last computed.", nil, nil),
		duration: prometheus.NewDesc("rivolt_drain_metrics_compute_seconds",
			"How long the last drain metrics computation took.", nil, nil),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.drain, c.parked, c.awake, c.observed, c.sleeps, c.quick, c.median, c.lastOK, c.duration} {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	s := c.snap.Load()
	if s == nil {
		return
	}
	g := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	g(c.lastOK, float64(s.at.Unix()))
	g(c.duration, s.took.Seconds())
	for _, st := range s.stats {
		g(c.parked, st.ParkedHours, st.VehicleID, st.Window)
		g(c.observed, st.ObservedHours, st.VehicleID, st.Window)
		g(c.sleeps, float64(st.Sleeps), st.VehicleID, st.Window)
		g(c.quick, float64(st.QuickRewakes), st.VehicleID, st.Window)
		if st.DrainPctPerDay != nil {
			g(c.drain, *st.DrainPctPerDay, st.VehicleID, st.Window)
		}
		if st.IdleAwakeHPerDay != nil {
			g(c.awake, *st.IdleAwakeHPerDay, st.VehicleID, st.Window)
		}
		if st.MedianSleep != nil {
			g(c.median, st.MedianSleep.Seconds(), st.VehicleID, st.Window)
		}
	}
}

// Set replaces the snapshot. Run calls it; tests use it directly.
func (c *Collector) Set(stats []Stat, at time.Time, took time.Duration) {
	c.snap.Store(&snapshot{stats: stats, at: at, took: took})
}

// leaderLockKey names the Postgres advisory lock the computing replica holds
// (hashed to the lock id in SQL, so it can't collide with the numeric ids the
// maintenance jobs use).
const leaderLockKey = "rivolt-drain-metrics"

// session is a held leader lock and the connection it lives on. Computing on
// that same connection means a dead connection (and with it the lock) shows up
// as a compute error.
type session interface {
	compute(ctx context.Context, now time.Time) ([]Stat, error)
	// release gives the lock up. discard drops the connection instead of
	// returning it to the pool, which is what frees a lock that could not be
	// released cleanly.
	release(discard bool)
}

// dbSession is the session on a *sql.Conn.
type dbSession struct{ conn *sql.Conn }

func (s *dbSession) compute(ctx context.Context, now time.Time) ([]Stat, error) {
	var stats []Stat
	err := inWorkMem(ctx, s.conn, func(q Querier) error {
		var err error
		stats, err = Compute(ctx, q, now)
		return err
	})
	return stats, err
}

// computeWorkMem is the sort/hash memory for the fleet scan. At the default 4 MB
// the 7-day aggregate spills about 95 MB to temp files on every run (measured on
// prod, the main source of the database's temp writes); 64 MB keeps it in memory.
// It is set with SET LOCAL inside a read-only transaction, so it covers this one
// query and nothing else on the connection or the server.
const computeWorkMem = "64MB"

// inWorkMem runs fn inside a read-only transaction on conn with work_mem raised
// for that transaction only. The leader's advisory lock is session-level, so it
// is not affected by the transaction ending.
func inWorkMem(ctx context.Context, conn *sql.Conn, fn func(Querier) error) error {
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // read-only; a no-op after Commit
	if _, err := tx.ExecContext(ctx, `SET LOCAL work_mem = '`+computeWorkMem+`'`); err != nil {
		return fmt.Errorf("drain: set work_mem: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *dbSession) release(discard bool) {
	if !discard {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var ok bool
		err := s.conn.QueryRowContext(uctx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, leaderLockKey).Scan(&ok)
		cancel()
		discard = err != nil || !ok
	}
	if discard {
		// An advisory lock belongs to the database session, and closing a
		// *sql.Conn only returns it to the pool. Reporting the connection bad
		// makes the pool close the session, which frees the lock.
		_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = s.conn.Close()
}

func dbElect(db *sql.DB) func(ctx context.Context) (session, error) {
	return func(ctx context.Context) (session, error) {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		var got bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, leaderLockKey).Scan(&got); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			_ = conn.Close()
			return nil, err
		}
		if !got {
			_ = conn.Close() // no lock taken: back to the pool
			return nil, nil
		}
		return &dbSession{conn: conn}, nil
	}
}

// Run recomputes now and then every interval until ctx is done, while this
// replica holds the leader lock. Without it the snapshot is cleared (the
// replica exports no drain series) and the next tick tries again. A failed
// recompute keeps the previous snapshot (its last-success timestamp ages, which
// is what to alert on), gives the lock up with the connection, and retries on
// the next tick.
func (c *Collector) Run(ctx context.Context, db *sql.DB, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	elect := c.elect
	if elect == nil {
		elect = dbElect(db)
	}
	var lead session
	defer func() {
		if lead != nil {
			lead.release(false)
		}
	}()
	refresh := func() {
		if lead == nil {
			s, err := elect(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logger.Warn("drain metrics: leader election failed", "err", err.Error())
				}
				return
			}
			if s == nil {
				c.snap.Store(nil) // another replica computes
				return
			}
			lead = s
			logger.Info("drain metrics: this replica computes")
		}
		qctx, cancel := context.WithTimeout(ctx, computeTimeout)
		defer cancel()
		start := time.Now()
		stats, err := lead.compute(qctx, start)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("drain metrics: compute failed", "err", err.Error())
			}
			lead.release(true)
			lead = nil
			return
		}
		took := time.Since(start)
		c.Set(stats, start, took)
		logger.Debug("drain metrics: computed", "series", len(stats), "took", took.Round(time.Millisecond).String())
	}
	refresh()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}

// ParseInterval reads RIVOLT_DRAIN_METRICS_INTERVAL: empty means
// DefaultInterval; "0", "off" or "false" disable the metrics (ok =
// false); anything else is a Go duration, at least a minute. An
// unparsable value falls back to the default and returns the error
// for the caller to log.
func ParseInterval(v string) (d time.Duration, ok bool, err error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return DefaultInterval, true, nil
	case "0", "off", "false":
		return 0, false, nil
	}
	d, err = time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d <= 0 {
		if err == nil {
			err = fmt.Errorf("drain: interval %q must be positive", v)
		}
		return DefaultInterval, true, err
	}
	if d < time.Minute {
		d = time.Minute
	}
	return d, true, nil
}
