package main

import (
	"testing"

	"github.com/apohor/rivolt/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewPlanRadar(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if r, err := newPlanRadar(env(""), nil); r != nil || err != nil {
		t.Fatalf("empty means off: %v %v", r, err)
	}
	for _, bad := range []string{"radar.radar.svc", "ftp://x", "http://u:p@x", "http://x?a=1", "http://"} {
		if _, err := newPlanRadar(env(bad), nil); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	m := metrics.New()
	if r, err := newPlanRadar(env("http://radar.radar.svc/"), m); r == nil || err != nil {
		t.Fatalf("valid URL: %v %v", r, err)
	}
	if n, err := testutil.GatherAndCount(m.Registry(), "rivolt_radar_cache_bytes"); err != nil || n != 1 {
		t.Fatalf("cache gauge: %d %v", n, err)
	}
}
