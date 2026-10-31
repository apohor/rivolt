package rivian

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

type wiringFence struct{ st fence.State }

func (f wiringFence) Fence(string) (fence.Token, fence.State) {
	return fence.Token{Group: "g", Pod: "p", Epoch: 7}, f.st
}

// A monitor the boot hydrate sweep started before main installed the lease store must be fenced once it is: it asks the registry
// at every check instead of keeping what the registry had when it was built. In lead mode, until then, it reads Lost (nothing is
// written, nothing is dialled), never Off.
func TestMonitorsStartedBeforeTheFenceIsInstalledAreFencedAfterwards(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		require bool
		before  fence.State
	}{
		{"lead mode off", false, fence.Off},
		{"lead mode on", true, fence.Lost},
	} {
		reg := NewMonitorRegistry(nil, nil, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		reg.RequireFence(c.require)
		m := NewStateMonitor(nil, nil)
		m.SetFence(registryFence{reg}, reg.fenced) // what Start does for every monitor it builds

		var counted []string
		if _, st := m.fenceState(ctx, "v", "drives"); st != c.before {
			t.Errorf("%s: before the lease store is installed the state is %v, want %v", c.name, st, c.before)
		}
		reg.SetFence(wiringFence{fence.Held}, func(kind string) { counted = append(counted, kind) }) // main, after the sweep
		fctx, st := m.fenceState(ctx, "v", "drives")
		if tok, ok := fence.From(fctx); st != fence.Held || !ok || tok.Epoch != 7 {
			t.Errorf("%s: after install: state %v, token %+v %v, want Held with the lease's token", c.name, st, tok, ok)
		}
		reg.SetFence(wiringFence{fence.Lost}, func(kind string) { counted = append(counted, kind) })
		if _, st := m.fenceState(ctx, "v", "charges"); st != fence.Lost || len(counted) != 1 || counted[0] != "charges" {
			t.Errorf("%s: a refused write on the already-running monitor: state %v, counted %v", c.name, st, counted)
		}
		if _, cancel, ok := m.rivianCtx(ctx, "v"); ok {
			cancel()
			t.Errorf("%s: a Rivian call went out for a group this pod does not hold", c.name)
		}
	}
}

