package rivian

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingObserver struct {
	mu     sync.Mutex
	failed []bool
}

func (o *recordingObserver) OnSleepEnded(_ string, _ time.Duration, failed bool) {
	o.mu.Lock()
	o.failed = append(o.failed, failed)
	o.mu.Unlock()
}

func (o *recordingObserver) outcomes() []bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]bool(nil), o.failed...)
}

// gateHarness runs sleepGate against a hand-driven power state and
// records every awake-worker context it launches.
type gateHarness struct {
	t    *testing.T
	m    *StateMonitor
	logs *syncBuffer
	obs  *recordingObserver

	mu   sync.Mutex
	ctxs []context.Context
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newGateHarness(t *testing.T, safe bool) *gateHarness {
	t.Helper()
	oldTick, oldMax := sleepGateTick, failedSleepMax
	sleepGateTick, failedSleepMax = 2*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { sleepGateTick, failedSleepMax = oldTick, oldMax })

	logs := &syncBuffer{}
	h := &gateHarness{
		t:    t,
		logs: logs,
		obs:  &recordingObserver{},
		m: &StateMonitor{
			logger: slog.New(slog.NewTextHandler(logs, nil)),
			cache:  map[string]*State{},
		},
	}
	h.m.SetSleepSafe(func(string) bool { return safe })
	h.m.SetSleepObserver(h.obs)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.setPower("ready")
	go h.m.sleepGate(ctx, "veh-1", func(wctx context.Context) {
		h.mu.Lock()
		h.ctxs = append(h.ctxs, wctx)
		h.mu.Unlock()
	})
	h.waitFor("first worker start", func() bool { return h.starts() == 1 })
	return h
}

func (h *gateHarness) setPower(ps string) {
	h.m.mu.Lock()
	h.m.cache["veh-1"] = &State{PowerState: ps}
	h.m.mu.Unlock()
}

func (h *gateHarness) starts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.ctxs)
}

func (h *gateHarness) latestRunning() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ctxs[len(h.ctxs)-1].Err() == nil
}

func (h *gateHarness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// A sleep-safe vehicle parks its workers when it sleeps and restarts
// them on the next non-sleep state.
func TestSleepGateParksAndResumes(t *testing.T) {
	h := newGateHarness(t, true)

	h.setPower("sleep")
	h.waitFor("workers parked", func() bool { return !h.latestRunning() })

	h.setPower("standby")
	h.waitFor("workers resumed", func() bool { return h.starts() == 2 && h.latestRunning() })
	if !strings.Contains(h.logs.String(), "sleep-safe: resumed subscriptions") {
		t.Errorf("missing resume log:\n%s", h.logs.String())
	}
}

// Without sleep-safe the gate never touches the workers, but still
// times sleeps: a short one is failed, a long one lasted.
func TestSleepGateObservesWithoutParking(t *testing.T) {
	h := newGateHarness(t, false)

	h.setPower("sleep")
	time.Sleep(20 * time.Millisecond)
	h.setPower("standby")
	h.waitFor("short sleep observed", func() bool { return len(h.obs.outcomes()) == 1 })

	h.setPower("sleep")
	time.Sleep(failedSleepMax + 50*time.Millisecond)
	h.setPower("ready")
	h.waitFor("long sleep observed", func() bool { return len(h.obs.outcomes()) == 2 })

	if got := h.obs.outcomes(); !got[0] || got[1] {
		t.Errorf("outcomes = %v, want [failed=true failed=false]", got)
	}
	if h.starts() != 1 || !h.latestRunning() {
		t.Errorf("workers touched without sleep-safe: starts=%d running=%v", h.starts(), h.latestRunning())
	}
}

// Three failed sleeps inside the window raise the can't-sleep warning
// exactly once.
func TestSleepGateWarnsOnSleepLoop(t *testing.T) {
	h := newGateHarness(t, false)
	for i := 0; i < sleepLoopThreshold+1; i++ {
		h.setPower("sleep")
		time.Sleep(10 * time.Millisecond)
		h.setPower("standby")
		want := i + 1
		h.waitFor("sleep observed", func() bool { return len(h.obs.outcomes()) == want })
	}
	if n := strings.Count(h.logs.String(), "vehicle can't stay asleep"); n != 1 {
		t.Errorf("warnings = %d, want 1:\n%s", n, h.logs.String())
	}
}
