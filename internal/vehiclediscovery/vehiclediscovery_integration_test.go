//go:build integration

package vehiclediscovery_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
	"github.com/apohor/rivolt/internal/vehiclediscovery"
)

type memBackoff struct {
	mu   sync.Mutex
	held map[uuid.UUID]time.Duration
}

func (m *memBackoff) Held(_ context.Context, uid uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.held[uid]
	return ok
}

func (m *memBackoff) Hold(_ context.Context, uid uuid.UUID, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held == nil {
		m.held = map[uuid.UUID]time.Duration{}
	}
	m.held[uid] = d
}

func (m *memBackoff) heldFor(uid uuid.UUID) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held[uid]
}

type outcomes struct {
	mu sync.Mutex
	o  []string
}

func (o *outcomes) add(s string) { o.mu.Lock(); o.o = append(o.o, s); o.mu.Unlock() }
func (o *outcomes) get() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Join(o.o, ",")
}

func open(ctx context.Context, t *testing.T) *sql.DB {
	t.Helper()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// linked makes a user with a stored Rivian session (an opaque blob: the job only asks whether one exists).
func linked(ctx context.Context, t *testing.T, pool *sql.DB, name string) uuid.UUID {
	t.Helper()
	uid, err := db.EnsureUser(ctx, pool, name+"-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id) VALUES ($1, 'rivian.session', '\x00', 'k')`, uid); err != nil {
		t.Fatal(err)
	}
	return uid
}

// A linked user with no vehicle is the only candidate; one lookup primes the rows, the lease source query finds the vehicle, and the user is a
// candidate no more. Unlinked users, users who have vehicles, users with only legacy electrafi rows (not real) and flagged users are left out.
func TestDiscoveryPrimesALinkedUserWithNoVehicle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := open(ctx, t)
	target := linked(ctx, t, pool, "disc-target")
	hasVehicle := linked(ctx, t, pool, "disc-has")
	if err := db.UpsertVehicleFromSync(ctx, pool, hasVehicle, db.VehicleSync{RivianID: "rv-has-" + uuid.NewString()[:6]}); err != nil {
		t.Fatal(err)
	}
	onlyLegacy := linked(ctx, t, pool, "disc-legacy")
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'electrafi-abc')`, onlyLegacy); err != nil {
		t.Fatal(err)
	}
	flagged := linked(ctx, t, pool, "disc-flagged")
	if err := db.SetNeedsReauth(ctx, pool, flagged, "rejected"); err != nil {
		t.Fatal(err)
	}
	unlinked, _ := db.EnsureUser(ctx, pool, "disc-unlinked-"+uuid.NewString()[:8])

	cands, err := db.ListLinkedUsersWithoutVehicles(ctx, pool, 1000)
	if err != nil {
		t.Fatal(err)
	}
	in := map[uuid.UUID]bool{}
	for _, u := range cands {
		in[u] = true
	}
	if !in[target] || !in[onlyLegacy] || in[hasVehicle] || in[flagged] || in[unlinked] {
		t.Fatalf("candidates: target %v legacy-only %v has-vehicle %v flagged %v unlinked %v; want true true false false false", in[target], in[onlyLegacy], in[hasVehicle], in[flagged], in[unlinked])
	}

	vid := "rv-disc-" + uuid.NewString()[:8]
	var asked []uuid.UUID
	var seen outcomes
	job := &vehiclediscovery.Job{DB: pool, Backoff: &memBackoff{}, Observe: seen.add, Sleep: func(context.Context, time.Duration) {},
		Lookup: func(_ context.Context, uid uuid.UUID) ([]rivian.Vehicle, error) {
			asked = append(asked, uid)
			if uid == target {
				return []rivian.Vehicle{{ID: vid, Model: "R1T", Name: "Truck", TrimName: "Adventure", Make: "Rivian"}}, nil
			}
			return nil, nil // the other candidates: no vehicle yet
		}}
	job.Tick(ctx)
	var found string
	if err := pool.QueryRowContext(ctx, `SELECT rivian_vehicle_id FROM vehicles WHERE user_id = $1`, target).Scan(&found); err != nil || found != vid {
		t.Fatalf("the vehicle was not primed: %q (%v)", found, err)
	}
	// The row is what the lease coordinator's source reads.
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT DISTINCT rivian_vehicle_id FROM vehicles WHERE rivian_vehicle_id <> '' AND rivian_vehicle_id NOT LIKE 'electrafi-%') v WHERE rivian_vehicle_id = $1`, vid).Scan(&n); err != nil || n != 1 {
		t.Errorf("the lease source does not see the primed vehicle (%d, %v)", n, err)
	}
	after, _ := db.ListLinkedUsersWithoutVehicles(ctx, pool, 1000)
	for _, u := range after {
		if u == target {
			t.Error("a primed user is still a candidate")
		}
	}
	if !strings.Contains(seen.get(), vehiclediscovery.OutcomeStored) {
		t.Errorf("outcomes = %s, want stored among them", seen.get())
	}
}

