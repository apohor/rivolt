package receipts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/rivian"
)

type fakeFencer struct {
	tok   fence.Token
	state fence.State
	err   error
	calls int
}

func (f *fakeFencer) FenceUser(context.Context, string) (fence.Token, fence.State, error) {
	f.calls++
	return f.tok, f.state, f.err
}

// tokenStore records the fence token each write sees.
type tokenStore struct {
	*fakeStore
	seen []fence.Token
	none int
}

func (t *tokenStore) note(ctx context.Context) {
	if tok, ok := fence.From(ctx); ok {
		t.seen = append(t.seen, tok)
	} else {
		t.none++
	}
}
func (t *tokenStore) ClaimSummaries(ctx context.Context, uid uuid.UUID, d time.Duration) (bool, error) {
	t.note(ctx)
	return t.fakeStore.ClaimSummaries(ctx, uid, d)
}
func (t *tokenStore) UpsertReceipts(ctx context.Context, uid uuid.UUID, in []rivian.SessionSummary) error {
	t.note(ctx)
	return t.fakeStore.UpsertReceipts(ctx, uid, in)
}
func (t *tokenStore) MarkResult(ctx context.Context, uid uuid.UUID, err error) error {
	t.note(ctx)
	return t.fakeStore.MarkResult(ctx, uid, err)
}

type countingRegistry struct {
	fakeRegistry
	fors int
}

func (r *countingRegistry) For(uid uuid.UUID) rivian.Account {
	r.fors++
	return r.fakeRegistry.For(uid)
}

func newFenceSyncer(st store, a *fakeAcct) (*Syncer, *countingRegistry) {
	reg := &countingRegistry{fakeRegistry: fakeRegistry{a}}
	s := newSyncer(st, reg, func(uuid.UUID) bool { return true }, nil, nil)
	s.sleep = func(context.Context, time.Duration) {}
	return s, reg
}

// A pod that doesn't own the user's group (Lost, or Warming after a steal) skips before the claim:
// it never takes the claim (which would hold the owner off for the interval) and never touches the
// Rivian account.
func TestSyncSkipsWhenThisPodIsNotTheOwner(t *testing.T) {
	for _, st := range []fence.State{fence.Lost, fence.Warming} {
		store := &tokenStore{fakeStore: &fakeStore{claimOK: true}}
		acct := &fakeAcct{sums: []rivian.SessionSummary{{TransactionID: "t1"}}}
		s, reg := newFenceSyncer(store, acct)
		s.SetFence(&fakeFencer{state: st})
		res, err := s.Sync(context.Background(), uuid.New(), time.Minute, false)
		if err != nil {
			t.Fatalf("state %d: %v", st, err)
		}
		if !res.Skipped || !res.NotOwner {
			t.Errorf("state %d: result %+v, want skipped as not the owner", st, res)
		}
		if len(store.claims) != 0 || acct.sumCalls != 0 || reg.fors != 0 {
			t.Errorf("state %d: claims %v, Rivian calls %d, account lookups %d; want none", st, store.claims, acct.sumCalls, reg.fors)
		}
	}
}

// The owner (Held) syncs, and every write it makes carries its token; with lead mode off (Off) the
// writes carry none, as before.
func TestSyncCarriesTheOwnersTokenToEveryWrite(t *testing.T) {
	tok := fence.Token{Group: "g1", Pod: "pod-a", Epoch: 7}
	for _, c := range []struct {
		state fence.State
		want  bool
	}{{fence.Held, true}, {fence.Off, false}} {
		store := &tokenStore{fakeStore: &fakeStore{claimOK: true}}
		acct := &fakeAcct{sums: []rivian.SessionSummary{{TransactionID: "t1"}}}
		s, _ := newFenceSyncer(store, acct)
		s.SetFence(&fakeFencer{tok: tok, state: c.state})
		if _, err := s.Sync(context.Background(), uuid.New(), time.Minute, false); err != nil {
			t.Fatal(err)
		}
		if acct.sumCalls != 1 {
			t.Errorf("state %d: Rivian summaries calls %d, want 1", c.state, acct.sumCalls)
		}
		if c.want {
			if store.none != 0 || len(store.seen) < 3 {
				t.Errorf("Held: writes without the token %d, with it %d; want every write (claim, upsert, mark) to carry it", store.none, len(store.seen))
			}
			for _, got := range store.seen {
				if got != tok {
					t.Errorf("Held: a write carried %+v, want %+v", got, tok)
				}
			}
		} else if len(store.seen) != 0 {
			t.Errorf("Off: %d writes carried a token, want none", len(store.seen))
		}
	}
}

// If the fence can't be read (the user-to-group query failed), nothing is claimed.
func TestSyncFenceErrorClaimsNothing(t *testing.T) {
	store := &tokenStore{fakeStore: &fakeStore{claimOK: true}}
	acct := &fakeAcct{}
	s, _ := newFenceSyncer(store, acct)
	s.SetFence(&fakeFencer{err: errors.New("db down")})
	if _, err := s.Sync(context.Background(), uuid.New(), time.Minute, false); err == nil {
		t.Fatal("want the fence error")
	}
	if len(store.claims) != 0 || acct.sumCalls != 0 {
		t.Errorf("claims %v, Rivian calls %d; want none", store.claims, acct.sumCalls)
	}
}

// bothFencer is a lease-store stand-in: it answers per vehicle and per user.
type bothFencer struct{ *fakeFencer }

func (bothFencer) Fence(string) (fence.Token, fence.State) { return fence.Token{}, fence.Off }

// Lead mode, a syncer built before the lease store exists: through a required, empty holder it behaves as a pod that does not own
// the user (skips before the claim, no Rivian call, no account lookup); once the store is installed the same syncer syncs.
func TestSyncSkipsBeforeTheFenceIsInstalledInLeadMode(t *testing.T) {
	store := &tokenStore{fakeStore: &fakeStore{claimOK: true}}
	acct := &fakeAcct{sums: []rivian.SessionSummary{{TransactionID: "t1"}}}
	s, reg := newFenceSyncer(store, acct)
	var h fence.Holder
	h.Require(true)
	s.SetFence(&h)
	res, err := s.Sync(context.Background(), uuid.New(), time.Minute, false)
	if err != nil || !res.Skipped || !res.NotOwner || len(store.claims) != 0 || acct.sumCalls != 0 || reg.fors != 0 {
		t.Fatalf("before the install: %+v %v, claims %v, Rivian calls %d, lookups %d; want skipped, nothing touched", res, err, store.claims, acct.sumCalls, reg.fors)
	}
	h.Set(bothFencer{&fakeFencer{state: fence.Off}})
	if _, err := s.Sync(context.Background(), uuid.New(), time.Minute, false); err != nil || acct.sumCalls != 1 {
		t.Fatalf("after the install: %v, Rivian calls %d; want 1", err, acct.sumCalls)
	}
}
