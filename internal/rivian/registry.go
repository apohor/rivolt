package rivian

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

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
// designed scale (≤50 users self-hosted, low hundreds in the SaaS
// path) the memory footprint of one struct + one TLS
// session per user is negligible against the rate-limit headroom
// LRU eviction would buy back.
type liveAccountRegistry struct {
	build func(uuid.UUID) *LiveClient

	// sync, when attached, reconciles an account with the database before it is
	// handed out (see AccountSyncer).
	sync atomic.Pointer[AccountSyncer]

	// gate, when set, is single-owner M4-5 (RIVOLT_NO_STANDING_TOKENS): only a user whose group this pod holds has a client here.
	gate atomic.Pointer[tokenGate]

	mu    sync.RWMutex
	users map[uuid.UUID]*LiveClient
}

// tokenGate is what a registry needs to keep session tokens only for the groups its pod holds.
type tokenGate struct {
	// hold says whether this pod holds uid's group. An answer that cannot be had is "not held".
	hold func(context.Context, uuid.UUID) (held, known bool)
	// bare builds a client with no stored session restored (ForLogin).
	bare func(uuid.UUID) *LiveClient
}

// gateLookupTimeout bounds the group lookup a For call makes.
const gateLookupTimeout = 2 * time.Second

// TokenGated is implemented by a registry that may answer nil for a user it does not hold; callers use it to tell "not held" from "no live
// client configured".
type TokenGated interface {
	NoStandingTokens() bool
}

// NewLiveAccountRegistry returns a registry whose For(uid) calls
// produce a fresh *LiveClient on first sight by invoking build(uid).
//
// build is the operator-supplied factory (typically a closure over
// rivian.NewLive().WithRivoltVersion(...).WithBreaker(...)... etc.)
// so every per-user client gets the same breaker/limiter/kill-switch
// wiring that the singleton in main.go used to receive. The uid
// argument lets the closure thread it through user-scoped sinks
// (e.g. WithReauthSink that persists needs_reauth keyed by user_id).
func NewLiveAccountRegistry(build func(uuid.UUID) *LiveClient) AccountRegistry {
	return &liveAccountRegistry{
		build: build,
		users: make(map[uuid.UUID]*LiveClient),
	}
}

// SetNoStandingTokens turns on single-owner M4-5: For, Raw and the lazy build answer a literal nil (never a typed nil, never a stored one)
// for a user whose group hold says this pod does not hold, so no session token is decrypted for it. bare builds the throwaway client of
// ForLogin, which restores nothing. Call it before the registry is used.
func (r *liveAccountRegistry) SetNoStandingTokens(hold func(context.Context, uuid.UUID) (held, known bool), bare func(uuid.UUID) *LiveClient) {
	r.gate.Store(&tokenGate{hold: hold, bare: bare})
}

// NoStandingTokens reports whether the gate is on.
func (r *liveAccountRegistry) NoStandingTokens() bool { return r.gate.Load() != nil }

// held reports whether uid may have a client here: always without the gate.
func (r *liveAccountRegistry) held(uid uuid.UUID) bool {
	g := r.gate.Load()
	if g == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), gateLookupTimeout)
	defer cancel()
	held, _ := g.hold(ctx, uid)
	return held
}

// ForLogin returns the account a sign-in (password leg, OTP leg) works on, and done, which the caller runs once the session is stored. A
// user this pod holds gets the registered client and done does nothing. Any other user gets a throwaway client that restores no stored
// session and is wiped by done, so a sign-in on a pod that does not own the account leaves no token behind; the owner adopts the stored
// session. Without the gate it is For.
func (r *liveAccountRegistry) ForLogin(uid uuid.UUID) (Account, func()) {
	g := r.gate.Load()
	if g == nil || r.held(uid) {
		return r.For(uid), func() {}
	}
	c := g.bare(uid)
	return c, c.Zero
}

// ReleaseUnheld wipes and drops every client whose group this pod definitely no longer holds (the lookup answered, and said not held). An
// answer that cannot be had keeps the client: a database blip must not strip the owner of its tokens. It returns how many it dropped.
func (r *liveAccountRegistry) ReleaseUnheld(ctx context.Context) int {
	g := r.gate.Load()
	if g == nil {
		return 0
	}
	n := 0
	for _, uid := range r.Loaded() {
		qctx, cancel := context.WithTimeout(ctx, gateLookupTimeout)
		held, known := g.hold(qctx, uid)
		cancel()
		if held || !known {
			continue
		}
		r.mu.Lock()
		c := r.users[uid]
		delete(r.users, uid)
		r.mu.Unlock()
		if c != nil {
			c.Zero()
			n++
		}
	}
	return n
}

