package main

import (
	"testing"

	"github.com/apohor/rivolt/internal/metrics"
)

func TestNewWeatherClient(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "RIVOLT_WEATHER_BASE_URL" {
				return v
			}
			return ""
		}
	}
	m := metrics.New()
	if c, err := newWeatherClient(env(""), m); err != nil || c.SelfHosted || c.ForecastURL != "https://api.open-meteo.com" {
		t.Errorf("unset: %+v, %v", c, err)
	}
	c, err := newWeatherClient(env(" http://open-meteo.open-meteo.svc.cluster.local/ "), m)
	if err != nil || !c.SelfHosted || c.ForecastURL != "http://open-meteo.open-meteo.svc.cluster.local" {
		t.Errorf("set: %+v, %v", c, err)
	}
	for _, bad := range []string{"open-meteo", "ftp://x", "http://", "http://u:p@x", "http://x?a=b"} {
		if _, err := newWeatherClient(env(bad), m); err == nil {
			t.Errorf("%q accepted: a typo must stop the server, not fall back to the public API", bad)
		}
	}
	// The counter reports through the registry.
	c.Observe("forecast", "ok")
}
