package rivian

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestActionLogRingAndWindow(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	now := t0
	var l actionLog
	l.now = func() time.Time { return now }
	// A year of reconnect churn stays bounded.
	for i := 0; i < 100_000; i++ {
		now = now.Add(time.Second)
		l.note("veh-1", "subscribe", "VehicleState", "")
	}
	if n, c := len(l.byVehicle["veh-1"]), cap(l.byVehicle["veh-1"]); n != actionRing || c > 4*actionRing {
		t.Fatalf("ring len %d cap %d; want %d, bounded", n, c, actionRing)
	}
	now = t0.Add(time.Hour)
	l.note("", "request", "getUserInfo", "")
	now = now.Add(time.Second)
	l.note("veh-2", "request", "GetVehicleState", "")
	now = now.Add(time.Second)
	l.note("veh-1", "subscribe", "ParallaxMessages", "18 topics")

	got := l.around("veh-1", t0.Add(time.Hour), now)
	if len(got) != 2 || got[0].Op != "getUserInfo" || got[1].String() != "subscribe ParallaxMessages(18 topics)" {
		t.Errorf("around = %v; want the account call then veh-1's, not veh-2's", got)
	}
	if got := l.around("", t0, now); len(got) != 1 {
		t.Errorf("account-level only: %v", got)
	}
}

func TestVehicleOfAndTopics(t *testing.T) {
	cases := []struct {
		vars any
		want string
	}{
		{map[string]any{"vehicleID": "a"}, "a"},
		{map[string]any{"vehicleId": "b", "rvms": []string{"x"}}, "b"},
		{map[string]string{"id": "c"}, "c"},
		{map[string]any{"email": "someone@example.com"}, ""},
		{nil, ""},
		{struct{}{}, ""},
	}
	for _, tc := range cases {
		if got := vehicleOf(tc.vars); got != tc.want {
			t.Errorf("vehicleOf(%v) = %q, want %q", tc.vars, got, tc.want)
		}
	}
	if got := topicsDetail(map[string]any{"rvms": []string{"dynamics.vehicle.gnss"}}); got != "dynamics.vehicle.gnss" {
		t.Errorf("one topic: %q", got)
	}
	if got := topicsDetail(map[string]any{"rvms": CaptureRVMs}); got != "9 topics" {
		t.Errorf("many topics: %q", got)
	}
	if got := topicsDetail(map[string]any{"vehicleID": "a"}); got != "" {
		t.Errorf("no topics: %q", got)
	}
}

