//go:build integration

package vehiclediscovery_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/vehiclediscovery"
)

// Owner mode (single-owner M4-2b): the job looks up only the users of the groups its pod owns, keeps the answer in vehicle_discovery (none after
// 24 h, error after 30 min), skips a user who is not due, and a lookup that stores vehicles removes the row.
func TestOwnerDiscoveryKeepsWhatItFoundInPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := open(ctx, t)
	mine := linked(ctx, t, pool, "own-mine")
	empty := linked(ctx, t, pool, "own-empty")
	failing := linked(ctx, t, pool, "own-failing")
	notMine := linked(ctx, t, pool, "own-notmine")

	var calls atomic.Int32
	asked := map[uuid.UUID]int{}
	vid := "rv-own-" + uuid.NewString()[:8]
	var seen outcomes
	job := &vehiclediscovery.Job{DB: pool, Observe: seen.add, Sleep: func(context.Context, time.Duration) {},
		Users: func(context.Context) ([]vehiclediscovery.OwnedUser, error) { return owned(mine, empty, failing), nil },
		Lookup: func(_ context.Context, uid uuid.UUID) ([]rivian.Vehicle, error) {
			calls.Add(1)
			asked[uid]++
			switch uid {
			case mine:
				return []rivian.Vehicle{{ID: vid, Model: "R1S", Name: "Truck"}}, nil
			case failing:
				return nil, errors.New("rivian down")
			}
			return nil, nil
		}}
	if err := job.OwnerTick(ctx); err != nil {
		t.Fatal(err)
	}
	if asked[notMine] != 0 {
		t.Error("a user outside the pod's groups was looked up")
	}
	// Found: vehicles stored, no discovery row.
	if _, ok, _ := db.GetDiscovery(ctx, pool, mine); ok {
		t.Error("a user whose vehicles were found still has a discovery row")
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2`, mine, vid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the vehicle was not stored (%d, %v)", n, err)
	}
	// Empty: none, due again in about a day. Failed: error, due again in about half an hour.
	for u, want := range map[uuid.UUID]struct {
		state string
		in    time.Duration
	}{empty: {db.DiscoveryNone, vehiclediscovery.EmptyBackoff}, failing: {db.DiscoveryError, vehiclediscovery.ErrorBackoff}} {
		state, ok, err := db.GetDiscovery(ctx, pool, u)
		if err != nil || !ok || state != want.state {
			t.Fatalf("stored state %q ok=%v err=%v, want %q", state, ok, err, want.state)
		}
		var until time.Duration
		var secs float64
		if err := pool.QueryRowContext(ctx, `SELECT extract(epoch FROM next_check_at - now()) FROM vehicle_discovery WHERE user_id = $1`, u).Scan(&secs); err != nil {
			t.Fatal(err)
		}
		until = time.Duration(secs * float64(time.Second))
		if until > want.in+time.Minute || until < want.in-time.Minute {
			t.Errorf("%s: next check in %v, want about %v", want.state, until, want.in)
		}
	}
	// Not due: the next pass asks nobody.
	before := calls.Load()
	if err := job.OwnerTick(ctx); err != nil || calls.Load() != before {
		t.Errorf("a second pass made %d more lookups (err %v), want none while nothing is due", calls.Load()-before, err)
	}
	// Due again: move the error row into the past; the nudge looks that user up at once, and only if the pod owns them.
	if _, err := pool.ExecContext(ctx, `UPDATE vehicle_discovery SET next_check_at = now() - interval '1 minute' WHERE user_id = $1`, failing); err != nil {
		t.Fatal(err)
	}
	job.LookUpNow(ctx, notMine)
	if calls.Load() != before {
		t.Error("a nudge for a user the pod does not own made a lookup")
	}
	job.LookUpNow(ctx, failing)
	if calls.Load() != before+1 {
		t.Errorf("a nudge for a due owned user made %d lookups, want 1", calls.Load()-before)
	}
}

// A flagged (needs re-auth) user is not looked up in owner mode either.
func TestOwnerDiscoverySkipsAFlaggedUser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := open(ctx, t)
	u := linked(ctx, t, pool, "own-flagged")
	if _, err := db.RaiseNeedsReauth(ctx, pool, u, "rejected"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	job := &vehiclediscovery.Job{DB: pool, Sleep: func(context.Context, time.Duration) {},
		Users:  func(context.Context) ([]vehiclediscovery.OwnedUser, error) { return owned(u), nil },
		Lookup: func(context.Context, uuid.UUID) ([]rivian.Vehicle, error) { calls.Add(1); return nil, nil }}
	if err := job.OwnerTick(ctx); err != nil || calls.Load() != 0 {
		t.Errorf("flagged user: %d lookups (err %v), want none", calls.Load(), err)
	}
}

// owned makes the job's view of the users a pod owns, with no fence token (an unguarded write, as when lead mode is off).
func owned(us ...uuid.UUID) []vehiclediscovery.OwnedUser {
	out := make([]vehiclediscovery.OwnedUser, len(us))
	for i, u := range us {
		out[i] = vehiclediscovery.OwnedUser{UID: u}
	}
	return out
}

// The answer is written under the group's fence token: a pod that no longer holds the group writes nothing, and a pod that does, does.
func TestOwnerDiscoveryAnswersAreFenced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := open(ctx, t)
	u := linked(ctx, t, pool, "own-fenced")
	// No lease row for this group: the token is not good.
	stale := fence.Token{Group: u.String(), Pod: "pod-gone", Epoch: 1}
	err := db.RecordDiscovery(fence.With(ctx, stale), pool, u, db.DiscoveryNone, time.Now().Add(time.Hour))
	if !errors.Is(err, fence.ErrFenced) {
		t.Fatalf("a write under a token for a group nobody holds: %v, want fence.ErrFenced", err)
	}
	if _, ok, _ := db.GetDiscovery(ctx, pool, u); ok {
		t.Error("a fenced write left a row")
	}
	// A live lease for this pod at this epoch: the same write goes through.
	var epoch int64
	if err := pool.QueryRowContext(ctx, `INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-live', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds') RETURNING epoch`, u).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	live := fence.Token{Group: u.String(), Pod: "pod-live", Epoch: epoch}
	if err := db.RecordDiscovery(fence.With(ctx, live), pool, u, db.DiscoveryNone, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("a write under the live token: %v", err)
	}
	if state, ok, _ := db.GetDiscovery(ctx, pool, u); !ok || state != db.DiscoveryNone {
		t.Errorf("stored %q ok=%v, want none", state, ok)
	}
	if err := db.ClearDiscovery(fence.With(ctx, stale), pool, u); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("clearing under a stale token: %v, want fence.ErrFenced", err)
	}
	if err := db.ClearDiscovery(fence.With(ctx, live), pool, u); err != nil {
		t.Errorf("clearing under the live token: %v", err)
	}
}

// A fresh sign-in looks the user up again at once, whatever a stored 'none' says, and a second sign-in inside ForceEvery does not.
func TestSignInLooksUpAgainDespiteNone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := open(ctx, t)
	u := linked(ctx, t, pool, "signin")
	var calls atomic.Int32
	job := &vehiclediscovery.Job{DB: pool, Sleep: func(context.Context, time.Duration) {},
		Lookup: func(context.Context, uuid.UUID) ([]rivian.Vehicle, error) { calls.Add(1); return nil, nil }}
	job.OwnerMode(func(context.Context) ([]vehiclediscovery.OwnedUser, error) { return owned(u), nil })
	if err := job.OwnerTick(ctx); err != nil || calls.Load() != 1 {
		t.Fatalf("first pass: %d lookups, err %v", calls.Load(), err)
	}
	if state, ok, _ := db.GetDiscovery(ctx, pool, u); !ok || state != db.DiscoveryNone {
		t.Fatalf("stored state %q ok=%v, want none", state, ok)
	}
	job.LookUpNow(ctx, u)
	if calls.Load() != 1 {
		t.Fatal("a plain nudge looked up a user who is not due")
	}
	job.SignedIn(ctx, u)
	if calls.Load() != 2 {
		t.Fatalf("sign-in made %d lookups in all, want 2", calls.Load())
	}
	job.SignedIn(ctx, u)
	if calls.Load() != 2 {
		t.Error("a second sign-in inside ForceEvery looked up again")
	}
}

// A user with one synced vehicle gains a second on the Rivian account: the owner's slow refresh stores it (no request-path Rivian call), only after
// RefreshEvery, a failed refresh is retried after ErrorBackoff, and a truck that left the account keeps its row, as it did on /api/vehicles.
func TestOwnerRefreshPicksUpASecondTruck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := open(ctx, t)
	u := linked(ctx, t, pool, "own-refresh")
	first, second := "rv-r1-"+uuid.NewString()[:8], "rv-r2-"+uuid.NewString()[:8]
	if err := db.UpsertVehicleFromSync(ctx, pool, u, db.VehicleSync{RivianID: first, Model: "R1T", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	account := []rivian.Vehicle{{ID: first, Model: "R1T", Name: "One"}}
	var calls int
	var lookupErr error
	var seen outcomes
	job := &vehiclediscovery.Job{DB: pool, Observe: seen.add, Sleep: func(context.Context, time.Duration) {}, Now: func() time.Time { return now },
		Users: func(context.Context) ([]vehiclediscovery.OwnedUser, error) { return owned(u), nil },
		Lookup: func(context.Context, uuid.UUID) ([]rivian.Vehicle, error) {
			calls++
			return account, lookupErr
		}}
	count := func() (n int) {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id IN ($2, $3)`, u, first, second).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tick := func() {
		t.Helper()
		if err := job.OwnerTick(ctx); err != nil {
			t.Fatal(err)
		}
	}

	account = append(account, rivian.Vehicle{ID: second, Model: "R1S", Name: "Two"})
	tick() // first sight of the user: the wait starts, nothing is asked
	now = now.Add(vehiclediscovery.RefreshEvery - time.Hour)
	tick()
	if calls != 0 || count() != 1 {
		t.Fatalf("before RefreshEvery: %d lookups, %d vehicles; want 0 and 1", calls, count())
	}
	now = now.Add(time.Hour + vehiclediscovery.RefreshEvery/8 + time.Minute) // past the longest jitter
	lookupErr = errors.New("rivian down")
	tick()
	if calls != 1 || count() != 1 || seen.get() != "refresh_error" {
		t.Fatalf("failed refresh: %d lookups, %d vehicles, outcomes %q; want 1, 1, refresh_error", calls, count(), seen.get())
	}
	tick() // retried only after ErrorBackoff
	if calls != 1 {
		t.Fatalf("a failed refresh was retried at once (%d lookups)", calls)
	}
	now = now.Add(vehiclediscovery.ErrorBackoff + time.Minute)
	lookupErr = nil
	tick()
	if calls != 2 || count() != 2 || seen.get() != "refresh_error,refreshed" {
		t.Fatalf("refresh: %d lookups, %d vehicles, outcomes %q; want 2, 2, refresh_error,refreshed", calls, count(), seen.get())
	}
	// The first truck leaves the account: its row stays, nothing is deleted.
	account = account[1:]
	now = now.Add(vehiclediscovery.RefreshEvery + vehiclediscovery.RefreshEvery/8 + time.Minute)
	tick()
	if calls != 3 || count() != 2 {
		t.Fatalf("after a truck left: %d lookups, %d vehicles; want 3 and 2", calls, count())
	}
}
