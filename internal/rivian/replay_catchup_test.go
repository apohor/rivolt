package rivian

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/samples"
)

// scriptedSource replays a fixed frame list; frames older than liveFrom
// are flagged catchingUp. after is called after each emit so the test
// can observe the monitor between frames.
type scriptedSource struct {
	frames   []State
	liveFrom int
	after    func(i int)
}

func (s *scriptedSource) Stream(ctx context.Context, vehicleID string, emit func(State, bool)) error {
	for i, f := range s.frames {
		if ctx.Err() != nil {
			return nil
		}
		emit(f, i < s.liveFrom)
		if s.after != nil {
			s.after(i)
		}
	}
	return nil
}

// memLiveState is a no-network LiveStateStore.
type memLiveState struct{}

func (memLiveState) Save(context.Context, string, LiveStateSnapshot, time.Duration) error { return nil }
func (memLiveState) Load(context.Context, string) (LiveStateSnapshot, bool, error) {
	return LiveStateSnapshot{}, false, nil
}
func (memLiveState) Delete(context.Context, string) error                          { return nil }
func (memLiveState) PutState(context.Context, string, *State, time.Duration) error { return nil }
func (memLiveState) GetState(context.Context, string) (*State, error)              { return nil, nil }

type sinkLog struct {
	mu          sync.Mutex
	batches     [][]samples.Sample
	driveWrites map[string]int
	lastDrive   map[string]time.Time
	snapshots   int
}

func (l *sinkLog) totalSamples() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, b := range l.batches {
		n += len(b)
	}
	return n
}

