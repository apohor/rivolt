package rivian

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
)

// A home EVSE that flaps (active, station error, connecting, active within seconds) is still charging. Each error frame used to
// overwrite final_state, and the open-charge predicates only know the active states, so the session read as finished whenever the
// last frame was an error one.
func TestStationErrorFlapKeepsTheChargeOpen(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1_700_000_000, 0)
	c := &liveCharge{finalState: "charging_active"}
	for i, s := range []struct {
		at    time.Duration
		state string
	}{{5, "charging_station_error"}, {10, "charging_station_error"}, {12, "charging_connecting"}, {17, "charging_active"}} {
		c.noteState(t0.Add(s.at*time.Second), s.state)
		if !charges.IsOpenLiveFinalState(c.finalState) {
			t.Fatalf("frame %d (%s): final_state %q reads as finished", i, s.state, c.finalState)
		}
	}
	if !c.errSince.IsZero() && c.finalState != "charging_active" {
		t.Errorf("errSince %v, final %q", c.errSince, c.finalState)
	}
}

func TestStationErrorThatLastsBecomesTheFinalState(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1_700_000_000, 0)
	c := &liveCharge{finalState: "charging_active"}
	c.noteState(t0, "charging_station_error")
	c.noteState(t0.Add(stationErrorGrace-time.Second), "charging_station_error")
	if c.finalState != "charging_active" {
		t.Fatalf("inside the grace: %q", c.finalState)
	}
	c.noteState(t0.Add(stationErrorGrace), "charging_station_error")
	if c.finalState != "charging_station_error" {
		t.Fatalf("after the grace: %q", c.finalState)
	}
	// A recovery starts a fresh run.
	c.noteState(t0.Add(stationErrorGrace+time.Second), "charging_active")
	c.noteState(t0.Add(stationErrorGrace+2*time.Second), "charging_station_error")
	if c.finalState != "charging_active" {
		t.Fatalf("a new flap after recovery: %q", c.finalState)
	}
}

// The same flap through the real frame handler: one charge, open after every frame, and a run past the grace ends as the final state.
func TestStationErrorFlapThroughTheRecorder(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	frame := func(sec int, state string, soc float64) {
		f := chargingFrame(t0.Add(time.Duration(sec)*time.Second), soc)
		f.ChargerState = state
		s.handleChargeLifecycle(f, nil, m, ctx)
	}
	frame(0, "charging_active", 50)
	first := s.charge
	if first == nil {
		t.Fatal("no charge opened")
	}
	for i, fr := range []struct {
		sec   int
		state string
	}{{5, "charging_station_error"}, {10, "charging_station_error"}, {12, "charging_connecting"}, {17, "charging_active"}} {
		frame(fr.sec, fr.state, 50.1)
		if s.charge != first {
			t.Fatalf("frame %d (%s): a new charge replaced the open one", i, fr.state)
		}
		if !charges.IsOpenLiveFinalState(s.charge.finalState) {
			t.Fatalf("frame %d (%s): final_state %q reads as finished", i, fr.state, s.charge.finalState)
		}
	}
	base := 100
	for sec := 0; sec <= int(stationErrorGrace/time.Second)+5; sec += 5 {
		frame(base+sec, "charging_station_error", 50.2)
	}
	if s.charge != first || s.charge.finalState != "charging_station_error" {
		t.Fatalf("a run past the grace: same charge %v, final %q", s.charge == first, s.charge.finalState)
	}
}