// The HTTP path notes every request, under its vehicle or the account.
func TestLiveClientNotesRequests(t *testing.T) {
	old := powerStateTimestamp
	powerStateTimestamp = true
	t.Cleanup(func() { powerStateTimestamp = old })
	g := newStubGateway(t)
	c := NewLive().WithEndpoint(g.srv.URL)
	ctx := context.Background()
	start := time.Now().Add(-time.Second)
	if err := c.Login(ctx, Credentials{Email: "a@b.c", Password: "pw"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	st, err := c.State(ctx, "veh-a")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if want := time.Date(2026, 4, 23, 17, 59, 0, 250e6, time.UTC); !st.PowerStateAt.Equal(want) {
		t.Errorf("PowerStateAt = %v, want %v", st.PowerStateAt, want)
	}
	if q, _ := g.capturedReqs[len(g.capturedReqs)-1].Body["query"].(string); !strings.Contains(q, "powerState { timeStamp value }") {
		t.Errorf("switch on: the query must ask for powerState's timeStamp:\n%s", q)
	}
	if _, err := c.State(withActionReason(ctx, "periodic_refresh"), "veh-a"); err != nil {
		t.Fatalf("State: %v", err)
	}
	var ops []string
	for _, a := range c.RecentActions("veh-a", start, time.Now()) {
		ops = append(ops, a.String())
	}
	got := strings.Join(ops, "; ")
	if !strings.Contains(got, "request Login") || !strings.HasSuffix(got, "request GetVehicleState; request GetVehicleState(periodic_refresh)") {
		t.Errorf("actions = %s", got)
	}
	for _, a := range c.RecentActions("veh-b", start, time.Now()) {
		if a.Op == "GetVehicleState" {
			t.Error("veh-a's request must not be attributed to veh-b")
		}
	}
}

func TestMergeStatePowerStateAt(t *testing.T) {
	t1 := time.Unix(1_800_000_000, 0)
	prev := &State{PowerState: "sleep", PowerStateAt: t1}
	if got := mergeState(prev, &State{BatteryLevelPct: 70}); !got.PowerStateAt.Equal(t1) {
		t.Errorf("a frame without powerState must keep its time, got %v", got.PowerStateAt)
	}
	t2 := t1.Add(time.Minute)
	if got := mergeState(prev, &State{PowerState: "standby", PowerStateAt: t2}); got.PowerState != "standby" || !got.PowerStateAt.Equal(t2) {
		t.Errorf("merged = %s %v", got.PowerState, got.PowerStateAt)
	}
	if got := mergeState(prev, &State{PowerState: "ready"}); !got.PowerStateAt.IsZero() {
		t.Errorf("a new state without a time must not inherit the old one, got %v", got.PowerStateAt)
	}
}

func TestLogWakeAttribution(t *testing.T) {
	wake := time.Date(2026, 10, 14, 21, 2, 4, 0, time.UTC)
	now := wake
	c := NewLive()
	c.actions.now = func() time.Time { return now }
	note := func(at time.Time, kind, op, detail string) {
		now = at
		c.noteAction("veh-1", kind, op, detail)
	}
	note(wake.Add(-40*time.Minute), "request", "GetVehicleState", "")
	note(wake.Add(-90*time.Second), "request", "GetVehicleState", "")
	note(wake.Add(-3*time.Second), "subscribe", "ParallaxMessages", "18 topics")
	note(wake.Add(1500*time.Millisecond), "subscribe", "VehicleState", "")

	run := func(carAt time.Time) string {
		logs := &syncBuffer{}
		m := &StateMonitor{client: c, logger: slog.New(slog.NewTextHandler(logs, nil)),
			cache: map[string]*State{"veh-1": {PowerState: "standby", PowerStateAt: carAt}}}
		m.logWakeAttribution("veh-1", wake.Add(-12*time.Second), wake.Add(2*time.Second), "standby", true)
		return logs.String()
	}

	// The car's own time anchors it: the Parallax subscribe 3 s before
	// is the last action; the vehicleState subscribe after is listed
	// as +1.5s (our reaction), and the 40-min-old call is out of the
	// 2-min window.
	got := run(wake)
	for _, want := range []string{"wake attribution", "wake_source=car", "failed=true", "slept=14s",
		`last_action="subscribe ParallaxMessages(18 topics)"`, "last_action_before=3s", "seen_after=2s",
		"request GetVehicleState -1m30s; subscribe ParallaxMessages(18 topics) -3s; subscribe VehicleState +1.5s"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %s in:\n%s", want, got)
		}
	}
	// No car time (or an implausible one): anchored on when we saw it.
	for _, carAt := range []time.Time{{}, wake.Add(-3 * time.Hour)} {
		got = run(carAt)
		if !strings.Contains(got, "wake_source=observed") || !strings.Contains(got, `last_action="subscribe VehicleState"`) {
			t.Errorf("carAt %v:\n%s", carAt, got)
		}
	}
	// No client (replay monitors): nothing logged, no panic.
	logs := &syncBuffer{}
	(&StateMonitor{logger: slog.New(slog.NewTextHandler(logs, nil))}).logWakeAttribution("veh-1", wake, wake, "ready", false)
	if logs.String() != "" {
		t.Errorf("logged without a client: %s", logs.String())
	}
}

// The first awake frame's arrival anchors the wake when we have it: a
// refresh that frame triggered half a second later is listed as our
// reaction (+), not as the last action before the wake, even though the
// gate only saw the wake on its next tick.
func TestLogWakeAttributionAnchorsOnTheFirstAwakeFrame(t *testing.T) {
	frame := time.Date(2026, 10, 15, 4, 10, 0, 0, time.UTC)
	now := frame
	c := NewLive()
	c.actions.now = func() time.Time { return now }
	note := func(at time.Time, kind, op, detail string) {
		now = at
		c.noteAction("veh-1", kind, op, detail)
	}
	note(frame.Add(-20*time.Minute), "subscribe", "VehicleState", "")
	note(frame.Add(500*time.Millisecond), "request", "GetVehicleState", "refresh")

	logs := &syncBuffer{}
	m := &StateMonitor{client: c, logger: slog.New(slog.NewTextHandler(logs, nil)),
		cache:  map[string]*State{"veh-1": {PowerState: "standby", PowerStateAt: frame.Add(-2 * time.Second)}},
		wokeAt: map[string]time.Time{"veh-1": frame}}
	m.logWakeAttribution("veh-1", frame.Add(-2*time.Minute), frame.Add(900*time.Millisecond), "standby", false)
	got := logs.String()
	for _, want := range []string{"wake_source=frame", "wake_at=2026-10-15T04:10:00Z", "seen_after=900ms",
		`last_action="subscribe VehicleState"`, "request GetVehicleState(refresh) +500ms",
		"car_wake_at=2026-10-15T04:09:58Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %s in:\n%s", want, got)
		}
	}

	// A stamp from an earlier wake (before this sleep was seen) is not
	// this wake's: fall back to the car's time.
	logs = &syncBuffer{}
	m.logger = slog.New(slog.NewTextHandler(logs, nil))
	m.wokeAt["veh-1"] = frame.Add(-3 * time.Minute)
	m.logWakeAttribution("veh-1", frame.Add(-2*time.Minute), frame.Add(900*time.Millisecond), "standby", false)
	if got := logs.String(); !strings.Contains(got, "wake_source=car") || strings.Contains(got, "car_wake_at") {
		t.Errorf("stale frame stamp used:\n%s", got)
	}
}

