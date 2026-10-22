package rivian

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// memCorpus is an in-memory ReplayCorpus over a fixed frame list.
type memCorpus struct{ frames []State }

func (c *memCorpus) Frames(_ context.Context, from, to time.Time) ([]State, error) {
	var out []State
	for _, f := range c.frames {
		if !f.At.Before(from) && f.At.Before(to) {
			out = append(out, f)
		}
	}
	return out, nil
}

func (c *memCorpus) LastAtOrBefore(_ context.Context, t time.Time) (*State, error) {
	var last *State
	for i := range c.frames {
		if !c.frames[i].At.After(t) {
			last = &c.frames[i]
		}
	}
	return last, nil
}

// testFeed builds a feed over frames every minute for span, odometer
// climbing 1 km per frame, anchored at epoch.
func testFeed(epoch time.Time, span time.Duration) (*ReplayFeed, *memCorpus) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	c := &memCorpus{}
	n := int(span / time.Minute)
	for i := 0; i <= n; i++ {
		c.frames = append(c.frames, State{
			At:         start.Add(time.Duration(i) * time.Minute),
			Gear:       "P",
			OdometerKm: 1000 + float64(i),
		})
	}
	f := &ReplayFeed{
		corpus: c,
		timeline: replayTimeline{
			start:     start,
			end:       start.Add(span),
			epoch:     epoch,
			gap:       30 * time.Minute,
			odoSpanKm: float64(n),
		},
		source: Vehicle{Model: "R1S", PackKWh: 131},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f, c
}