// AttachSyncer makes For reconcile the account with the database first (rate limited
// by the syncer) and returns the lookup that does not, for the syncer's own use.
func (r *liveAccountRegistry) AttachSyncer(s *AccountSyncer) {
	s.forget = r.forget
	r.sync.Store(s)
}

// forget drops uid's account from the registry; a later For builds it afresh.
func (r *liveAccountRegistry) forget(uid uuid.UUID) {
	r.mu.Lock()
	delete(r.users, uid)
	r.mu.Unlock()
}

// Fresh reconciles uid's account with the database now and returns the stored state
// (see AccountSyncer.Fresh). ok is false without a syncer or when the database could not
// be read.
func (r *liveAccountRegistry) Fresh(ctx context.Context, uid uuid.UUID) (AccountDBState, bool) {
	s := r.sync.Load()
	if s == nil {
		return AccountDBState{}, false
	}
	return s.Fresh(ctx, uid)
}

// Raw is For without the sync.
func (r *liveAccountRegistry) Raw(uid uuid.UUID) Account {
	if !r.held(uid) {
		return nil
	}
	return r.lookup(uid)
}

func (r *liveAccountRegistry) For(uid uuid.UUID) Account {
	if !r.held(uid) {
		return nil // a literal nil: never a typed nil in the interface, never an entry in r.users
	}
	a := r.lookup(uid)
	if s := r.sync.Load(); s != nil && a != nil {
		s.Touch(uid)
	}
	return a
}

func (r *liveAccountRegistry) lookup(uid uuid.UUID) Account {
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
	c = r.build(uid)
	r.users[uid] = c
	return c
}

// UnheldTokens counts the clients that hold a session token for a user whose group held says this pod does not hold (single-owner M4-5). held
// runs outside the registry lock: it may query the database.
func (r *liveAccountRegistry) UnheldTokens(held func(uuid.UUID) bool) int {
	r.mu.RLock()
	with := make(map[uuid.UUID]*LiveClient, len(r.users))
	for uid, c := range r.users {
		with[uid] = c
	}
	r.mu.RUnlock()
	n := 0
	for uid, c := range with {
		if c.Authenticated() && !held(uid) {
			n++
		}
	}
	return n
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
	build func(uuid.UUID) *MockClient

	mu    sync.RWMutex
	users map[uuid.UUID]*MockClient
}

// NewMockAccountRegistry returns a registry that constructs a fresh
// *MockClient per user via build(uid). Used under RIVIAN_CLIENT=mock
// so the multi-user UI flow works in local dev without standing up
// real Rivian credentials.
func NewMockAccountRegistry(build func(uuid.UUID) *MockClient) AccountRegistry {
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
	c = r.build(uid)
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

func (nopAccountRegistry) For(uuid.UUID) Account { return nil }
func (nopAccountRegistry) Loaded() []uuid.UUID   { return nil }

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

// ForLogin returns the account a sign-in works on and the done to run after the session is stored (see liveAccountRegistry.ForLogin). A
// registry without the gate answers For and a done that does nothing.
func ForLogin(reg AccountRegistry, uid uuid.UUID) (Account, func()) {
	if l, ok := reg.(interface {
		ForLogin(uuid.UUID) (Account, func())
	}); ok {
		return l.ForLogin(uid)
	}
	return reg.For(uid), func() {}
}

// NoStandingTokens reports whether reg keeps session tokens only for the groups its pod holds (single-owner M4-5).
func NoStandingTokens(reg AccountRegistry) bool {
	g, ok := reg.(TokenGated)
	return ok && g.NoStandingTokens()
}

// unheldClient answers every call with a 503-class outage: the account's tokens are not on this pod (single-owner M4-5).
type unheldClient struct{}

// NewUnheldClient returns the Client a data-plane handler uses for a user whose group this pod does not hold.
func NewUnheldClient() Client { return unheldClient{} }

func notHeld(op string) error {
	return &UpstreamError{Class: ClassOutage, Op: op, Reason: "this server does not hold the vehicle's account"}
}

func (unheldClient) Login(context.Context, Credentials) error { return notHeld("Login") }
func (unheldClient) Vehicles(context.Context) ([]Vehicle, error) {
	return nil, notHeld("Vehicles")
}
func (unheldClient) State(context.Context, string) (*State, error) { return nil, notHeld("State") }
