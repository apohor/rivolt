package rivian

import (
	"context"
	"strings"
	"time"
)

// Wake-safety switches (battery-drain experiment, D2).
//
// Each switch narrows what Rivolt asks of Rivian's cloud while a car
// sleeps, so the experiment can tell which request keeps cars awake.
// All are off unless the wake_safety flag selects a vehicle; main
// adapts the flag into SetWakeSafety. sleep_safe_vehicles (sleep
// gate) is switch (b) and lives in sleep_gate.go.
//
//	(a) CaptureOff: the drive-dynamics Parallax subscription stops
//	    carrying the capture topics (CaptureRVMs). GPS, drive dynamics
//	    and the decoded base topics stay. Applied at the next reconnect,
//	    which Rivian's ~3-min connection TTL makes frequent.
//	(d) RestQuiet: periodicRefresh skips its REST GetVehicleState poll
//	    while the car reports asleep (experiment A: the only Rivolt
//	    request DA's D3 analysis couldn't exonerate, and the code notes
//	    it can bump some firmwares out of deep sleep). vehicleState
//	    stays subscribed, so a wake is still seen at once; REST resumes
//	    promptly on wake. Every REST call is logged with its reason
//	    (restState) and every skip with a per-sleep count. It overrides
//	    HoldAsleep for the same vehicle, whose wake checks are REST.
//	(c) HoldAsleep: when Rivian closes the vehicleState subscription
//	    (the ~3-min TTL) while the car reports asleep, don't reopen it.
//	    Reopen as soon as the cached state shows the car awake (any
//	    feed, or a periodicRefresh wake kick), a REST check every
//	    HoldCheckEvery finds it awake or its odometer moved, the switch
//	    goes off, or HoldMax passes. A REST error reopens at once (fail
//	    open). Every hold is logged at start and end.
//
// The guardrail for drives is the REST check. A drive that starts
// during a hold is picked up within HoldCheckEvery. A drive that both
// starts and ends between two checks is caught by the odometer
// comparison and logged loudly ("car moved while vehicleState was
// held"), so a missed drive can't go unnoticed.

// WakeSafety is one vehicle's switches. The zero value changes nothing.
type WakeSafety struct {
	CaptureOff     bool
	RestQuiet      bool
	HoldAsleep     bool
	HoldCheckEvery time.Duration
	HoldMax        time.Duration
}

// holdPollTick is how often a hold re-reads the cache and the switch.
// A var so tests can shrink it.
var holdPollTick = time.Second

// movedMi is how far the odometer may move during a hold before it
// counts as the car having driven (a REST odometer is whole-ish km).
const movedMi = 0.2

// SetWakeSafety installs the per-vehicle switch lookup (normally the
// flags store's wake_safety). nil turns every switch off.
func (m *StateMonitor) SetWakeSafety(fn func(vehicleID string) WakeSafety) {
	m.mu.Lock()
	m.wakeSafety = fn
	m.mu.Unlock()
}

func (m *StateMonitor) wakeSafetyFor(vehicleID string) WakeSafety {
	m.mu.RLock()
	fn := m.wakeSafety
	m.mu.RUnlock()
	if fn == nil {
		return WakeSafety{}
	}
	return fn(vehicleID)
}

// captureTopics returns the drive-dynamics extra-topics function:
// CaptureRVMs when the capture flag is on and switch (a) is off for the
// vehicle. It's called once per (re)connect and logs when the answer
// changes.
func (m *StateMonitor) captureTopics(vehicleID string) func() []string {
	first, lastOff := true, false
	return func() []string {
		if !m.parallaxCapture {
			return nil
		}
		off := m.wakeSafetyFor(vehicleID).CaptureOff
		if off != lastOff || (first && off) {
			if off {
				m.logger.Info("wake-safety: capture topics off", "vehicle", vehicleID, "topics", len(CaptureRVMs))
			} else {
				m.logger.Info("wake-safety: capture topics back on", "vehicle", vehicleID, "topics", len(CaptureRVMs))
			}
		}
		first, lastOff = false, off
		if off {
			return nil
		}
		return CaptureRVMs
	}
}

func (m *StateMonitor) cachedPowerState(vehicleID string) (ps string, odoKm float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if st := m.cache[vehicleID]; st != nil {
		return strings.ToLower(strings.TrimSpace(st.PowerState)), st.OdometerKm
	}
	return "", 0
}

// vehicleStateHold is switch (c) for one vehicle, as the
// SubscribeVehicleStateHeld hook.
func (m *StateMonitor) vehicleStateHold(vehicleID string) ReconnectHold {
	return func(ctx context.Context) (bool, error) {
		ws := m.wakeSafetyFor(vehicleID)
		// rest_quiet wins: the hold's wake checks are REST calls, the
		// very thing experiment A switches off, and without them a
		// held, silent vehicle would only be seen waking at hold_max.
		if !ws.HoldAsleep || ws.RestQuiet {
			return false, nil
		}
		if ps, _ := m.cachedPowerState(vehicleID); ps != "sleep" {
			return false, nil
		}
		return true, m.holdWhileAsleep(ctx, vehicleID, ws)
	}
}