func newCatchupMonitor(src FrameSource) (*StateMonitor, *sinkLog) {
	m := NewSourceMonitor(src, []Vehicle{{ID: "demo-1", PackKWh: 130}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.SetLiveStateStore(memLiveState{})
	log := &sinkLog{driveWrites: map[string]int{}, lastDrive: map[string]time.Time{}}
	m.catchup.sinks = catchupSinks{
		insertSamples: func(_ context.Context, b []samples.Sample) error {
			log.mu.Lock()
			log.batches = append(log.batches, append([]samples.Sample(nil), b...))
			log.mu.Unlock()
			return nil
		},
		upsertDrive: func(_ context.Context, _ string, d *liveDrive) {
			log.mu.Lock()
			log.driveWrites[d.id]++
			log.lastDrive[d.id] = d.endAt
			log.mu.Unlock()
		},
		upsertCharge: func(context.Context, string, *liveCharge) {},
		saveSnapshot: func(string, LiveStateSnapshot) {
			log.mu.Lock()
			log.snapshots++
			log.mu.Unlock()
		},
	}
	return m, log
}

// frames builds a catch-up backlog: parkedN parked frames, then a
// driveN-frame drive, one second apart, starting at t0.
func catchupFrames(t0 time.Time, parkedN, driveN int) []State {
	var out []State
	odo := 1000.0
	for i := 0; i < parkedN; i++ {
		out = append(out, State{At: t0.Add(time.Duration(i) * time.Second), Gear: "P", BatteryLevelPct: 70,
			OdometerKm: odo, Latitude: 30.27, Longitude: -97.74, PowerState: "ready"})
	}
	for i := 0; i < driveN; i++ {
		odo += 0.02
		out = append(out, State{At: t0.Add(time.Duration(parkedN+i) * time.Second), Gear: "D", SpeedKph: 72,
			BatteryLevelPct: 70 - float64(i)/100, OdometerKm: odo,
			Latitude: 30.27 + float64(i)*0.0002, Longitude: -97.74, PowerState: "go"})
	}
	return out
}

func TestReplayCatchup_BatchesWritesAndFlushesBeforeLive(t *testing.T) {
	t0 := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	backlog := catchupFrames(t0, 1100, 120)
	live := backlog[len(backlog)-1]
	live.At = time.Now()
	src := &scriptedSource{frames: append(backlog, live), liveFrom: len(backlog)}
	m, log := newCatchupMonitor(src)

	var pendingAtLive = -1
	src.after = func(i int) {
		if i == len(backlog) {
			pendingAtLive = m.catchup.pendingSamples("demo-1")
		}
	}
	m.runSource(context.Background(), "demo-1")

	// Every backlog frame is a sample (1 s apart, above the 0.5 s floor),
	// written in ⌈n/500⌉-ish batches instead of one round trip each.
	if got := log.totalSamples(); got != len(backlog) {
		t.Errorf("samples flushed = %d, want %d", got, len(backlog))
	}
	if n := len(log.batches); n > len(backlog)/catchupFlushSamples+1 {
		t.Errorf("sample batches = %d, want ≤ %d", n, len(backlog)/catchupFlushSamples+1)
	}
	var prev time.Time
	for _, b := range log.batches {
		for _, s := range b {
			if !s.At.After(prev) {
				t.Fatalf("samples out of order: %s after %s", s.At, prev)
			}
			prev = s.At
		}
	}
	// The backlog is persisted before the first live frame's own writes.
	if pendingAtLive != 0 {
		t.Errorf("pending samples when the first live frame was recorded = %d, want 0", pendingAtLive)
	}
	// In-session drive rows: a handful of flushed upserts, not one per
	// frame, and the last one carries the drive's final end time.
	if len(log.driveWrites) != 1 {
		t.Fatalf("drive ids written = %v, want 1", log.driveWrites)
	}
	for id, n := range log.driveWrites {
		if n > 3 {
			t.Errorf("drive %s upserted %d times during catch-up, want ≤ 3", id, n)
		}
		if want := backlog[len(backlog)-1].At; !log.lastDrive[id].Equal(want) {
			t.Errorf("drive %s flushed endAt %s, want %s", id, log.lastDrive[id], want)
		}
	}
	// Snapshots: one per flush, not per frame.
	if log.snapshots == 0 || log.snapshots > len(log.batches)+1 {
		t.Errorf("snapshots = %d for %d flushes", log.snapshots, len(log.batches))
	}
}

func TestReplayCatchup_FlushesOnStreamEnd(t *testing.T) {
	t0 := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	backlog := catchupFrames(t0, 120, 0)
	src := &scriptedSource{frames: backlog, liveFrom: len(backlog)} // never goes live
	m, log := newCatchupMonitor(src)
	m.runSource(context.Background(), "demo-1")
	if got := log.totalSamples(); got != len(backlog) {
		t.Errorf("samples persisted at stream end = %d, want %d", got, len(backlog))
	}
	if m.catchup.pendingSamples("demo-1") != 0 {
		t.Error("buffer not drained at stream end")
	}
}

func TestReplayCatchup_WriteThroughSupersedesBufferedRow(t *testing.T) {
	m, log := newCatchupMonitor(&scriptedSource{})
	m.quietHooks.Store(true)
	d := &liveDrive{id: "d1", startedAt: time.Now(), endAt: time.Now().Add(time.Minute)}
	m.touchLiveDrive(context.Background(), "demo-1", d)
	// A close (write-through) persists a newer row; the buffered one must
	// not be replayed over it at the next flush.
	m.upsertLiveDrive(context.Background(), "demo-1", d)
	m.flushCatchup("demo-1")
	if log.driveWrites["d1"] != 0 {
		t.Errorf("superseded buffered drive row flushed %d times", log.driveWrites["d1"])
	}
}

func TestReplayCatchup_LiveMonitorUnaffected(t *testing.T) {
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.quietHooks.Store(true) // even if set, no catch-up writer on live monitors
	if m.catchup != nil || m.deferringWrites() {
		t.Fatal("live monitor must never defer writes")
	}
}

// A failed sample flush keeps the batch and writes it (in order, ahead of
// newer frames) on a later flush, instead of leaving a permanent hole
// behind the persisted max(at) (devils-advocate, #72).
func TestReplayCatchup_FailedFlushRequeues(t *testing.T) {
	t0 := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	backlog := catchupFrames(t0, 1600, 0)
	live := backlog[len(backlog)-1]
	live.At = time.Now()
	src := &scriptedSource{frames: append(backlog, live), liveFrom: len(backlog)}
	m, log := newCatchupMonitor(src)
	failures := 1
	insert := m.catchup.sinks.insertSamples
	m.catchup.sinks.insertSamples = func(ctx context.Context, b []samples.Sample) error {
		if failures > 0 {
			failures--
			return errors.New("db down")
		}
		return insert(ctx, b)
	}
	m.runSource(context.Background(), "demo-1")

	if failures != 0 {
		t.Fatal("the failing flush never ran")
	}
	if got := log.totalSamples(); got != len(backlog) {
		t.Fatalf("samples persisted = %d, want all %d (none lost to the failed flush)", got, len(backlog))
	}
	var prev time.Time
	for _, b := range log.batches {
		for _, s := range b {
			if !s.At.After(prev) {
				t.Fatalf("samples out of order after requeue: %s after %s", s.At, prev)
			}
			prev = s.At
		}
	}
	if m.catchup.pendingSamples("demo-1") != 0 {
		t.Error("buffer not drained")
	}
}

// The catch-up flush is the owner's write: held it carries the lease, warming keeps the batch, lost
// refuses (and counts); lead mode off is unchanged.
func TestCatchupFenceByState(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 4}
	for _, c := range []struct {
		name    string
		src     fence.Source
		err     error
		hasTok  bool
		counted int
	}{
		{"off", nil, nil, false, 0},
		{"held", fakeFence{tok: tok, st: fence.Held}, nil, true, 0},
		{"warming", fakeFence{tok: tok, st: fence.Warming}, errCatchupWarming, false, 0},
		{"lost", fakeFence{st: fence.Lost}, fence.ErrFenced, false, 1},
	} {
		m := NewStateMonitor(nil, nil)
		n := 0
		m.SetFence(c.src, func(string) { n++ })
		ctx, err := m.catchupFence(context.Background(), "demo-1")
		if !errors.Is(err, c.err) || (c.err == nil && err != nil) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.err)
		}
		if _, has := fence.From(ctx); has != c.hasTok {
			t.Errorf("%s: token in ctx = %v, want %v", c.name, has, c.hasTok)
		}
		if n != c.counted {
			t.Errorf("%s: counted %d, want %d", c.name, n, c.counted)
		}
	}
}