// Backoffs: an error is held 30 minutes, an empty result 24 hours, and a held user is skipped (no Rivian call) on the next pass.
func TestDiscoveryBacksOffByOutcome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := open(ctx, t)
	u := linked(ctx, t, pool, "disc-backoff")
	bo := &memBackoff{}
	for name, c := range map[string]struct {
		lookup func() ([]rivian.Vehicle, error)
		want   time.Duration
		out    string
	}{
		"error": {func() ([]rivian.Vehicle, error) { return nil, errors.New("rivian down") }, vehiclediscovery.ErrorBackoff, vehiclediscovery.OutcomeError},
		"empty": {func() ([]rivian.Vehicle, error) { return nil, nil }, vehiclediscovery.EmptyBackoff, vehiclediscovery.OutcomeEmpty},
	} {
		bo.held = nil
		var calls atomic.Int32
		var seen outcomes
		job := &vehiclediscovery.Job{DB: pool, Backoff: bo, Observe: seen.add, Sleep: func(context.Context, time.Duration) {},
			Lookup: func(_ context.Context, uid uuid.UUID) ([]rivian.Vehicle, error) {
				if uid == u {
					calls.Add(1)
					return c.lookup()
				}
				return nil, nil
			}}
		job.Tick(ctx)
		if got := bo.heldFor(u); got != c.want {
			t.Errorf("%s: held %v, want %v", name, got, c.want)
		}
		job.Tick(ctx)
		if calls.Load() != 1 {
			t.Errorf("%s: %d lookups across two passes, want 1 (the second is skipped by the backoff); outcomes %s", name, calls.Load(), seen.get())
		}
		if !strings.Contains(seen.get(), c.out) || !strings.Contains(seen.get(), vehiclediscovery.OutcomeSkipped) {
			t.Errorf("%s: outcomes = %s, want %s and skipped", name, seen.get(), c.out)
		}
	}
}

// A lookup that says the Rivian session is invalid is counted needs_reauth; the account is flagged by the client's own reauth path (a fake
// here: the sink flags the row), and the user is not a candidate again.
func TestDiscoveryNeedsReauth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := open(ctx, t)
	u := linked(ctx, t, pool, "disc-reauth")
	var seen outcomes
	var calls atomic.Int32
	job := &vehiclediscovery.Job{DB: pool, Backoff: &memBackoff{}, Observe: seen.add, Sleep: func(context.Context, time.Duration) {},
		Lookup: func(ctx context.Context, uid uuid.UUID) ([]rivian.Vehicle, error) {
			if uid != u {
				return nil, nil
			}
			calls.Add(1)
			_ = db.SetNeedsReauth(ctx, pool, uid, "credentials rejected") // what the LiveClient's sink does on a user-action error
			return nil, &rivian.UpstreamError{Op: "getUserInfo", Class: rivian.ClassUserAction, HTTPStatus: 401}
		}}
	job.Tick(ctx)
	if !strings.Contains(seen.get(), vehiclediscovery.OutcomeNeedsReauth) {
		t.Errorf("outcomes = %s, want needs_reauth", seen.get())
	}
	job.Tick(ctx)
	if calls.Load() != 1 {
		t.Errorf("%d lookups for a flagged user across two passes, want 1", calls.Load())
	}
	cands, _ := db.ListLinkedUsersWithoutVehicles(ctx, pool, 1000)
	for _, c := range cands {
		if c == u {
			t.Error("a flagged user is still a candidate")
		}
	}
	// Already flagged in this pod's memory: the sentinel counts the same.
	u2 := linked(ctx, t, pool, "disc-reauth2")
	job2 := &vehiclediscovery.Job{DB: pool, Backoff: &memBackoff{}, Observe: seen.add, Sleep: func(context.Context, time.Duration) {},
		Lookup: func(_ context.Context, uid uuid.UUID) ([]rivian.Vehicle, error) {
			if uid == u2 {
				return nil, rivian.ErrNeedsReauth
			}
			return nil, nil
		}}
	job2.Tick(ctx)
	if strings.Count(seen.get(), vehiclediscovery.OutcomeNeedsReauth) < 2 {
		t.Errorf("outcomes = %s, want a second needs_reauth for ErrNeedsReauth", seen.get())
	}
}

// Two pods: one runs the pass, the other does nothing (a Postgres advisory lock).
func TestDiscoveryRunsOnOnePod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsn := testpg.DSN(ctx, t)
	a, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	u := linked(ctx, t, a, "disc-single")
	inside, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	lookup := func(_ context.Context, uid uuid.UUID) ([]rivian.Vehicle, error) {
		if uid != u {
			return nil, nil
		}
		if calls.Add(1) == 1 {
			close(inside)
			<-release
		}
		return nil, nil
	}
	podA := &vehiclediscovery.Job{DB: a, Lookup: lookup, Sleep: func(context.Context, time.Duration) {}}
	podB := &vehiclediscovery.Job{DB: b, Lookup: lookup, Sleep: func(context.Context, time.Duration) {}}
	done := make(chan struct{})
	go func() { podA.Tick(ctx); close(done) }()
	<-inside // pod A is inside its pass
	podB.Tick(ctx)
	close(release)
	<-done
	if calls.Load() != 1 {
		t.Errorf("%d lookups with two pods ticking at once, want 1", calls.Load())
	}
}