// A registry setter called after the first monitor was built reaches only later monitors: it is logged as an error and counted so a
// wiring-order bug (the hydrate sweep runs in main before some setters) cannot pass silently. The forwarders are exempt.
func TestSettersAfterMonitorsStartedAreCounted(t *testing.T) {
	reg := NewMonitorRegistry(nil, nil, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg.SetRouteFiller(nil)
	reg.SetSleepSafe(nil)
	if n := reg.LateWirings(); n != 0 {
		t.Fatalf("before any monitor: %d late wirings", n)
	}
	reg.mu.Lock()
	reg.started = true // Start sets this when it builds a monitor
	reg.mu.Unlock()
	reg.SetFence(nil, nil)
	reg.RequireFence(true)
	reg.SetScheduleObserver(nil)
	if n := reg.LateWirings(); n != 0 {
		t.Fatalf("the forwarders are fine late: %d", n)
	}
	reg.SetRouteFiller(nil)
	reg.SetElevationLookup(nil)
	reg.AddDriveCloseHookFactory(nil)
	if n := reg.LateWirings(); n != 3 {
		t.Fatalf("three copied setters called late, counted %d", n)
	}
}

// main.go's order: the boot hydrate sweep builds a monitor for every user with a session, and a monitor copies most of what the
// registry holds at that moment. So every registry setter must come before the first monitorRegistry.Start in main.go, except the two
// that monitors read through the registry (SetFence, SetScheduleObserver). This reads the source text: main() is straight-line code,
// so text order is call order. It is what would have caught SetRouteFiller sitting after the sweep.
func TestMainWiresTheRegistryBeforeTheBootSweepStartsMonitors(t *testing.T) {
	src, err := os.ReadFile("../../cmd/rivolt/main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "monitorRegistry.Start(")
	if start < 0 {
		t.Fatal("no monitorRegistry.Start in main.go: the sweep moved; update this test")
	}
	forwarded := map[string]string{"SetFence": "", "SetScheduleObserver": ""}
	seen := 0
	for _, m := range regexp.MustCompile(`monitorRegistry\.((?:Set|Add|Require)\w+)\(`).FindAllStringSubmatchIndex(text, -1) {
		name := text[m[2]:m[3]]
		seen++
		if _, ok := forwarded[name]; ok {
			continue
		}
		if m[0] > start {
			line := 1 + strings.Count(text[:m[0]], "\n")
			t.Errorf("main.go:%d monitorRegistry.%s comes after the boot sweep's monitorRegistry.Start: monitors already running would not get it", line, name)
		}
	}
	if seen < 10 {
		t.Fatalf("found only %d registry setter calls in main.go: the pattern no longer matches", seen)
	}
	if i := strings.Index(text, "monitorRegistry.RequireFence("); i < 0 || i > start {
		t.Error("main.go must call monitorRegistry.RequireFence before the boot sweep (lead mode fails closed until the lease store is installed)")
	}
}

// Every fenced{kind} increment comes with its WARN, from one place, so the log and the counter cannot disagree; the WARN is limited
// to one per kind per vehicle per minute and carries the epoch the pod last held and the shortened vehicle id.
func TestRefusedOwnerActionsCountEveryTimeAndLogOncePerKindPerVehiclePerMinute(t *testing.T) {
	var buf strings.Builder
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(&buf, nil)))
	counted := map[string]int{}
	m.SetFence(wiringFence{fence.Held}, func(kind string) { counted[kind]++ })
	ctx := context.Background()
	m.fenceState(ctx, "rv-wiring-1234", "drives") // Held: the epoch is remembered, nothing refused
	m.SetFence(wiringFence{fence.Lost}, func(kind string) { counted[kind]++ })

	for i := 0; i < 5; i++ {
		m.fenceState(ctx, "rv-wiring-1234", "drives")
	}
	m.fenceState(ctx, "rv-wiring-1234", "charges")
	m.fenceState(ctx, "rv-wiring-9999", "drives")
	if counted["drives"] != 6 || counted["charges"] != 1 {
		t.Fatalf("counter = %v, want drives 6 (every refusal) and charges 1", counted)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d WARN lines for 7 refusals across 3 (kind, vehicle) pairs, want 3:\n%s", len(lines), buf.String())
	}
	first := lines[0]
	for _, want := range []string{"level=WARN", "kind=drives", "epoch_last_held=7", "vehicle=…1234"} {
		if !strings.Contains(first, want) {
			t.Errorf("first line lacks %q: %s", want, first)
		}
	}
	if strings.Contains(buf.String(), "rv-wiring-1234") {
		t.Error("the full vehicle id is in the log")
	}
	// A minute later the same pair logs again.
	m.refuseMu.Lock()
	for k := range m.refusedLog {
		m.refusedLog[k] = time.Now().Add(-2 * refuseLogEvery)
	}
	m.refuseMu.Unlock()
	m.fenceState(ctx, "rv-wiring-1234", "drives")
	if n := strings.Count(buf.String(), "kind=drives"); n != 3 {
		t.Errorf("after a minute: %d drives lines, want 3 (two vehicles + one again)", n)
	}
}

type registryDeadlineFence struct {
	st fence.State
	at time.Time
}

func (d registryDeadlineFence) Fence(string) (fence.Token, fence.State) {
	return fence.Token{Group: "g", Pod: "p", Epoch: 9}, d.st
}
func (d registryDeadlineFence) Deadline(string) (time.Time, bool) { return d.at, !d.at.IsZero() }

// The pause defence: every Rivian call's context carries the vehicle's local lease deadline, so on resume it is already done. A
// monitor built before the lease store is installed must still get it, which means the registry's fence forwards Deadline.
func TestRivianCtxKeepsTheLeaseDeadlineForMonitorsStartedBeforeTheFence(t *testing.T) {
	reg := NewMonitorRegistry(nil, nil, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg.RequireFence(true)
	m := NewStateMonitor(nil, nil)
	m.SetFence(registryFence{reg}, reg.fenced)
	var counted []string
	ctx := context.Background()

	if _, _, ok := m.rivianCtx(ctx, "v"); ok {
		t.Fatal("a Rivian call went out before the fence was installed in lead mode")
	}
	deadline := time.Now().Add(20 * time.Second).Round(0)
	reg.SetFence(registryDeadlineFence{fence.Held, deadline}, func(kind string) { counted = append(counted, kind) })
	dctx, cancel, ok := m.rivianCtx(ctx, "v")
	if !ok {
		t.Fatal("refused although the group is held and the deadline is ahead")
	}
	defer cancel()
	if got, has := dctx.Deadline(); !has || !got.Equal(deadline) {
		t.Fatalf("the call's deadline = %v (set %v), want the lease's local deadline", got, has)
	}

	reg.SetFence(registryDeadlineFence{fence.Held, time.Now().Add(-time.Second)}, func(kind string) { counted = append(counted, kind) })
	if _, _, ok := m.rivianCtx(ctx, "v"); ok || len(counted) != 1 || counted[0] != "rivian" {
		t.Fatalf("a passed deadline: ok=%v counted=%v, want refused and fenced{rivian} once", ok, counted)
	}

	// A source with no deadlines: the call is not bounded, as for the lease store with lead mode off.
	reg.SetFence(wiringFence{fence.Held}, nil)
	if dctx2, cancel2, ok := m.rivianCtx(ctx, "v"); !ok {
		t.Fatal("refused a held group whose source has no deadlines")
	} else {
		defer cancel2()
		if _, has := dctx2.Deadline(); has {
			t.Error("a deadline appeared from nowhere")
		}
	}
}
