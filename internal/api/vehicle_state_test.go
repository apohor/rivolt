package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/rivian"
)

// restStateClient is a rivian.Client whose State is the REST fallback.
type restStateClient struct {
	rivian.Client
	mu    sync.Mutex
	calls int
	st    rivian.State
}

func (c *restStateClient) State(context.Context, string) (*rivian.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	st := c.st
	return &st, nil
}

// peerStore is the shared store a peer pod publishes its live State to.
type peerStore struct {
	mu sync.Mutex
	st *rivian.State
}

func (s *peerStore) Save(context.Context, string, rivian.LiveStateSnapshot, time.Duration) error {
	return nil
}
func (s *peerStore) Load(context.Context, string) (rivian.LiveStateSnapshot, bool, error) {
	return rivian.LiveStateSnapshot{}, false, nil
}
func (s *peerStore) Delete(context.Context, string) error { return nil }
func (s *peerStore) PutState(_ context.Context, _ string, st *rivian.State, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *st
	s.st = &cp
	return nil
}
func (s *peerStore) GetState(context.Context, string) (*rivian.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st == nil {
		return nil, nil
	}
	cp := *s.st
	return &cp, nil
}

// On a pod that doesn't own the vehicle, a REST fallback (taken while
// the owner's snapshot was missing) no longer pins /api/state: once the
// owner publishes, the snapshot is served.
func TestHandleVehicleState_NonOwnerFollowsPeerSnapshot(t *testing.T) {
	const vid = "veh-1"
	t0 := time.Date(2026, 10, 15, 19, 35, 1, 0, time.UTC)
	client := &restStateClient{st: rivian.State{VehicleID: vid, At: t0}}
	store := &peerStore{}
	mon := rivian.NewStateMonitor(nil, nil)
	mon.SetLiveStateStore(store)
	h := handleVehicleState(client, mon)

	get := func() time.Time {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/state/"+vid, nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("vehicleID", vid)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var st rivian.State
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st.At
	}

	// No snapshot yet: REST, primed; an immediate poll reuses it.
	if at := get(); !at.Equal(t0) || client.calls != 1 {
		t.Fatalf("first poll: at %v after %d REST calls, want %v after 1", at, client.calls, t0)
	}
	if at := get(); !at.Equal(t0) || client.calls != 1 {
		t.Fatalf("second poll: at %v after %d REST calls, want the primed %v, no refetch", at, client.calls, t0)
	}

	// The owner publishes newer state: served, not the primed entry.
	t1 := t0.Add(12 * time.Minute)
	_ = store.PutState(context.Background(), vid, &rivian.State{VehicleID: vid, At: t1}, 0)
	if at := get(); !at.Equal(t1) || client.calls != 1 {
		t.Errorf("after the owner published: at %v after %d REST calls, want the snapshot %v", at, client.calls, t1)
	}
}