// Only a sleep → awake change stamps the frame time.
func TestPutCacheStampsWakeFrames(t *testing.T) {
	m := &StateMonitor{cache: map[string]*State{}}
	m.putCacheLocked("v", &State{PowerState: "sleep"})
	if _, ok := m.wokeAt["v"]; ok {
		t.Fatal("first frame (asleep) stamped a wake")
	}
	m.putCacheLocked("v", &State{PowerState: ""}) // not reported: no wake
	m.cache["v"] = &State{PowerState: "sleep"}
	m.putCacheLocked("v", &State{PowerState: "standby"})
	first, ok := m.wokeAt["v"]
	if !ok {
		t.Fatal("sleep → standby not stamped")
	}
	m.putCacheLocked("v", &State{PowerState: "ready"}) // already awake
	if m.wokeAt["v"] != first {
		t.Error("an awake → awake change moved the wake stamp")
	}
}

// The sleep gate emits the attribution line when a sleep ends.
func TestSleepGateLogsWakeAttribution(t *testing.T) {
	old := sleepGateTick
	sleepGateTick = time.Millisecond
	t.Cleanup(func() { sleepGateTick = old })
	logs := &syncBuffer{}
	c := NewLive()
	c.noteAction("veh-1", "subscribe", "VehicleState", "")
	m := &StateMonitor{client: c, logger: slog.New(slog.NewTextHandler(logs, nil)),
		cache: map[string]*State{"veh-1": {PowerState: "sleep"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.sleepGate(ctx, "veh-1", func(context.Context) {})
	set := func(ps string) {
		m.mu.Lock()
		m.putCacheLocked("veh-1", &State{PowerState: ps})
		m.mu.Unlock()
	}
	// Sleep, then wake, until the gate (ticking every millisecond, but
	// maybe not scheduled for a while on a loaded machine) has seen both.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "wake attribution") {
		if time.Now().After(deadline) {
			t.Fatalf("no wake attribution logged:\n%s", logs.String())
		}
		set("sleep")
		time.Sleep(20 * time.Millisecond)
		set("standby")
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), `last_action="subscribe VehicleState"`) || !strings.Contains(logs.String(), "wake_source=frame") {
		t.Errorf("logs:\n%s", logs.String())
	}
}

func TestPowerStateTimestampSwitch(t *testing.T) {
	old := powerStateTimestamp
	t.Cleanup(func() { powerStateTimestamp = old })
	powerStateTimestamp = false
	for _, q := range []string{qVehicleState, qVehicleStateSubscription} {
		if withPowerStateTS(q) != q || !strings.Contains(q, "powerState { value }") {
			t.Error("switch off must leave the queries exactly as they were")
		}
	}
	powerStateTimestamp = true
	for _, q := range []string{qVehicleState, qVehicleStateSubscription} {
		if got := withPowerStateTS(q); strings.Count(got, "powerState { timeStamp value }") != 1 {
			t.Errorf("switch on:\n%s", got)
		}
	}
}
