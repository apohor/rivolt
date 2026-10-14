package drain

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestStatThresholds(t *testing.T) {
	med := sql.NullFloat64{Float64: 3600, Valid: true}
	s := stat("rv", Window24h, 5.9, 1, 5.9, 1, 2, 1, med)
	if s.DrainPctPerDay != nil || s.IdleAwakeHPerDay != nil {
		t.Errorf("under 6 h must report no rates, got %+v", s)
	}
	if s.MedianSleep == nil || *s.MedianSleep != time.Hour || s.Sleeps != 2 || s.QuickRewakes != 1 {
		t.Errorf("counts/median = %+v", s)
	}
	s = stat("rv", Window24h, 12, 0.5, 20, 5, 0, 0, sql.NullFloat64{})
	if s.DrainPctPerDay == nil || *s.DrainPctPerDay != 1 {
		t.Errorf("drain = %v, want 1 %%/day", s.DrainPctPerDay)
	}
	if s.IdleAwakeHPerDay == nil || *s.IdleAwakeHPerDay != 6 {
		t.Errorf("awake = %v, want 6 h/day", s.IdleAwakeHPerDay)
	}
	if s.MedianSleep != nil {
		t.Errorf("no sleeps must mean no median")
	}
	if s = stat("rv", Window7d, 23, 1, 23, 1, 0, 0, sql.NullFloat64{}); s.DrainPctPerDay != nil || s.IdleAwakeHPerDay != nil {
		t.Errorf("7d needs 24 h, got %+v", s)
	}
}

func TestCollector(t *testing.T) {
	c := NewCollector()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	if n, err := testutil.GatherAndCount(reg); err != nil || n != 0 {
		t.Fatalf("before the first compute: %d series, %v; want none", n, err)
	}
	drainV, awakeV, med := 2.5, 3.0, 2*time.Hour
	c.Set([]Stat{
		{VehicleID: "rv-a", Window: Window24h, ParkedHours: 20, DrainPctPerDay: &drainV, ObservedHours: 22,
			IdleAwakeHPerDay: &awakeV, Sleeps: 3, QuickRewakes: 1, MedianSleep: &med},
		{VehicleID: "rv-a", Window: Window7d, ParkedHours: 10, ObservedHours: 10},
	}, time.Unix(1_800_000_000, 0), 1500*time.Millisecond)
	want := `
# HELP rivolt_vehicle_parked_drain_pct_per_day SoC lost per day while parked, over the window (absent with under 6 h / 24 h parked).
# TYPE rivolt_vehicle_parked_drain_pct_per_day gauge
rivolt_vehicle_parked_drain_pct_per_day{vehicle="rv-a",window="24h"} 2.5
# HELP rivolt_vehicle_quick_rewakes Sleeps that ended in the window after under 60 s (failed sleeps).
# TYPE rivolt_vehicle_quick_rewakes gauge
rivolt_vehicle_quick_rewakes{vehicle="rv-a",window="24h"} 1
rivolt_vehicle_quick_rewakes{vehicle="rv-a",window="7d"} 0
# HELP rivolt_vehicle_median_sleep_seconds Median length of the sleeps that ended in the window.
# TYPE rivolt_vehicle_median_sleep_seconds gauge
rivolt_vehicle_median_sleep_seconds{vehicle="rv-a",window="24h"} 7200
# HELP rivolt_drain_metrics_last_success_timestamp_seconds When the drain metrics were last computed.
# TYPE rivolt_drain_metrics_last_success_timestamp_seconds gauge
rivolt_drain_metrics_last_success_timestamp_seconds 1.8e+09
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"rivolt_vehicle_parked_drain_pct_per_day", "rivolt_vehicle_quick_rewakes",
		"rivolt_vehicle_median_sleep_seconds", "rivolt_drain_metrics_last_success_timestamp_seconds"); err != nil {
		t.Error(err)
	}
	// 24h: 7 series, 7d: 4 (no rates, no median), plus 2 job gauges.
	if n, _ := testutil.GatherAndCount(reg); n != 13 {
		t.Errorf("series = %d, want 13", n)
	}
	// A vehicle gone from the next snapshot loses its series.
	c.Set(nil, time.Unix(1_800_000_900, 0), time.Second)
	if n, _ := testutil.GatherAndCount(reg); n != 2 {
		t.Errorf("after an empty snapshot: %d series, want 2", n)
	}
}

func TestParseInterval(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		on   bool
		err  bool
	}{
		{"", DefaultInterval, true, false},
		{"off", 0, false, false},
		{" 0 ", 0, false, false},
		{"FALSE", 0, false, false},
		{"5m", 5 * time.Minute, true, false},
		{"10s", time.Minute, true, false},
		{"soon", DefaultInterval, true, true},
		{"-5m", DefaultInterval, true, true},
	} {
		d, on, err := ParseInterval(tc.in)
		if d != tc.want || on != tc.on || (err != nil) != tc.err {
			t.Errorf("ParseInterval(%q) = %v, %v, %v", tc.in, d, on, err)
		}
	}
}

func TestDedupe(t *testing.T) {
	got := dedupe([]row{{vid: "a", obs7: 10}, {vid: "b", obs7: 5}, {vid: "a", obs7: 30, park7: 1}, {vid: "a", obs7: 20}})
	if len(got) != 2 || got[0].vid != "a" || got[0].obs7 != 30 || got[0].park7 != 1 || got[1].vid != "b" {
		t.Errorf("dedupe = %+v", got)
	}
}
