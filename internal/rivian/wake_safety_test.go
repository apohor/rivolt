package rivian

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// holdHarness is a monitor with a hand-driven cached power state and
// odometer, for exercising the wake-safety hold.
type holdHarness struct {
	t    *testing.T
	m    *StateMonitor
	logs *syncBuffer

	mu    sync.Mutex
	ws    WakeSafety
	rest  func() (*State, error)
	nREST int
}

func newHoldHarness(t *testing.T) *holdHarness {
	t.Helper()
	old := holdPollTick
	holdPollTick = time.Millisecond
	t.Cleanup(func() { holdPollTick = old })
	logs := &syncBuffer{}
	h := &holdHarness{t: t, logs: logs}
	h.m = &StateMonitor{
		logger: slog.New(slog.NewTextHandler(logs, nil)),
		cache:  map[string]*State{},
		stamp:  map[string]time.Time{},
	}
	h.ws = WakeSafety{HoldAsleep: true, HoldCheckEvery: time.Hour, HoldMax: time.Hour}
	h.m.SetWakeSafety(func(string) WakeSafety {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.ws
	})
	h.m.restFn = func(context.Context, string) (*State, error) {
		h.mu.Lock()
		h.nREST++
		fn := h.rest
		h.mu.Unlock()
		if fn == nil {
			return &State{PowerState: "sleep", OdometerKm: 1000}, nil
		}
		return fn()
	}
	h.set("ready", 1000)
	return h
}

func (h *holdHarness) set(ps string, odoKm float64) {
	h.m.mu.Lock()
	h.m.cache["veh-1"] = &State{PowerState: ps, OdometerKm: odoKm}
	h.m.mu.Unlock()
}

func (h *holdHarness) setWS(fn func(*WakeSafety)) {
	h.mu.Lock()
	fn(&h.ws)
	h.mu.Unlock()
}

func (h *holdHarness) holding() bool {
	h.m.mu.RLock()
	defer h.m.mu.RUnlock()
	return h.m.holdingVS["veh-1"]
}

// hold runs the hold hook in the background; the result arrives on
// the returned channel.
func (h *holdHarness) hold(ctx context.Context) <-chan error {
	out := make(chan error, 1)
	go func() {
		held, err := h.m.vehicleStateHold("veh-1")(ctx)
		if !held {
			err = errors.New("not held")
		}
		out <- err
	}()
	return out
}

func waitResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("hold never ended")
		return nil
	}
}

