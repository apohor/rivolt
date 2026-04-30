package leases

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory storeAPI used to assert the
// Coordinator's reconcile diff logic without booting Postgres. It
// behaves like a single-pod view: Acquire always succeeds (no peers
// to compete with) until vehicleID is removed from the seed set;
// stealVeh simulates a peer winning a lease away.
type fakeStore struct {
	mu        sync.Mutex
	owned     map[string]bool
	stealVeh  string // if non-empty, drop from `owned` next Renew
	acquireOK func(string) bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{owned: make(map[string]bool)}
}

func (f *fakeStore) Acquire(_ context.Context, vid string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireOK != nil && !f.acquireOK(vid) {
		return false, nil
	}
	f.owned[vid] = true
	return true, nil
}

func (f *fakeStore) Renew(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stealVeh != "" {
		delete(f.owned, f.stealVeh)
		f.stealVeh = ""
	}
	out := make([]string, 0, len(f.owned))
	for k := range f.owned {
		out = append(out, k)
	}
	return out, nil
}

func (f *fakeStore) ReleaseAll(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.owned)
	f.owned = make(map[string]bool)
	return n, nil
}

func TestCoordinatorAcquiresAndReleases(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	var (
		mu       sync.Mutex
		acquired []string
		released []string
	)
	onAcquire := func(v string) {
		mu.Lock()
		acquired = append(acquired, v)
		mu.Unlock()
	}
	onRelease := func(v string) {
		mu.Lock()
		released = append(released, v)
		mu.Unlock()
	}

	c := newCoordinator(
		fs,
		func(context.Context) ([]string, error) { return []string{"v1", "v2"}, nil },
		onAcquire, onRelease,
		nil,
		// reconcileInterval irrelevant — we drive ticks manually via
		// reconcile() calls.
		time.Hour,
	)

	// First reconcile: should acquire both vehicles.
	c.reconcile(context.Background())
	mu.Lock()
	if len(acquired) != 2 || len(released) != 0 {
		t.Fatalf("after first reconcile: acquired=%v released=%v", acquired, released)
	}
	mu.Unlock()

	// Simulate a peer stealing v1 — Renew on next tick will return
	// only v2, so the coordinator should fire onRelease("v1").
	fs.mu.Lock()
	fs.stealVeh = "v1"
	fs.mu.Unlock()
	c.reconcile(context.Background())
	mu.Lock()
	if len(released) != 1 || released[0] != "v1" {
		t.Fatalf("after steal: released=%v", released)
	}
	// And we should attempt to re-acquire v1, since the cluster set
	// still contains it. The fake will hand it back.
	if len(acquired) != 3 || acquired[2] != "v1" {
		t.Fatalf("expected re-acquisition of v1: acquired=%v", acquired)
	}
	mu.Unlock()
}

func TestCoordinatorShutdownReleasesAll(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	var (
		mu       sync.Mutex
		released []string
	)
	c := newCoordinator(
		fs,
		func(context.Context) ([]string, error) { return []string{"v1", "v2"}, nil },
		func(string) {},
		func(v string) {
			mu.Lock()
			released = append(released, v)
			mu.Unlock()
		},
		nil,
		time.Hour,
	)
	c.reconcile(context.Background())
	c.Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(released) != 2 {
		t.Fatalf("expected 2 onRelease callbacks, got %v", released)
	}
}

func TestNewStoreRejectsEmptyPodID(t *testing.T) {
	t.Parallel()
	if _, err := NewStore(nil, ""); err == nil {
		t.Fatal("expected error for nil DB")
	}
	// Can't easily test the empty-podID branch without a real *sql.DB;
	// rely on integration coverage for that path.
}

// TestCoordinatorTriggerReconcile is the boot-trigger contract
// test: a coordinator whose vehicle list is initially empty should
// pick up newly-discovered vehicles within milliseconds of
// TriggerReconcile, NOT wait the full reconcileInterval. Regression
// test for the v0.16.1 fix where pod boot would wait ~30s for the
// next tick before subscribing to anything.
func TestCoordinatorTriggerReconcile(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	var (
		mu       sync.Mutex
		acquired []string
		ready    bool
	)
	source := func(context.Context) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		if !ready {
			return nil, nil
		}
		return []string{"v1"}, nil
	}
	c := newCoordinator(
		fs,
		source,
		func(v string) {
			mu.Lock()
			acquired = append(acquired, v)
			mu.Unlock()
		},
		func(string) {},
		nil,
		// Long interval — the test must succeed via the trigger,
		// never via the ticker. If the trigger plumbing is broken
		// this hangs and times out.
		time.Hour,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = c.Run(ctx)
		close(done)
	}()

	// Wait for the immediate first reconcile (sees empty vehicle
	// set), then flip the source and trigger.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		empty := len(acquired) == 0
		mu.Unlock()
		if empty {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	ready = true
	mu.Unlock()
	c.TriggerReconcile()

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(acquired)
		mu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	if len(acquired) != 1 || acquired[0] != "v1" {
		mu.Unlock()
		t.Fatalf("expected v1 acquired via trigger, got %v", acquired)
	}
	mu.Unlock()

	cancel()
	<-done
}

// TestCoordinatorTriggerCoalesces verifies many simultaneous
// TriggerReconcile calls don't pile up — the cap-1 channel
// coalesces them into at most one extra reconcile beyond what's
// in flight.
func TestCoordinatorTriggerCoalesces(t *testing.T) {
	t.Parallel()
	c := newCoordinator(
		newFakeStore(),
		func(context.Context) ([]string, error) { return nil, nil },
		func(string) {},
		func(string) {},
		nil,
		time.Hour,
	)
	// 1000 triggers in a row — the channel should never block.
	for i := 0; i < 1000; i++ {
		c.TriggerReconcile()
	}
	// And it survives a nil receiver gracefully.
	(*Coordinator)(nil).TriggerReconcile()
}
