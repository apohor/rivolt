package main

import (
	"io/fs"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/web"
)

// installWebCarry layers the carry directory under the embedded bundle and exports what the build recorded about it:
// rivolt_web_carry_files (assets listed in its manifest) and rivolt_web_carry_missing_sources (sources the image build could not copy; the
// build stays green and flags it, and this is how the missing carry is seen after the fact). Without a carry directory nothing changes and
// both read 0.
func installWebCarry(base fs.FS, dir string, m *metrics.Metrics, logger *slog.Logger) fs.FS {
	out, info := web.WithCarry(base, dir)
	if m != nil {
		files := prometheus.NewGauge(prometheus.GaugeOpts{Name: "rivolt_web_carry_files", Help: "Hashed web assets of other builds this image carries so a rolling update does not 404 the chunks of the version a browser started on."})
		missing := prometheus.NewGauge(prometheus.GaugeOpts{Name: "rivolt_web_carry_missing_sources", Help: "Sources of carried web assets (main builds, the last two releases) the image build could not copy."})
		files.Set(float64(info.Files))
		missing.Set(float64(len(info.Missing)))
		m.Registry().MustRegister(files, missing)
	}
	if info.Files > 0 || len(info.Missing) > 0 {
		logger.Info("web asset carry", "files", info.Files, "missing_sources", info.Missing)
	}
	return out
}
