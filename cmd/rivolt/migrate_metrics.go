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
	m.Registry().MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "rivolt_migrate_lock_retries_total",
		Help: "Times a migration file was run again after a lock timeout or deadlock. Only pods that migrate themselves count; the Job logs each retry.",
	}, func() float64 { return float64(db.MigrateLockRetries()) }))
	m.Registry().MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "rivolt_migration_checksum_mismatch",
		Help: "Applied migrations whose recorded sha256 differs from the file this binary ships, at startup. Non-zero is a finding.",
	}, func() float64 { return float64(db.ChecksumMismatches()) }))
}