func stillHolding(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("hold ended early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestHoldNotEngaged(t *testing.T) {
	h := newHoldHarness(t)
	hook := h.m.vehicleStateHold("veh-1")
	if held, err := hook(context.Background()); held || err != nil {
		t.Errorf("awake car: held=%v err=%v; want an immediate reopen", held, err)
	}
	h.set("sleep", 1000)
	h.setWS(func(w *WakeSafety) { w.HoldAsleep = false })
	if held, err := hook(context.Background()); held || err != nil {
		t.Errorf("switch off: held=%v err=%v; want an immediate reopen", held, err)
	}
	h.m.SetWakeSafety(nil)
	if held, _ := hook(context.Background()); held {
		t.Error("no switch lookup must mean no hold")
	}
}

func TestHoldResumesOnWake(t *testing.T) {
	h := newHoldHarness(t)
	h.set("sleep", 1000)
	h.m.stamp["veh-1"] = time.Now().Add(-time.Hour)
	ch := h.hold(context.Background())
	stillHolding(t, ch)
	if !h.holding() {
		t.Error("holdingVS must be set while holding (the watchdog skips it)")
	}
	h.set("standby", 1000)
	if err := waitResult(t, ch); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if h.holding() {
		t.Error("holdingVS must clear after the hold")
	}
	if time.Since(h.m.stamp["veh-1"]) > time.Second {
		t.Error("the stamp must restart at the reopen so the watchdog measures from there")
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "holding vehicleState resubscribe while asleep") ||
		!strings.Contains(logs, `reason="power_state standby"`) {
		t.Errorf("logs:\n%s", logs)
	}
}

func TestHoldEndings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(h *holdHarness)
		during func(h *holdHarness)
		reason string
		warn   bool
	}{
		{name: "rest awake", setup: func(h *holdHarness) {
			n := 0
			h.ws.HoldCheckEvery = 5 * time.Millisecond
			h.rest = func() (*State, error) {
				if n++; n < 3 {
					return &State{PowerState: "sleep", OdometerKm: 1000}, nil
				}
				return &State{PowerState: "ready", OdometerKm: 1000}, nil
			}
		}, reason: `reason="rest ready" held=`},
		{name: "rest odometer moved", setup: func(h *holdHarness) {
			h.ws.HoldCheckEvery = 5 * time.Millisecond
			h.rest = func() (*State, error) { return &State{PowerState: "sleep", OdometerKm: 1004}, nil }
		}, reason: `reason="rest odometer moved"`, warn: true},
		{name: "cached odometer moved", during: func(h *holdHarness) { h.set("sleep", 1003) },
			reason: `reason="odometer moved"`, warn: true},
		{name: "rest error fails open", setup: func(h *holdHarness) {
			h.ws.HoldCheckEvery = 5 * time.Millisecond
			h.rest = func() (*State, error) { return nil, errors.New("gateway 503") }
		}, reason: `reason="rest check failed"`},
		{name: "max hold", setup: func(h *holdHarness) { h.ws.HoldMax = 20 * time.Millisecond },
			reason: `reason="max hold"`},
		{name: "switch off", during: func(h *holdHarness) {
			h.setWS(func(w *WakeSafety) { w.HoldAsleep = false })
		}, reason: `reason="switch off"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHoldHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			h.set("sleep", 1000)
			ch := h.hold(context.Background())
			if tc.during != nil {
				stillHolding(t, ch)
				tc.during(h)
			}
			if err := waitResult(t, ch); err != nil {
				t.Fatalf("hold: %v", err)
			}
			logs := h.logs.String()
			if !strings.Contains(logs, tc.reason) {
				t.Errorf("want %s in logs:\n%s", tc.reason, logs)
			}
			if got := strings.Contains(logs, "car moved while vehicleState was held"); got != tc.warn {
				t.Errorf("moved warning = %v, want %v:\n%s", got, tc.warn, logs)
			}
		})
	}
}

func TestHoldCancelled(t *testing.T) {
	h := newHoldHarness(t)
	h.set("sleep", 1000)
	ctx, cancel := context.WithCancel(context.Background())
	ch := h.hold(ctx)
	stillHolding(t, ch)
	cancel() // e.g. periodicRefresh's wake kick
	if err := waitResult(t, ch); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if h.holding() {
		t.Error("holdingVS must clear on cancel")
	}
}

// TestHoldAsleepSleepMicroCycles replays the failing car's pattern
// (prod 01-242521064, 2026-10-14: it falls asleep and is awake again
// 12-40 s later, over and over) through the real reconnect loop
// (subscribeLoop) with the hold installed. The fake session stands in
// for Rivian's ~3-min connection TTL; the test closes it at chosen
// points so every step is deterministic. The rule under test: a
// session the server closed while the car is asleep is not reopened
// until the car is seen awake (or a guardrail fires), and is reopened
// within a poll tick of that.
func TestHoldAsleepSleepMicroCycles(t *testing.T) {
	h := newHoldHarness(t)
	h.ws.HoldCheckEvery = time.Hour // only the cache wakes it, except step 5

	errTTL := errors.New("ws read: close 4420 Connection TTL expired")
	starts := make(chan string, 16) // power state seen at each session start
	closeNow := make(chan struct{})
	run := func(ctx context.Context) error {
		ps, _ := h.m.cachedPowerState("veh-1")
		starts <- ps
		select {
		case <-ctx.Done():
			return nil
		case <-closeNow:
			return errTTL
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- subscribeLoop(ctx, run, h.m.vehicleStateHold("veh-1"),
			func(time.Duration) <-chan time.Time { return time.After(time.Millisecond) })
	}()
	opened := func(want string) {
		t.Helper()
		select {
		case ps := <-starts:
			if ps != want {
				t.Fatalf("session opened with the car %q, want %q", ps, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no session opened (want one with the car %q)", want)
		}
	}
	noSession := func() {
		t.Helper()
		select {
		case ps := <-starts:
			t.Fatalf("a session opened while held (car %q)", ps)
		case <-time.After(40 * time.Millisecond):
		}
	}
	ttl := func() { closeNow <- struct{}{} }

	opened("ready")

	// 1. Awake: a TTL close reopens at once, as today.
	ttl()
	opened("ready")

	// 2-3. Micro-cycles: asleep at the TTL close → held; awake again
	// moments later → reopened.
	for _, woke := range []string{"standby", "ready", "standby"} {
		h.set("sleep", 1000)
		ttl()
		noSession()
		h.set(woke, 1000)
		opened(woke)
	}

	// 4. A long sleep across several would-be TTL closes: one hold, no
	// reopen at all until the car wakes.
	h.set("sleep", 1000)
	ttl()
	noSession()
	noSession()
	h.set("go", 1000)
	opened("go")

	// 5. Guardrail: the car drove and slept again inside the hold; the
	// REST check sees the odometer moved and reopens, loudly.
	h.setWS(func(w *WakeSafety) { w.HoldCheckEvery = 5 * time.Millisecond })
	h.mu.Lock()
	h.rest = func() (*State, error) { return &State{PowerState: "sleep", OdometerKm: 1010}, nil }
	h.mu.Unlock()
	h.set("sleep", 1000)
	ttl()
	opened("sleep")

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("loop ended with %v", err)
	}
	logs := h.logs.String()
	if n := strings.Count(logs, "holding vehicleState resubscribe while asleep"); n != 5 {
		t.Errorf("holds logged = %d, want 5 (every suppression logged):\n%s", n, logs)
	}
	if n := strings.Count(logs, "resuming vehicleState") + strings.Count(logs, "car moved while vehicleState was held"); n != 5 {
		t.Errorf("hold ends logged = %d, want 5:\n%s", n, logs)
	}
	if !strings.Contains(logs, "car moved while vehicleState was held") {
		t.Errorf("the moved-car guardrail must warn:\n%s", logs)
	}
}

func TestSubscribeLoopWithoutHold(t *testing.T) {
	// nil hold: TTL closes reopen; auth failure ends the loop.
	n := 0
	err := subscribeLoop(context.Background(), func(context.Context) error {
		if n++; n < 3 {
			return errors.New("close 4420")
		}
		return errWSUnauthenticated
	}, nil, func(time.Duration) <-chan time.Time { return time.After(time.Millisecond) })
	if !errors.Is(err, errWSUnauthenticated) || n != 3 {
		t.Errorf("err = %v after %d sessions; want auth failure after 3", err, n)
	}
}

func TestCaptureTopics(t *testing.T) {
	logs := &syncBuffer{}
	m := &StateMonitor{logger: slog.New(slog.NewTextHandler(logs, nil)), parallaxCapture: true}
	off := false
	m.SetWakeSafety(func(string) WakeSafety { return WakeSafety{CaptureOff: off} })
	topics := m.captureTopics("veh-1")
	if got := topics(); !slices.Equal(got, CaptureRVMs) {
		t.Errorf("switch off: %v, want CaptureRVMs", got)
	}
	off = true
	if got := topics(); got != nil {
		t.Errorf("switch on: %v, want none", got)
	}
	if got := topics(); got != nil {
		t.Errorf("still on: %v", got)
	}
	off = false
	if got := topics(); !slices.Equal(got, CaptureRVMs) {
		t.Errorf("switch back off: %v", got)
	}
	l := logs.String()
	if strings.Count(l, "capture topics off") != 1 || strings.Count(l, "capture topics back on") != 1 {
		t.Errorf("want one log per change:\n%s", l)
	}
	m.parallaxCapture = false
	if got := m.captureTopics("veh-1")(); got != nil {
		t.Errorf("capture flag off: %v, want none", got)
	}
}

// Switch (d), experiment A: no REST poll while the car reports asleep,
// on the treated vehicle only; every call and skip logged.
func TestRestQuietAsleep(t *testing.T) {
	logs := &syncBuffer{}
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(logs, nil)))
	calls := map[string]int{}
	m.restFn = func(_ context.Context, vid string) (*State, error) {
		calls[vid]++
		return &State{VehicleID: vid, PowerState: "sleep", OdometerKm: 1000}, nil
	}
	quiet := true
	m.SetWakeSafety(func(vid string) WakeSafety { return WakeSafety{RestQuiet: quiet && vid == "treated"} })
	set := func(vid, ps string) {
		m.mu.Lock()
		m.cache[vid] = &State{VehicleID: vid, PowerState: ps, OdometerKm: 1000}
		m.mu.Unlock()
	}
	ctx := context.Background()

	set("treated", "sleep")
	set("control", "sleep")
	m.refreshOnce(ctx, "treated")
	m.refreshOnce(ctx, "treated")
	m.refreshOnce(ctx, "control")
	if calls["treated"] != 0 || calls["control"] != 1 {
		t.Fatalf("asleep: calls = %v; want none on the treated vehicle, the control unchanged", calls)
	}
	l := logs.String()
	if !strings.Contains(l, `REST skipped while asleep" vehicle=treated reason=periodic_refresh skipped_this_sleep=2`) {
		t.Errorf("each skip logged with a per-sleep count:\n%s", l)
	}
	if !strings.Contains(l, `"rivian rest state" vehicle=control reason=periodic_refresh power_state=sleep`) {
		t.Errorf("every REST call logged with its reason:\n%s", l)
	}
	set("treated", "standby")
	m.refreshOnce(ctx, "treated")
	if calls["treated"] != 1 {
		t.Errorf("awake: %d REST calls, want 1", calls["treated"])
	}
	if !strings.Contains(logs.String(), "vehicle=treated reason=periodic_refresh power_state=standby skipped_before=2") {
		t.Errorf("the first call after a quiet sleep reports the skips:\n%s", logs.String())
	}
	quiet = false
	set("treated", "sleep")
	m.refreshOnce(ctx, "treated")
	if calls["treated"] != 2 {
		t.Errorf("switch off: %d REST calls, want 2 (unchanged behavior)", calls["treated"])
	}

	// The stale-charge confirm is a REST call too.
	quiet = true
	if ok, _ := m.confirmStillCharging(ctx, "treated"); ok || calls["treated"] != 2 {
		t.Errorf("stale-charge confirm while asleep: ok=%v calls=%d; want a skip", ok, calls["treated"])
	}
}

