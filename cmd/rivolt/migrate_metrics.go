package main

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/metrics"
)

// registerMigrateMetrics exports how many invalid index leftovers this process's migrations have dropped and rebuilt
// (rivolt_migrate_index_repaired_total). A no-transaction migration that finds one logs a warning and counts it; the migrate Job is a separate
// process whose counter is not scraped, so there the log line is the record.
func registerMigrateMetrics(m *metrics.Metrics) {
	if m == nil {
		return
	}
	m.Registry().MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "rivolt_migrate_index_repaired_total",
		Help: "Invalid indexes left by a failed CREATE INDEX CONCURRENTLY that a no-transaction migration dropped to build again.",
	}, func() float64 { return float64(db.IndexRepairs()) }))
}
