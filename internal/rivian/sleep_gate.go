package rivian

import (
	"context"
	"strings"
	"time"
)

// Sleep gate.
//
// A Rivian is supposed to go standby → sleep after about a minute
// parked and stay there for hours. In Sep 2026 one R1S on prod was
// found doing the opposite: half of its sleeps since 2026-07-20 ended
// within 10-20 seconds, every time preceded by the car publishing a
// burst of fresh Parallax frames (climate_hold_status first, then
// cabin temp / closures / locks / ota.deployment.state). Parked drain
// roughly doubled. Five other trucks on the identical subscription set
// were unaffected, so whether Rivolt's live subscriptions keep that
// car awake could not be settled from the logs alone.
//
// The gate answers it experimentally and is the fix if the answer is
// yes. For a vehicle in sleep-safe mode (flags.SleepSafeName), every
// subscription except the vehicleState WebSocket is parked while the
// car reports "sleep": all Parallax topics, the charging-session
// subscriptions, the REST refresh and the live-session fetch. That is
// the pre-July 2026 setup, under which the same car slept ~6 h at a
// time. vehicleState stays up because it is how we learn the car woke;
// the moment it reports anything but "sleep" the parked workers are
// restarted, within one tick. Losing Parallax while asleep costs
// nothing: a sleeping car emits no fresh frames, only cloud replays of
// the last snapshot (confirmed 2026-07-18, see noteParallaxFrame).
//
// The gate also runs for every other vehicle as a pure observer: it
// times each sleep, logs "vehicle sleep ended" with the duration, and
// warns once an hour when a car keeps failing to stay asleep, so the
// next car with this problem shows up without a manual investigation.

// SleepObserver is told about every completed sleep: how long the car
// stayed asleep and whether that counts as a failed sleep. main wires
// it to Prometheus; keeping it an interface keeps metrics out of this
// package.
type SleepObserver interface {
	OnSleepEnded(vehicleID string, slept time.Duration, failed bool)
}

const (
	// sleepLoopThreshold failed sleeps within sleepLoopWindow is a car
	// that can't stay asleep, worth a warning.
	sleepLoopThreshold = 3
	sleepLoopWindow    = time.Hour
)

// Vars rather than consts so tests can shrink them.
var (
	// failedSleepMax: a sleep shorter than this ended on a wake nobody
	// asked for. Healthy sleeps last tens of minutes to hours; the
	// broken car's were 10-60 s.
	failedSleepMax = 60 * time.Second
	// sleepGateTick is how often the gate samples the cached power
	// state. The car's own wake follows sleep by ~10 s in the failure
	// case, so a 1 s tick parks subscriptions well inside that window.
	sleepGateTick = time.Second
)

// SetSleepSafe installs the per-vehicle sleep-safe check (normally
// the flags store's SleepSafe().Covers). nil disables gating; the
// gate still observes.
func (m *StateMonitor) SetSleepSafe(fn func(vehicleID string) bool) {
	m.mu.Lock()
	m.sleepSafe = fn
	m.mu.Unlock()
}

// SetSleepObserver installs the sleep metrics hook. nil is allowed.
func (m *StateMonitor) SetSleepObserver(o SleepObserver) {
	m.mu.Lock()
	m.sleepObserver = o
	m.mu.Unlock()
}

// sleepGate runs startAwake once, then parks (cancels) and restarts
// the workers it launched as the car falls asleep and wakes, for
// vehicles in sleep-safe mode. It returns when ctx is done.
//
// startAwake must start its workers on the context it is given and
// return; every worker must exit when that context is cancelled.
func (m *StateMonitor) sleepGate(ctx context.Context, vehicleID string, startAwake func(context.Context)) {
	var cancelAwake context.CancelFunc
	resume := func() {
		wctx, cancel := context.WithCancel(ctx)
		cancelAwake = cancel
		startAwake(wctx)
	}
	resume()
	defer func() {
		if cancelAwake != nil {
			cancelAwake()
		}
	}()

	var (
		parked       bool
		parkedAt     time.Time
		prevPS       string
		sleepAt      time.Time
		failures     []time.Time
		lastLoopWarn time.Time
	)
	t := time.NewTicker(sleepGateTick)
	defer t.Stop()
	for {
		var now time.Time
		select {
		case <-ctx.Done():
			return
		case now = <-t.C:
		}
		m.mu.RLock()
		var ps string
		if st := m.cache[vehicleID]; st != nil {
			ps = strings.ToLower(strings.TrimSpace(st.PowerState))
		}
		safeFn, obs := m.sleepSafe, m.sleepObserver
		m.mu.RUnlock()
		safe := safeFn != nil && safeFn(vehicleID)
		if ps == "" {
			// Not reported yet; neither a sleep nor a wake.
			continue
		}

		// Observe: time every sleep, whatever the gate does.
		if ps == "sleep" && prevPS != "sleep" {
			sleepAt = now
		}
		if prevPS == "sleep" && ps != "sleep" && !sleepAt.IsZero() {
			slept := now.Sub(sleepAt)
			failed := slept < failedSleepMax
			m.logger.Info("vehicle sleep ended",
				"vehicle", vehicleID, "slept", slept.Round(time.Second).String(),
				"woke_to", ps, "failed", failed, "sleep_safe", safe)
			if obs != nil {
				obs.OnSleepEnded(vehicleID, slept, failed)
			}
			if failed {
				failures = append(failures, now)
				for len(failures) > 0 && now.Sub(failures[0]) > sleepLoopWindow {
					failures = failures[1:]
				}
				if len(failures) >= sleepLoopThreshold && now.Sub(lastLoopWarn) > sleepLoopWindow {
					lastLoopWarn = now
					m.logger.Warn("vehicle can't stay asleep",
						"vehicle", vehicleID, "failed_sleeps_last_hour", len(failures),
						"sleep_safe", safe)
				}
			}
			sleepAt = time.Time{}
		}
		prevPS = ps

		// Gate: park everything but vehicleState while asleep.
		wantParked := safe && ps == "sleep"
		switch {
		case wantParked && !parked:
			cancelAwake()
			cancelAwake = nil
			parked, parkedAt = true, now
			m.logger.Info("sleep-safe: parked subscriptions while asleep", "vehicle", vehicleID)
		case !wantParked && parked:
			resume()
			parked = false
			m.logger.Info("sleep-safe: resumed subscriptions",
				"vehicle", vehicleID, "power_state", ps,
				"parked_for", now.Sub(parkedAt).Round(time.Second).String())
		}
	}
}
