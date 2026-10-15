package api

import (
	"context"
	"encoding/json"
	"errors"
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
	err   error
}

func (c *restStateClient) State(context.Context, string) (*rivian.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
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
// owner publishes, the snapshot is served. Without one, polls don't fan
// out into Rivian calls.
func TestHandleVehicleState_NonOwner(t *testing.T) {
	const vid = "veh-1"
	t0 := time.Date(2026, 10, 15, 19, 35, 1, 0, time.UTC)
	client := &restStateClient{st: rivian.State{VehicleID: vid, At: t0, PowerState: "ready"}}
	store := &peerStore{}
	mon := rivian.NewStateMonitor(nil, nil)
	mon.SetLiveStateStore(store)
	h := handleVehicleState(client, mon)

	get := func() (int, time.Time) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/state/"+vid, nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("vehicleID", vid)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != http.StatusOK {
			return w.Code, time.Time{}
		}
		var st rivian.State
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return w.Code, st.At
	}

	// No snapshot: one REST fetch, then 20 polls (5 min of a 15 s
	// poller) serve it without calling Rivian again.
	for i := 0; i < 21; i++ {
		if code, at := get(); code != http.StatusOK || !at.Equal(t0) {
			t.Fatalf("poll %d: %d at %v, want 200 at %v", i, code, at, t0)
		}
	}
	if client.calls != 1 {
		t.Errorf("21 polls with no snapshot made %d REST calls, want 1", client.calls)
	}

	// The owner publishes newer state: served, not the primed entry.
	t1 := t0.Add(12 * time.Minute)
	_ = store.PutState(context.Background(), vid, &rivian.State{VehicleID: vid, At: t1}, 0)
	if _, at := get(); !at.Equal(t1) || client.calls != 1 {
		t.Errorf("after the owner published: at %v after %d REST calls, want the snapshot %v", at, client.calls, t1)
	}
}

// A failed fetch serves the entry the pod has; with none, polls inside
// the refetch window get 503 rather than another Rivian call each.
func TestHandleVehicleState_FetchFailure(t *testing.T) {
	const vid = "veh-1"
	client := &restStateClient{err: errors.New("upstream down")}
	mon := rivian.NewStateMonitor(nil, nil)
	h := handleVehicleState(client, mon)
	codes := map[int]int{}
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest(http.MethodGet, "/api/state/"+vid, nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("vehicleID", vid)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
		w := httptest.NewRecorder()
		h(w, r)
		codes[w.Code]++
	}
	if client.calls != 1 || codes[http.StatusServiceUnavailable] != 4 {
		t.Errorf("5 polls against a failing upstream: %d REST calls, codes %v; want 1 call, then 503s", client.calls, codes)
	}
}
