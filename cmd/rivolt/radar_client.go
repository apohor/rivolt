package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/apohor/rivolt/internal/api"
	"github.com/apohor/rivolt/internal/radar"
)

// newPlanRadar is the forecast radar the trip planner tags legs from, read from the in-cluster radar service at RIVOLT_RADAR_BASE_URL. It
// returns a nil interface (radar off) when the variable is empty, and an error when it is not an http(s) base URL.
func newPlanRadar(getenv func(string) string) (api.RadarSource, error) {
	base := strings.TrimSpace(getenv("RIVOLT_RADAR_BASE_URL"))
	if base == "" {
		return nil, nil
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" {
		return nil, fmt.Errorf("RIVOLT_RADAR_BASE_URL is not an http(s) base URL")
	}
	return radar.NewStore(base, nil), nil
}
