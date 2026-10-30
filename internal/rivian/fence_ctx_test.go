package rivian

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

type fakeFence struct {
	tok fence.Token
	st  fence.State
}

func (f fakeFence) Fence(string) (fence.Token, fence.State) { return f.tok, f.st }

func TestFenceCtxStates(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 3}
	for _, c := range []struct {
		name      string
		src       fence.Source
		write     bool
		guarded   bool
		wantFence int
	}{
		{"no fence wired", nil, true, false, 0},
		{"lead mode off", fakeFence{st: fence.Off}, true, false, 0},
		{"held", fakeFence{tok: tok, st: fence.Held}, true, true, 0},
		{"warming", fakeFence{st: fence.Warming}, false, false, 0},
		{"lost", fakeFence{st: fence.Lost}, false, false, 1},
	} {
		m := &StateMonitor{}
		n := 0
		m.SetFence(c.src, func(string) { n++ })
		ctx, ok := m.fenceCtx(context.Background(), "veh", "samples")
		if ok != c.write {
			t.Errorf("%s: write = %v, want %v", c.name, ok, c.write)
		}
		got, has := fence.From(ctx)
		if has != c.guarded || (has && got != tok) {
			t.Errorf("%s: token in ctx = %v %+v", c.name, has, got)
		}
		if n != c.wantFence {
			t.Errorf("%s: fenced observer called %d times, want %d", c.name, n, c.wantFence)
		}
	}
}

// During the post-steal warm-up (and when the lease is lost) only the database write is held back:
// every frame still goes through the accumulators, so a charge loses no energy to a steal.
func TestFramesAdvanceTheChargeWhileWarming(t *testing.T) {
	at := time.Date(2026, 10, 30, 7, 0, 0, 0, time.UTC)
	for name, st := range map[string]fence.State{"warming": fence.Warming, "lost": fence.Lost, "held": fence.Held} {
		m := NewStateMonitor(nil, nil)
		m.SetFence(fakeFence{st: st, tok: fence.Token{Group: "g", Pod: "p", Epoch: 1}}, func(string) {})
		m.vehicleInfo["veh-w"] = &Vehicle{ID: "veh-w", Model: "R1T"}
		for minute := 0; minute <= 60; minute++ {
			m.applyVehicleStatePush(context.Background(), "veh-w", &State{VehicleID: "veh-w", At: at.Add(time.Duration(minute) * time.Minute),
				Gear: "P", ChargerState: "charging_active", ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: 7.5,
				BatteryLevelPct: 20 + float64(minute)/10, OdometerKm: 30000})
		}
		m.sessMu.Lock()
		kwh := m.liveChargeRow("veh-w", m.sessions["veh-w"].charge).EnergyAddedKWh
		m.sessMu.Unlock()
		if math.Abs(kwh-7.5) > 0.2 {
			t.Errorf("%s: %.2f kWh after an hour at 7.5 kW, want about 7.5", name, kwh)
		}
	}
}
