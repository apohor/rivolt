package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/weather"
)

// newWeatherClient is the one Open-Meteo client every weather caller shares (planner, outside temperature, trip advice, drive recaps).
// RIVOLT_WEATHER_BASE_URL points it at our own Open-Meteo (http or https, e.g. http://open-meteo.open-meteo.svc.cluster.local); that server keeps
// a few days of past hours and has no archive, so older times are skipped rather than sent. Unset (self-hosters, dev): the public API, which
// is free for non-commercial use only. An invalid value stops the server rather than silently using the public API.
func newWeatherClient(getenv func(string) string, m *metrics.Metrics) (*weather.Client, error) {
	var c *weather.Client
	if base := strings.TrimSpace(getenv("RIVOLT_WEATHER_BASE_URL")); base != "" {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" {
			return nil, fmt.Errorf("RIVOLT_WEATHER_BASE_URL is not an http(s) base URL")
		}
		c = weather.NewSelfHostedClient(strings.TrimRight(base, "/"))
	} else {
		c = weather.NewClient()
	}
	if m != nil && m.WeatherRequestsTotal != nil {
		c.Observe = func(endpoint, outcome string) { m.WeatherRequestsTotal.WithLabelValues(endpoint, outcome).Inc() }
		for _, ep := range []string{"forecast", "archive"} {
			for _, oc := range []string{"ok", "error", "skipped_old"} {
				m.WeatherRequestsTotal.WithLabelValues(ep, oc).Add(0)
			}
		}
	}
	return c, nil
}

// planElevationZoom is the DEM zoom the trip planner reads climb at: the in-cluster mirror holds z0-10 across North America, and z=12 only for
// Texas (the recorder's altitude).
const planElevationZoom = 10