func TestWaitRefreshWakesEarlyUnderRestQuiet(t *testing.T) {
	old := restWakePoll
	restWakePoll = time.Millisecond
	t.Cleanup(func() { restWakePoll = old })
	logs := &syncBuffer{}
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(logs, nil)))
	quiet := true
	m.SetWakeSafety(func(string) WakeSafety { return WakeSafety{RestQuiet: quiet} })
	set := func(ps string) {
		m.mu.Lock()
		m.cache["veh-1"] = &State{PowerState: ps}
		m.mu.Unlock()
	}

	set("sleep")
	done := make(chan bool, 1)
	go func() { done <- m.waitRefresh(context.Background(), "veh-1", time.Hour) }()
	time.Sleep(20 * time.Millisecond)
	set("ready")
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("waitRefresh = false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a wake under rest_quiet must end the wait")
	}
	if !strings.Contains(logs.String(), "awake after a REST-quiet sleep, refreshing now") {
		t.Errorf("logs:\n%s", logs.String())
	}

	// Without the switch it's the plain timer (the control is unchanged).
	quiet = false
	set("sleep")
	start := time.Now()
	go func() { done <- m.waitRefresh(context.Background(), "veh-1", 80*time.Millisecond) }()
	time.Sleep(10 * time.Millisecond)
	set("ready")
	<-done
	if el := time.Since(start); el < 70*time.Millisecond {
		t.Errorf("switch off: waited %v, want the full interval", el)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m.waitRefresh(ctx, "veh-1", time.Hour) {
		t.Error("a cancelled context must end the wait with false")
	}
}

func TestRestQuietOverridesHold(t *testing.T) {
	h := newHoldHarness(t)
	h.setWS(func(w *WakeSafety) { w.RestQuiet = true })
	h.set("sleep", 1000)
	if held, err := h.m.vehicleStateHold("veh-1")(context.Background()); held || err != nil {
		t.Errorf("rest_quiet + hold_asleep: held=%v err=%v; want no hold (its checks are REST)", held, err)
	}
}