// holdWhileAsleep blocks until vehicleState should be reopened (nil) or
// ctx ends (ctx's error).
func (m *StateMonitor) holdWhileAsleep(ctx context.Context, vehicleID string, ws WakeSafety) error {
	if ws.HoldCheckEvery <= 0 {
		ws.HoldCheckEvery = 5 * time.Minute
	}
	if ws.HoldMax <= 0 {
		ws.HoldMax = 30 * time.Minute
	}
	start := time.Now()
	_, odo0 := m.cachedPowerState(vehicleID)
	m.mu.Lock()
	if m.holdingVS == nil {
		m.holdingVS = map[string]bool{}
	}
	m.holdingVS[vehicleID] = true
	m.mu.Unlock()
	m.logger.Info("wake-safety: holding vehicleState resubscribe while asleep",
		"vehicle", vehicleID, "check_every", ws.HoldCheckEvery.String(), "max", ws.HoldMax.String())

	checks := 0
	reason, movedBy := "", 0.0
	end := func() {
		m.mu.Lock()
		delete(m.holdingVS, vehicleID)
		if m.stamp == nil {
			m.stamp = map[string]time.Time{}
		}
		// The watchdog measures from the reopen, not from the last
		// frame before the hold.
		m.stamp[vehicleID] = time.Now()
		m.mu.Unlock()
		attrs := []any{"vehicle", vehicleID, "reason", reason,
			"held", time.Since(start).Round(time.Second).String(), "rest_checks", checks}
		if movedBy > 0 {
			m.logger.Warn("wake-safety: car moved while vehicleState was held; drive start may be missing",
				append(attrs, "moved_mi", movedBy)...)
			return
		}
		m.logger.Info("wake-safety: resuming vehicleState", attrs...)
	}
	defer end()

	poll := time.NewTicker(holdPollTick)
	defer poll.Stop()
	check := time.NewTimer(ws.HoldCheckEvery)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			reason = "cancelled"
			return ctx.Err()
		case <-poll.C:
			ps, odo := m.cachedPowerState(vehicleID)
			switch {
			case ps != "sleep":
				reason = "power_state " + ps
			case odo0 > 0 && (odo-odo0)*kmToMi > movedMi:
				reason, movedBy = "odometer moved", (odo-odo0)*kmToMi
			case !m.wakeSafetyFor(vehicleID).HoldAsleep:
				reason = "switch off"
			case time.Since(start) >= ws.HoldMax:
				reason = "max hold"
			default:
				continue
			}
			return nil
		case <-check.C:
			checks++
			st, err := m.restState(ctx, vehicleID, "hold_check")
			if ctx.Err() != nil {
				reason = "cancelled"
				return ctx.Err()
			}
			if err != nil || st == nil {
				reason = "rest check failed"
				return nil
			}
			if ps := strings.ToLower(strings.TrimSpace(st.PowerState)); ps != "sleep" {
				reason = "rest " + ps
				return nil
			}
			if odo0 > 0 && st.OdometerKm > 0 && (st.OdometerKm-odo0)*kmToMi > movedMi {
				reason, movedBy = "rest odometer moved", (st.OdometerKm-odo0)*kmToMi
				return nil
			}
			check.Reset(ws.HoldCheckEvery)
		}
	}
}

// restQuietAsleep reports whether switch (d) suppresses a REST call
// (reason says which) for vehicleID right now. Each skip is logged with
// the count of skips in the current sleep; a REST call or a wake
// resets the count.
func (m *StateMonitor) restQuietAsleep(vehicleID, reason string) bool {
	if !m.wakeSafetyFor(vehicleID).RestQuiet {
		return false
	}
	if ps, _ := m.cachedPowerState(vehicleID); ps != "sleep" {
		return false
	}
	m.mu.Lock()
	if m.restSkips == nil {
		m.restSkips = map[string]int{}
	}
	m.restSkips[vehicleID]++
	n := m.restSkips[vehicleID]
	m.mu.Unlock()
	m.logger.Info("wake-safety: REST skipped while asleep",
		"vehicle", vehicleID, "reason", reason, "skipped_this_sleep", n)
	return true
}

// restState is every REST GetVehicleState call the monitor makes, each
// logged with why and the cached power state (experiment A evidence:
// these line up against wakes).
func (m *StateMonitor) restState(ctx context.Context, vehicleID, reason string) (*State, error) {
	ps, _ := m.cachedPowerState(vehicleID)
	m.mu.Lock()
	skipped := m.restSkips[vehicleID]
	delete(m.restSkips, vehicleID)
	m.mu.Unlock()
	attrs := []any{"vehicle", vehicleID, "reason", reason, "power_state", ps}
	if skipped > 0 {
		attrs = append(attrs, "skipped_before", skipped)
	}
	m.logger.Info("rivian rest state", attrs...)
	if m.restFn != nil {
		return m.restFn(ctx, vehicleID)
	}
	return m.client.State(ctx, vehicleID)
}

// restWakePoll is how often periodicRefresh's wait checks for a wake
// under rest_quiet. A var so tests can shrink it.
var restWakePoll = 2 * time.Second

// waitRefresh waits out one periodicRefresh interval. Under rest_quiet
// it ends early when the car goes from asleep to awake, so the first
// REST poll after a quiet sleep comes promptly instead of up to 30 min
// later (the interval was set while it slept). For other vehicles it's
// the plain timer it always was. False when ctx ended.
func (m *StateMonitor) waitRefresh(ctx context.Context, vehicleID string, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	poll := time.NewTicker(restWakePoll)
	defer poll.Stop()
	ps, _ := m.cachedPowerState(vehicleID)
	wasAsleep := ps == "sleep"
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-poll.C:
			ps, _ := m.cachedPowerState(vehicleID)
			if wasAsleep && ps != "sleep" && ps != "" && m.wakeSafetyFor(vehicleID).RestQuiet {
				m.mu.Lock()
				skipped := m.restSkips[vehicleID]
				m.mu.Unlock()
				m.logger.Info("wake-safety: awake after a REST-quiet sleep, refreshing now",
					"vehicle", vehicleID, "power_state", ps, "skipped_this_sleep", skipped)
				return true
			}
			wasAsleep = ps == "sleep"
		}
	}
}
