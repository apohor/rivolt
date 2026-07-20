package rivian

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/drives"
)

// Replayer feeds a captured sequence of State frames through the real
// recorder lifecycle with persistence disabled, collecting the drives it
// would have produced. It lets recorder/lifecycle changes be regression-
// tested against real (DB-exported) or hand-authored frame streams offline
// — no database and no live Rivian feed (which preview can no longer
// provide; see docs/REPLAY_HARNESS.md and the preview-rivian-disconnected
// note).
//
// Frames are full State snapshots (the shape a vehicle_state row exports
// to), fed in arrival order — NOT deltas — so each frame is applied as-is,
// exactly as the merged cache state the live callback hands the recorder.
type Replayer struct {
	m       *StateMonitor
	vehicle string
	prev    *State

	mu     sync.Mutex
	closed []drives.Drive
}

// NewReplayer builds a persistence-free monitor for one vehicle id. All
// stores are left nil, so the recorder's writes are no-ops (the SetStores
// contract) and only the in-memory lifecycle runs; closed drives are
// captured via the drive-close hook.
func NewReplayer(vehicleID string) *Replayer {
	r := &Replayer{vehicle: vehicleID}
	m := &StateMonitor{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions:    map[string]*liveSessions{},
		cache:       map[string]*State{},
		stamp:       map[string]time.Time{},
		vehicleInfo: map[string]*Vehicle{},
	}
	m.driveCloseHook = func(_ context.Context, d drives.Drive) {
		r.mu.Lock()
		r.closed = append(r.closed, d)
		r.mu.Unlock()
	}
	r.m = m
	return r
}

// Feed pushes one frame through the recorder exactly as the live WS
// callback would after merging: it becomes the current cache state and
// drives the lifecycle. VehicleID is stamped automatically.
func (r *Replayer) Feed(f State) {
	cur := f
	cur.VehicleID = r.vehicle
	r.m.mu.Lock()
	r.m.cache[r.vehicle] = &cur
	r.m.mu.Unlock()
	r.m.record(context.Background(), r.vehicle, r.prev, &cur)
	r.prev = &cur
}

// FeedAll replays a whole sequence in order.
func (r *Replayer) FeedAll(frames []State) {
	for _, f := range frames {
		r.Feed(f)
	}
}

// Drives returns the drives the replay produced. The recorder fires the
// close hook in a goroutine, so this waits for the collection to go quiet
// (no new drive for a short settle window) before reading it back — safe
// to call once after the last Feed.
func (r *Replayer) Drives() []drives.Drive {
	const settle = 100 * time.Millisecond
	last := -1
	for {
		r.mu.Lock()
		n := len(r.closed)
		r.mu.Unlock()
		if n == last {
			break
		}
		last = n
		time.Sleep(settle)
	}
	r.mu.Lock()
	out := append([]drives.Drive(nil), r.closed...)
	r.mu.Unlock()
	return out
}
