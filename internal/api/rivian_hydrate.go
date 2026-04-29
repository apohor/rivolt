package api

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

// rivianHydrateMW lazily restores a user's persisted Rivian session
// into the in-memory Account on the first authenticated request. It
// replaces the boot-time `LoadRivianSession(local)` hydration that
// (a) tied the live client to a single legacy user identity and
// (b) silently failed across redeploys for any other identity.
//
// Multi-replica posture: each replica builds its own per-user cache
// against the shared Postgres `user_secrets` table (sealed under the
// shared KEK from External Secrets). No cross-replica coordination
// is required — replica restarts and scale-outs simply re-hydrate
// on the next request from each user.
//
// Today the data plane still shares a single in-memory Account
// across all callers, so "hydrate" means Restore() into that one
// instance the first time we see a given user. When the per-user
// client cache lands (Phase 2 multi-replica work), only the
// resolver inside `hydrate` changes — the middleware contract
// stays put.
//
// nil account or nil store → pass through (mock-less tests, stub
// client, secrets sealer disabled).
func rivianHydrateMW(account rivian.Account, store *secrets.Store, logger *slog.Logger) func(http.Handler) http.Handler {
	hyd := newRivianHydrator(account, store, logger)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hyd.ensure(r)
			next.ServeHTTP(w, r)
		})
	}
}

// rivianHydrator caches the set of user IDs we've already attempted
// to restore for, so subsequent requests don't re-hit Postgres.
// "Attempted" includes the no-row case — a user who has never
// linked Rivian shouldn't pay for a SELECT on every request.
//
// The cache is process-local; on replica restart we rebuild it from
// scratch, which is fine: the first request after restart pays the
// SELECT, the rest are free.
type rivianHydrator struct {
	account rivian.Account
	store   *secrets.Store
	logger  *slog.Logger

	mu     sync.Mutex
	tried  map[uuid.UUID]struct{}
	loaded uuid.UUID // user_id whose session is currently in the shared account, if any
}

func newRivianHydrator(account rivian.Account, store *secrets.Store, logger *slog.Logger) *rivianHydrator {
	return &rivianHydrator{
		account: account,
		store:   store,
		logger:  logger,
		tried:   make(map[uuid.UUID]struct{}),
	}
}

// ensure restores the request user's persisted Rivian session into
// the shared Account, exactly once per (user_id, replica lifetime).
// No-ops when prerequisites are missing.
func (h *rivianHydrator) ensure(r *http.Request) {
	if h == nil || h.account == nil || h.store == nil {
		return
	}
	uid, ok := auth.UserFromContext(r.Context())
	if !ok || uid == uuid.Nil {
		return
	}

	h.mu.Lock()
	if _, seen := h.tried[uid]; seen {
		h.mu.Unlock()
		return
	}
	// Mark as attempted before we drop the lock — duplicate
	// concurrent requests for the same user shouldn't all stampede
	// the secrets store. If the load fails, ensure() still won't
	// retry until the next process boot; that matches the prior
	// behavior, where a corrupt blob meant "log in again".
	h.tried[uid] = struct{}{}
	h.mu.Unlock()

	sess, err := secrets.LoadRivianSession(r.Context(), h.store, uid)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("rivian session hydrate failed", "user_id", uid.String(), "err", err.Error())
		}
		return
	}
	if sess.UserSessionToken == "" {
		// User hasn't linked Rivian. Nothing to do.
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	// Today's data plane has a single shared LiveClient; the last
	// hydrating user wins. That's correct for single-tenant /
	// single-replica deployments and matches the pre-existing
	// behavior. The per-user client cache that lifts this constraint
	// lands with the multi-replica trio (lease reconciliation,
	// reconnect controls, token bucket).
	if h.loaded == uid {
		return
	}
	h.account.Restore(sess)
	h.loaded = uid
	if h.logger != nil {
		h.logger.Info("rivian session restored", "user_id", uid.String(), "email", sess.Email)
	}
}
