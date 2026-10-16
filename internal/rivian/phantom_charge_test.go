package rivian

import (
	"bytes"
	"context"
	"github.com/apohor/rivolt/internal/charges"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"
)

// Stale live-session totals must not become charges (phantom-charges.md):
// the charging-session feeds replay the previous session's snapshot,
// its total marked active, on every resubscribe.
func TestStaleSessionTotalsAreNotCredited(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 7, 13, 20, 7, 0, 0, time.UTC)
	ctx := context.Background()
	frame := func(sec int, cs string, kw, soc float64) State {
		return State{At: t0.Add(time.Duration(sec) * time.Second), Gear: "P", ChargerState: cs,
			ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: kw, BatteryLevelPct: soc,
			OdometerKm: 30000, Latitude: 34.16, Longitude: -118.17, PowerState: "ready"}
	}
	// push is a live-session frame as the charging subscriptions apply
	// it; elapsed is the session's own idea of how long it has run.
	push := func(r *Replayer, sec int, total, kw float64, elapsed int64) {
		r.m.applyLiveSession(ctx, r.vehicle, &LiveSession{At: t0.Add(time.Duration(sec) * time.Second), Active: true,
			TotalChargedEnergyKWh: total, PowerKW: kw, TimeElapsedSeconds: elapsed})
	}
	replay := func(r *Replayer, sec int, total, kw float64) { push(r, sec, total, kw, 10000) }
	openRow := func(r *Replayer) (energy, maxKW float64, open bool) {
		r.m.sessMu.Lock()
		s := r.m.sessions[r.vehicle]
		r.m.sessMu.Unlock()
		if s == nil || s.charge == nil {
			return 0, 0, false
		}
		row := r.m.liveChargeRow(r.vehicle, s.charge)
		return row.EnergyAddedKWh, row.MaxPowerKW, true
	}

	t.Run("scheduled, 0 kW, replayed 36.8 kWh: no charge at all", func(t *testing.T) {
		r := NewReplayer("v")
		for s := 0; s <= 2*3600; s += 30 {
			r.Feed(frame(s, "charging_scheduled", 0, 65.3))
			if s%180 == 0 {
				replay(r, s, 36.8, 0)
			}
		}
		if _, _, open := openRow(r); open || len(r.Charges()) != 0 {
			t.Errorf("a scheduled 0 kW wait recorded a charge (open %v, closed %d)", open, len(r.Charges()))
		}
	})

	t.Run("ready, 0 kW, replayed 36.8 kWh: no energy, no fake peak", func(t *testing.T) {
		r := NewReplayer("v")
		for s := 0; s <= 20*60; s += 30 {
			r.Feed(frame(s, "charging_ready", 0, 65.3))
			if s%180 == 0 {
				replay(r, s, 36.8, 0)
			}
		}
		e, kw, open := openRow(r)
		if !open || e != 0 || kw != 0 {
			t.Errorf("idle plugged session: open %v, %.1f kWh at %.0f kW; want 0 and 0", open, e, kw)
		}
		for _, c := range r.Charges() {
			if c.EnergyAddedKWh > 0.5 || c.MaxPowerKW > 1 {
				t.Errorf("closed idle session %s: %.1f kWh at %.0f kW", c.ID, c.EnergyAddedKWh, c.MaxPowerKW)
			}
		}
	})

	t.Run("a replay carrying a small stale power is capped", func(t *testing.T) {
		r := NewReplayer("v")
		for s := 0; s <= 20*60; s += 30 {
			r.Feed(frame(s, "charging_ready", 0, 65.3))
			if s%180 == 0 {
				replay(r, s, 36.8, 1.2)
			}
		}
		e, _, _ := openRow(r)
		if limit := 1.2 * (20.0 / 60) * 1.15; e > limit+1e-9 {
			t.Errorf("energy %.2f kWh, want ≤ %.2f (1.2 kW for 20 min)", e, limit)
		}
	})

	t.Run("genuine home L2: the session total is credited", func(t *testing.T) {
		r := NewReplayer("v")
		for s := 0; s <= 3600; s += 30 {
			r.Feed(frame(s, "charging_active", 7.2, 60+float64(s)/3600*5.5))
			if s%180 == 0 {
				push(r, s, 7.0*float64(s)/3600, 7.2, int64(s))
			}
		}
		if e, kw, _ := openRow(r); math.Abs(e-7.0) > 0.01 || kw != 7.2 {
			t.Errorf("L2: %.2f kWh at %.1f kW, want 7.0 at 7.2", e, kw)
		}
	})

	t.Run("a plug-in's running total from days ago is not this charge's", func(t *testing.T) {
		// Day 3 of a plug-in: a real 1.4 h session at 7 kW, but the
		// breakdown reports the plug-in's running total (1 kWh before
		// this session, inside the power cap's slack) and 75 h elapsed.
		r := NewReplayer("v")
		const pluggedFor = 75 * 3600
		for s := 0; s <= 5100; s += 30 {
			r.Feed(frame(s, "charging_active", 7.0, 60+float64(s)/5100*7))
			if s%180 == 0 {
				push(r, s, 1+7.0*float64(s)/3600, 7.0, int64(pluggedFor+s))
			}
		}
		e, _, _ := openRow(r)
		if want := 7.0 * 5100 / 3600; math.Abs(e-want) > 0.2 {
			t.Errorf("energy %.2f kWh, want the session's own ~%.2f, not the plug-in total", e, want)
		}
	})

	// One freshness rule for energy, price and network (#195): a
	// session that started well before the charge isn't its own, even
	// when it reports no elapsed time.
	startedAt := func(r *Replayer, sec int, total, kw float64, start time.Time) {
		r.m.applyLiveSession(ctx, r.vehicle, &LiveSession{At: t0.Add(time.Duration(sec) * time.Second), Active: true,
			TotalChargedEnergyKWh: total, PowerKW: kw, StartTime: start.Format(time.RFC3339)})
	}
	for _, tc := range []struct {
		name     string
		start    time.Duration
		credited bool
	}{
		// 0.8 kWh of the total predates the charge, inside the power cap.
		{"a session that started 2 h before the charge gives it no total", -2 * time.Hour, false},
		{"a session that started 2 min before the charge is its own", -2 * time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReplayer("v")
			for s := 0; s <= 3600; s += 30 {
				r.Feed(frame(s, "charging_active", 7.2, 60+float64(s)/3600*5.5))
				if s%180 == 0 {
					startedAt(r, s, 0.8+7.0*float64(s)/3600, 7.2, t0.Add(tc.start))
				}
			}
			e, _, _ := openRow(r)
			if credited := e > 7.5; credited != tc.credited { // 7.8 total vs 7.2 integral
				t.Errorf("energy %.2f kWh: session total credited %v, want %v", e, credited, tc.credited)
			}
		})
	}

	// Rivolt opened the charge late (a WS gap at plug-in, or an R2 that
	// opens only once power flows): a 3 h session whose start is 20 min
	// before the charge's is still its own; the start-time check has the
	// elapsed check's slack (15 min + age/4).
	t.Run("a long charge opened 20 min late keeps its session total", func(t *testing.T) {
		r := NewReplayer("v")
		for s := 0; s <= 3*3600; s += 60 {
			r.Feed(frame(s, "charging_active", 7.2, 40+float64(s)/3600*7))
			if s%300 == 0 {
				startedAt(r, s, 2.4+7.0*float64(s)/3600, 7.2, t0.Add(-20*time.Minute))
			}
		}
		if e, _, _ := openRow(r); e < 2.4+7.0*3-0.1 {
			t.Errorf("energy %.2f kWh: the late-opened charge lost its own session's total", e)
		}
	})

	// A StartTime without a zone doesn't parse as RFC3339: no evidence,
	// so the total is credited.
	t.Run("a zone-less StartTime is no evidence", func(t *testing.T) {
		r := NewReplayer("v")
		for s := 0; s <= 3600; s += 30 {
			r.Feed(frame(s, "charging_active", 7.2, 60+float64(s)/3600*5.5))
			if s%180 == 0 {
				r.m.applyLiveSession(ctx, r.vehicle, &LiveSession{At: t0.Add(time.Duration(s) * time.Second), Active: true,
					TotalChargedEnergyKWh: 0.8 + 7.0*float64(s)/3600, PowerKW: 7.2, StartTime: t0.Add(-2 * time.Hour).Format("2006-01-02T15:04:05")})
			}
		}
		if e, _, _ := openRow(r); e < 7.5 {
			t.Errorf("energy %.2f kWh: a zone-less StartTime was treated as stale", e)
		}
	})

	t.Run("genuine DC fast: the session total is credited", func(t *testing.T) {
		r := NewReplayer("v")
		for s := 0; s <= 1200; s += 10 {
			r.Feed(frame(s, "charging_active", 150, 20+float64(s)/1200*30))
			if s%60 == 0 {
				push(r, s, 40*float64(s)/1200, 150, int64(s))
			}
		}
		if e, _, _ := openRow(r); math.Abs(e-40) > 0.01 {
			t.Errorf("DCFC: %.2f kWh, want 40", e)
		}
	})
}

func TestScheduledIsNotCharging(t *testing.T) {
	if isChargingCS("charging_scheduled") || !isChargingCS("charging_active") || !isChargingCS("charging_ready") {
		t.Error("charging_scheduled must not count as charging; active and ready still do")
	}
}

// The close log carries what the freshness rule saw.
func TestLogChargeCloseStartDelta(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	start := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	c := &liveCharge{startedAt: start, endAt: start.Add(time.Hour)}
	sess := &LiveSession{StartTime: start.Add(-2 * time.Minute).Format(time.RFC3339), TimeElapsedSeconds: 3720}
	logChargeClose(logger, "v", charges.Charge{EnergyAddedKWh: 7, MaxPowerKW: 7.2}, sess, c)
	got := buf.String()
	for _, want := range []string{"ac session closed", "start_delta_s=-120", "elapsed_s=3720", "age_s=3600", "session_stale=false", "session_bonded=true"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %s in %s", want, got)
		}
	}
}