// A wall window straddling the loop wrap maps onto the corpus tail and
// head, with the gap silent and the odometer continuing upward.
func TestReplayTimeline_WrapKeepsOdometerMonotonic(t *testing.T) {
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f, _ := testFeed(epoch, time.Hour) // period = 1h30m
	// Wall 1h20m..1h40m: last 10 min of loop 0's gap, first 10 min of loop 1.
	frames, err := f.frames(context.Background(), epoch.Add(80*time.Minute), epoch.Add(100*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 10 {
		t.Fatalf("got %d frames, want 10 (gap is silent)", len(frames))
	}
	if want := epoch.Add(90 * time.Minute); !frames[0].At.Equal(want) {
		t.Errorf("first frame at %v, want loop-1 start %v", frames[0].At, want)
	}
	// Loop 1 starts at loop 0's final odometer (1060) and climbs.
	if frames[0].OdometerKm != 1060 {
		t.Errorf("loop-1 odometer starts at %.0f, want 1060", frames[0].OdometerKm)
	}
	if !sort.SliceIsSorted(frames, func(i, j int) bool { return frames[i].At.Before(frames[j].At) }) {
		t.Error("frames not time-ordered")
	}
}

// latest() during the loop gap reports the corpus's final frame.
func TestReplayFeed_LatestInGapIsCorpusEnd(t *testing.T) {
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f, _ := testFeed(epoch, time.Hour)
	st, err := f.latest(context.Background(), epoch.Add(75*time.Minute))
	if err != nil || st == nil {
		t.Fatalf("latest: %v %v", st, err)
	}
	if want := epoch.Add(time.Hour); !st.At.Equal(want) {
		t.Errorf("latest at %v, want corpus end placed at %v", st.At, want)
	}
}

// A replay account only honours sessions it issued, and only streams
// its own demo vehicle.
func TestReplayClient_SessionAndVehicleIsolation(t *testing.T) {
	f, _ := testFeed(replayEpoch, time.Hour)
	uid := uuid.New()
	c := NewReplayClient(f, uid)

	c.Restore(Session{Email: "x@y", UserSessionToken: "real-rivian-token"})
	if c.Authenticated() {
		t.Fatal("restored a non-replay session")
	}
	if err := c.Login(context.Background(), Credentials{Email: "x@y", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot()
	c2 := NewReplayClient(f, uid)
	c2.Restore(snap)
	if !c2.Authenticated() {
		t.Fatal("replay snapshot did not restore")
	}

	vs, err := c.Vehicles(context.Background())
	if err != nil || len(vs) != 1 || vs[0].ID != DemoVehicleID(uid) {
		t.Fatalf("vehicles = %+v, %v", vs, err)
	}
	err = c.Stream(context.Background(), "01-000000000", func(State, bool) {
		t.Fatal("emitted a frame for a real vehicle")
	})
	if !errors.Is(err, ErrVehicleNotFound) {
		t.Fatalf("stream real vehicle: %v, want ErrVehicleNotFound", err)
	}
}

// Stream backfills elapsed frames flagged catchingUp, then switches to
// live frames, resuming after the last recorded sample.
func TestReplayFeed_StreamCatchesUpThenGoesLive(t *testing.T) {
	// Not parallel: it asserts a frame count inside a 3 s wall-clock
	// window, which the CPU-heavy parallel corpus tests could starve
	// under -race.
	now := time.Now()
	// Anchor so the corpus started 3h ago and runs 1h past now.
	epoch := now.Add(-3 * time.Hour).Truncate(time.Minute)
	f, _ := testFeed(epoch, 4*time.Hour)
	f.backfill = 2 * time.Hour
	f.lastRecorded = func(context.Context, string) (time.Time, error) { return time.Time{}, nil }

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var mu sync.Mutex
	var catchUp, live int
	var first time.Time
	_ = f.Stream(ctx, "demo", func(st State, catchingUp bool) {
		mu.Lock()
		defer mu.Unlock()
		if first.IsZero() {
			first = st.At
		}
		if catchingUp {
			catchUp++
		} else {
			live++
		}
	})
	if d := first.Sub(now.Add(-2 * time.Hour)); d < -time.Minute || d > time.Minute {
		t.Errorf("first frame %v, want ~backfill (2h) ago", first)
	}
	if catchUp < 100 {
		t.Errorf("catch-up frames = %d, want ~118", catchUp)
	}
	if live == 0 {
		t.Error("no live (not catching up) frames near now")
	}
}

// With an allowlist, other users can neither log in nor be restored.
func TestReplayClient_AllowedUsers(t *testing.T) {
	f, _ := testFeed(replayEpoch, time.Hour)
	ok, other := uuid.New(), uuid.New()
	f.SetAllowedUsers(func(uid uuid.UUID) bool { return uid == ok })

	c := NewReplayClient(f, other)
	err := c.Login(context.Background(), Credentials{Email: "x@y", Password: "p"})
	if !errors.Is(err, ErrReplayNotAllowed) {
		t.Fatalf("login for disallowed user: %v", err)
	}
	c.Restore(Session{Email: "x@y", UserSessionToken: replaySessionToken})
	if c.Authenticated() {
		t.Fatal("restored a disallowed user")
	}
	if err := NewReplayClient(f, ok).Login(context.Background(), Credentials{Email: "x@y", Password: "p"}); err != nil {
		t.Fatalf("login for allowed user: %v", err)
	}
}

// A scheduled pause (charging_ready) between charging frames keeps the
// cable plugged; driving or charger_disconnected clears it.
func TestPlugTrackerStickyThroughChargingReady(t *testing.T) {
	var p plugTracker
	seq := []struct {
		gear, cs, status string
		wantPlugged      bool
	}{
		{"P", "charging_active", "chrgr_sts_connected_charging", true},
		{"P", "charging_ready", "chrgr_sts_not_connected", true}, // pause mid-session
		{"P", "charging_active", "chrgr_sts_connected_charging", true},
		{"P", "charger_disconnected", "chrgr_sts_not_connected", false},
		{"P", "charging_ready", "chrgr_sts_not_connected", false}, // unplugged, idle
		{"D", "charging_ready", "chrgr_sts_not_connected", false},
	}
	for i, f := range seq {
		st := State{Gear: f.gear, ChargerState: f.cs, ChargerStatus: f.status}
		p.apply(&st)
		if got := isPluggedCS(st.ChargerStatus); got != f.wantPlugged {
			t.Errorf("frame %d (%s/%s): plugged=%v, want %v", i, f.gear, f.cs, got, f.wantPlugged)
		}
	}
}
