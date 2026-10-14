package drain

import (
	"context"
	"database/sql"
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
type Collector struct {
	snap atomic.Pointer[snapshot]

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

// Run recomputes now and then every interval until ctx is done. A
// failed recompute keeps the previous snapshot (its last-success
// timestamp ages, which is what to alert on) and logs a warning.
func (c *Collector) Run(ctx context.Context, db *sql.DB, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	refresh := func() {
		qctx, cancel := context.WithTimeout(ctx, computeTimeout)
		defer cancel()
		start := time.Now()
		stats, err := Compute(qctx, db, start)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("drain metrics: compute failed", "err", err.Error())
			}
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
