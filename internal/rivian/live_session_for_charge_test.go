package rivian

import (
	"context"
	"math"
	"testing"
	"time"
)

// The live-session endpoint serves a cached session only when it belongs to the open charge. The cache can hold the previous
// plug-in's session (a replayed Parallax snapshot with the whole earlier charge's total and its own power), which used to
// price a charge that had just begun at the earlier charge's cost.
func TestLiveSessionForChargeServesOnlyTheOpenChargesSession(t *testing.T) {
	ctx := context.Background()
	m := NewStateMonitor(nil, nil)
	m.vehicleInfo[chargeTestVehicle] = &Vehicle{ID: chargeTestVehicle, Model: "R1T", PackKWh: 100}
	at := time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)
	push := func(minute int, kw float64) {
		m.applyVehicleStatePush(ctx, chargeTestVehicle, &State{VehicleID: chargeTestVehicle, At: at.Add(time.Duration(minute) * time.Minute),
			Gear: "P", ChargerState: "charging_active", ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: kw,
			BatteryLevelPct: 45 + float64(minute)/20, OdometerKm: 30000})
	}
	// The previous plug-in's session, replayed: 85.4 kWh over 12 h, at 6.7 kW.
	stale := &LiveSession{VehicleID: chargeTestVehicle, Active: true, TotalChargedEnergyKWh: 85.4, PowerKW: 6.7, TimeElapsedSeconds: 722 * 60,
		StartTime: at.Add(-13 * time.Hour).Format(time.RFC3339Nano)}
	m.mu.Lock()
	m.lastSession[chargeTestVehicle] = stale
	m.mu.Unlock()

	// No charge is open yet: there is no session to serve.
	if sess, e := m.LiveSessionForCharge(chargeTestVehicle); sess != nil || e != 0 {
		t.Fatalf("before the charge opens: %+v, %.2f kWh, want nothing", sess, e)
	}

	// The charge opens; the stale session was never bonded to it. Nothing is served, and the energy is the charge's own.
	for minute := 0; minute <= 2; minute++ {
		push(minute, 7.6)
	}
	sess, e := m.LiveSessionForCharge(chargeTestVehicle)
	if sess != nil {
		t.Errorf("a session never bonded to this charge was served: %+v", sess)
	}
	if e <= 0 || e > 1 {
		t.Errorf("the charge's own energy after 2 minutes at 7.6 kW is %.3f kWh, want a few hundredths", e)
	}

	// Bonded to the charge but describing a much earlier start (it predates the charge): still not served.
	m.sessMu.Lock()
	id := m.sessions[chargeTestVehicle].charge.id
	m.sessMu.Unlock()
	m.mu.Lock()
	m.lastSessionFor[chargeTestVehicle] = id
	m.mu.Unlock()
	if sess, _ := m.LiveSessionForCharge(chargeTestVehicle); sess != nil {
		t.Errorf("a bonded session that predates the charge was served: %+v", sess)
	}

	// Bonded and this charge's own (started with it): served.
	fresh := &LiveSession{VehicleID: chargeTestVehicle, Active: true, TotalChargedEnergyKWh: 0.3, PowerKW: 7.6, TimeElapsedSeconds: 120,
		StartTime: at.Format(time.RFC3339Nano)}
	m.mu.Lock()
	m.lastSession[chargeTestVehicle] = fresh
	m.mu.Unlock()
	if sess, _ := m.LiveSessionForCharge(chargeTestVehicle); sess != fresh {
		t.Errorf("the open charge's own session was not served: %+v", sess)
	}

	// No charge for another vehicle: nothing.
	if sess, e := m.LiveSessionForCharge("other"); sess != nil || math.Abs(e) > 0 {
		t.Errorf("another vehicle: %+v %.2f", sess, e)
	}
}
