package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/apohor/rivolt/internal/api"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/radar"
)

// newPlanRadar is the forecast radar the trip planner tags legs from, read from the in-cluster radar service at RIVOLT_RADAR_BASE_URL. It
// returns a nil interface (radar off) when the variable is empty, and an error when it is not an http(s) base URL.
func newPlanRadar(getenv func(string) string, m *metrics.Metrics) (api.RadarSource, error) {
	base := strings.TrimSpace(getenv("RIVOLT_RADAR_BASE_URL"))
	if base == "" {
		return nil, nil
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" {
		return nil, fmt.Errorf("RIVOLT_RADAR_BASE_URL is not an http(s) base URL")
	}
	st := radar.NewStore(base, nil)
	if m != nil {
		// The decoded grids a pod holds, to read against its memory limit (the cache is capped at radar.DefaultCacheBytes).
		m.Registry().MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "rivolt_radar_cache_bytes",
			Help: "Decoded forecast radar grids held in this pod's memory.",
		}, func() float64 { return float64(st.CachedBytes()) }))
	}
	return st, nil
}
