package otadetails

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/rivian"
)

type mapFence map[string]fence.State

func (m mapFence) Fence(id string) (fence.Token, fence.State) {
	st, ok := m[id]
	if !ok {
		return fence.Token{}, fence.Lost
	}
	if st == fence.Held {
		return fence.Token{Group: "g-" + id, Pod: "pod-a", Epoch: 3}, st
	}
	return fence.Token{}, st
}

// tokenStore records the fence token each claim and save sees, per vehicle.
type tokenStore struct {
	*fakeStore
	tok map[string][]fence.Token
}

func (t *tokenStore) note(ctx context.Context, id string) {
	if t.tok == nil {
		t.tok = map[string][]fence.Token{}
	}
	tok, _ := fence.From(ctx)
	t.tok[id] = append(t.tok[id], tok)
}
func (t *tokenStore) Claim(ctx context.Context, uid uuid.UUID, id string, d time.Duration) (bool, error) {
	t.note(ctx, id)
	return t.fakeStore.Claim(ctx, uid, id, d)
}
func (t *tokenStore) Save(ctx context.Context, uid uuid.UUID, id string, info *rivian.OTAInfo) error {
	t.note(ctx, id)
	return t.fakeStore.Save(ctx, uid, id, info)
}

type countingRegistry struct {
	registry
	fors int
}

func (r *countingRegistry) For(uid uuid.UUID) rivian.Account { r.fors++; return r.registry.For(uid) }

// In lead mode only the vehicles whose group this pod holds are claimed and fetched, and each one's
// writes carry its own token; a vehicle another pod owns (Lost) or that was just taken (Warming) is
// left alone.
func TestSyncUserOnlyTheOwnersVehicles(t *testing.T) {
	st := &tokenStore{fakeStore: &fakeStore{ids: []string{"a", "b", "c"}, due: map[string]bool{"a": true, "b": true, "c": true}}}
	acct := &fakeAcct{}
	reg := &countingRegistry{registry: registry{acct}}
	s := newSyncer(st, reg, nil, nil)
	s.sleep = func(context.Context, time.Duration) {}
	s.SetFence(mapFence{"a": fence.Held, "b": fence.Lost, "c": fence.Warming})
	n, err := s.SyncUser(context.Background(), uuid.New())
	if err != nil || n != 1 {
		t.Fatalf("fetched %d, %v; want 1", n, err)
	}
	if len(acct.calls) != 1 || acct.calls[0] != "a" {
		t.Errorf("Rivian calls %v; want only a", acct.calls)
	}
	if _, ok := st.tok["b"]; ok {
		t.Error("b (another pod's) was claimed")
	}
	if _, ok := st.tok["c"]; ok {
		t.Error("c (warming) was claimed")
	}
	for _, tok := range st.tok["a"] {
		if tok.Group != "g-a" || tok.Epoch != 3 {
			t.Errorf("a's write carried %+v; want its group's token", tok)
		}
	}
}

// A pod that owns none of the user's vehicles doesn't touch the Rivian account at all.
func TestSyncUserNotTheOwnerTouchesNothing(t *testing.T) {
	st := &tokenStore{fakeStore: &fakeStore{ids: []string{"a", "b"}, due: map[string]bool{"a": true, "b": true}}}
	acct := &fakeAcct{}
	reg := &countingRegistry{registry: registry{acct}}
	s := newSyncer(st, reg, nil, nil)
	s.SetFence(mapFence{"a": fence.Lost, "b": fence.Warming})
	if n, err := s.SyncUser(context.Background(), uuid.New()); err != nil || n != 0 {
		t.Fatalf("fetched %d, %v; want 0", n, err)
	}
	if reg.fors != 0 || len(acct.calls) != 0 || len(st.tok) != 0 {
		t.Errorf("account lookups %d, Rivian calls %v, writes %v; want none", reg.fors, acct.calls, st.tok)
	}
}

// With lead mode off (Off) every vehicle is synced as before, with no token.
func TestSyncUserLeadModeOffIsUnchanged(t *testing.T) {
	st := &tokenStore{fakeStore: &fakeStore{ids: []string{"a", "b"}, due: map[string]bool{"a": true, "b": true}}}
	acct := &fakeAcct{}
	s := newSyncer(st, registry{acct}, nil, nil)
	s.sleep = func(context.Context, time.Duration) {}
	s.SetFence(mapFence{"a": fence.Off, "b": fence.Off})
	if n, err := s.SyncUser(context.Background(), uuid.New()); err != nil || n != 2 {
		t.Fatalf("fetched %d, %v; want 2", n, err)
	}
	for id, toks := range st.tok {
		for _, tok := range toks {
			if tok != (fence.Token{}) {
				t.Errorf("%s: a write carried a token %+v with lead mode off", id, tok)
			}
		}
	}
}