// A flush the lease refuses drops the backlog (the new owner's replay rebuilds it) and does not run the
// snapshot ahead of it; one that waits out the warm-up keeps it and writes it later.
func TestCatchupFlushFencedDropsAndWarmingKeeps(t *testing.T) {
	for _, c := range []struct {
		name        string
		err         error
		wantPending int
		wantSnap    int
	}{
		{"fenced", fence.ErrFenced, 0, 0},
		{"warming", errCatchupWarming, 3, 0},
	} {
		m, log := newCatchupMonitor(&scriptedSource{})
		fenced := 0
		m.SetFence(nil, func(string) { fenced++ })
		m.catchup.sinks.insertSamples = func(context.Context, []samples.Sample) error { return c.err }
		now := time.Now()
		for i := 0; i < 3; i++ {
			m.catchup.addSample("demo-1", samples.Sample{VehicleID: "demo-1", At: now.Add(time.Duration(i) * time.Second)})
		}
		m.catchup.setSnapshot("demo-1", LiveStateSnapshot{DriveCounter: 1})
		m.flushCatchup("demo-1")
		if got := m.catchup.pendingSamples("demo-1"); got != c.wantPending {
			t.Errorf("%s: pending = %d, want %d", c.name, got, c.wantPending)
		}
		if log.snapshots != c.wantSnap {
			t.Errorf("%s: snapshots = %d, want %d (the snapshot never runs ahead of the samples)", c.name, log.snapshots, c.wantSnap)
		}
		_ = fenced
	}
}
