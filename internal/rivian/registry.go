package rivian

import (
	"sync"

	"github.com/google/uuid"
)

// AccountRegistry resolves a per-user Account. Each user owns an
// isolated *LiveClient (or *MockClient under RIVIAN_CLIENT=mock) so
// concurrent Login/Restore from different sessions cannot corrupt
// each other's tokens.
//
// This replaces the singleton Account that v0.17.x carried at the
// top of api.Deps. The shared singleton was the root cause of two
// classes of bugs:
//
//  1. rivianHydrateMW restoring the request user's session into a
//     shared LiveClient — last-write-wins across concurrent users.
//  2. Boot-time pods coming up with no session at all because no
//     authenticated UI request had triggered hydration yet, leaving
//     the StateMonitor goroutines busy-looping on
//     ErrNotAuthenticated until someone opened the app.
//
// Implementations must be safe for concurrent use; resolution is
// idempotent (the same userID returns the same Account instance for
// the lifetime of the registry).
type AccountRegistry interface {
	// For returns the Account bound to userID, constructing one on
	// first sight. Returns nil only when the registry was wired
	// with a session-less client (rivian.Stub).
	For(userID uuid.UUID) Account
	// Loaded returns every userID currently held in the registry.
	// Used by metrics + the boot hydrator. Order is unspecified.
	Loaded() []uuid.UUID
}

// liveAccountRegistry is the production implementation: one
// *LiveClient per user, lazily constructed, never evicted. At the
// designed scale (≤50 users in the homelab path, low hundreds in
// the SaaS path) the memory footprint of one struct + one TLS
// session per user is negligible against the rate-limit headroom
// LRU eviction would buy back.
type liveAccountRegistry struct {
	build func() *LiveClient

	mu    sync.RWMutex
	users map[uuid.UUID]*LiveClient
}

// NewLiveAccountRegistry returns a registry whose For(uid) calls
// produce a fresh *LiveClient on first sight by invoking build().
//
// build is the operator-supplied factory (typically a closure over
// rivian.NewLive().WithRivoltVersion(...).WithBreaker(...)... etc.)
// so every per-user client gets the same breaker/limiter/kill-switch
// wiring that the singleton in main.go used to receive.
func NewLiveAccountRegistry(build func() *LiveClient) AccountRegistry {
	return &liveAccountRegistry{
		build: build,
		users: make(map[uuid.UUID]*LiveClient),
	}
}

func (r *liveAccountRegistry) For(uid uuid.UUID) Account {
	r.mu.RLock()
	c, ok := r.users[uid]
	r.mu.RUnlock()
	if ok {
		return c
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check inside the write lock so two concurrent For()
	// calls on the same uid return the same instance.
	if c, ok := r.users[uid]; ok {
		return c
	}
	c = r.build()
	r.users[uid] = c
	return c
}

func (r *liveAccountRegistry) Loaded() []uuid.UUID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]uuid.UUID, 0, len(r.users))
	for uid := range r.users {
		out = append(out, uid)
	}
	return out
}

// mockAccountRegistry is the RIVIAN_CLIENT=mock equivalent: per-user
// *MockClient. Tests instantiate this directly.
type mockAccountRegistry struct {
	build func() *MockClient

	mu    sync.RWMutex
	users map[uuid.UUID]*MockClient
}

// NewMockAccountRegistry returns a registry that constructs a fresh
// *MockClient per user via build(). Used under RIVIAN_CLIENT=mock so
// the multi-user UI flow works in local dev without standing up real
// Rivian credentials.
func NewMockAccountRegistry(build func() *MockClient) AccountRegistry {
	return &mockAccountRegistry{
		build: build,
		users: make(map[uuid.UUID]*MockClient),
	}
}

func (r *mockAccountRegistry) For(uid uuid.UUID) Account {
	r.mu.RLock()
	c, ok := r.users[uid]
	r.mu.RUnlock()
	if ok {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.users[uid]; ok {
		return c
	}
	c = r.build()
	r.users[uid] = c
	return c
}

func (r *mockAccountRegistry) Loaded() []uuid.UUID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]uuid.UUID, 0, len(r.users))
	for uid := range r.users {
		out = append(out, uid)
	}
	return out
}

// nopAccountRegistry is wired when RIVIAN_CLIENT=stub (no network).
// For() returns nil; api handlers gate on that to short-circuit the
// Rivian-touching surface without a special case at every call site.
type nopAccountRegistry struct{}

// NewNopAccountRegistry returns a registry that always returns nil.
// Used with the stub client.
func NewNopAccountRegistry() AccountRegistry { return nopAccountRegistry{} }

func (nopAccountRegistry) For(uuid.UUID) Account   { return nil }
func (nopAccountRegistry) Loaded() []uuid.UUID     { return nil }

// liveClientFromAccount returns the underlying *LiveClient if a is
// one. Used by the StateMonitor to access live-only methods
// (SubscribeVehicleState, State) without leaking the concrete type
// up through Account. Returns nil when a is nil or backed by mock.
func liveClientFromAccount(a Account) *LiveClient {
	if a == nil {
		return nil
	}
	if lc, ok := a.(*LiveClient); ok {
		return lc
	}
	return nil
}
