package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strconv"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
)

// userFencer answers for a user's group (fence.Holder satisfies it).
type userFencer interface {
	FenceUser(ctx context.Context, uid string) (fence.Token, fence.State, error)
}

// ungroupedFencer tells whether a user has no group at all (fence.Holder satisfies it).
type ungroupedFencer interface {
	UserUngrouped(ctx context.Context, uid string) (bool, error)
}

// reauthOwnerCtx is the context a needs_reauth raise runs under (single-owner M4-3). When this pod holds the user's group (Held, or Warming: the
// raise is a write the epoch CAS orders) the group's fence token goes on it, so the raise is guarded by the epoch; a pod that has lost the group
// writes nothing. When lead mode is off the context is unchanged. Anything else (the group is held elsewhere, or cannot be told) gets no token:
// the raise is then unguarded as before, or refused with db.ErrNotOwner once raises are owner-only (RIVOLT_SINGLE_OWNER).
func reauthOwnerCtx(ctx context.Context, f userFencer, uid string) context.Context {
	if f == nil {
		return ctx
	}
	tok, st, err := f.FenceUser(ctx, uid)
	if err != nil {
		return ctx
	}
	if st == fence.Held || st == fence.Warming {
		return fence.With(ctx, tok)
	}
	// A user with no vehicle row has no group and so no owner, ever (the discovery job is who sees their dead session): their rejection is the
	// only one there will be, so it may raise. The raise is still guarded by the session version and the rising-edge update.
	if st == fence.Lost {
		if g, ok := f.(ungroupedFencer); ok {
			if none, err := g.UserUngrouped(ctx, uid); err == nil && none {
				return fence.Unowned(ctx)
			}
		}
	}
	return ctx
}

// reauthCarries is the label of rivolt_reauth_raise_attempts_total: what the raise's context carries.
func reauthCarries(ctx context.Context) string {
	if _, ok := fence.From(ctx); ok {
		return "token"
	}
	if fence.IsUnowned(ctx) {
		return "unowned"
	}
	return "none"
}

// reauthRefusedReason is the label of rivolt_reauth_raise_refused_total for a raise that was refused.
func reauthRefusedReason(err error) string {
	if errors.Is(err, fence.ErrFenced) {
		return "fenced"
	}
	return "not_owner"
}

// envInt reads a non-negative integer setting; unset or invalid is def (0 lets the callee use its own default).
func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

// reauthSinkDeps is what a per-user client's re-auth sink needs. main.go builds every sink from it, and the integration test builds the same one
// over real Postgres and lease stores, so the test exercises the wiring and not a hand-made context.
type reauthSinkDeps struct {
	pool     *sql.DB
	fencer   userFencer
	metrics  *metrics.Metrics
	logger   *slog.Logger
	synced   func() bool         // an account syncer is installed: the raise is checked against the stored session version
	onRaised func(reason string) // the raise was a new transition (the one-shot email)
}

// newReauthSink is the sink for one user's client: the empty reason clears the flag; any other raises it, as the group's owner only once
// RIVOLT_SINGLE_OWNER is on. A refused raise (this pod does not hold the group) is counted and is not an error.
func newReauthSink(d reauthSinkDeps, uid uuid.UUID) func(ctx context.Context, reason string) {
	return func(sinkCtx context.Context, reason string) {
		// Empty reason is the clear signal (successful Login).
		if reason == "" {
			if err := db.SetNeedsReauth(sinkCtx, d.pool, uid, ""); err != nil {
				d.logger.Warn("clear needs_reauth", "user_id", uid.String(), "err", err.Error())
			}
			return
		}
		// Only if the session this client failed with is still the stored one: a
		// pod that has not yet caught up with a sign-in elsewhere must not flag the
		// user, nor send the email, right after they signed in.
		var transitioned bool
		var err error
		sinkCtx = reauthOwnerCtx(sinkCtx, d.fencer, uid.String()) // M4-3: the raise carries the group's epoch when this pod owns the user
		if d.synced() {
			loaded := rivian.SessionVersionFrom(sinkCtx)
			if loaded.IsZero() {
				// The client does not know which stored session it holds yet; the
				// next sync records it and the next rejection raises.
				d.metrics.ReauthRaiseDeferred.Inc()
				d.logger.Info("rivian reauth raise deferred: session version unknown", "user_id", uid.String())
				return
			}
			d.metrics.ReauthRaiseAttempts.WithLabelValues(reauthCarries(sinkCtx)).Inc()
			transitioned, err = db.RaiseNeedsReauthIfCurrent(sinkCtx, d.pool, uid, reason, loaded)
		} else {
			// No account sync (no secrets store): there is no stored session to be
			// stale against.
			d.metrics.ReauthRaiseAttempts.WithLabelValues(reauthCarries(sinkCtx)).Inc()
			transitioned, err = db.RaiseNeedsReauth(sinkCtx, d.pool, uid, reason)
		}
		if errors.Is(err, db.ErrNotOwner) || errors.Is(err, fence.ErrFenced) {
			// This pod does not hold the user's group (or has just lost it): its rejection says nothing the owner has not seen. Not a failure.
			d.metrics.ReauthRaiseRefused.WithLabelValues(reauthRefusedReason(err)).Inc()
			return
		}
		if err != nil {
			d.logger.Warn("persist needs_reauth", "user_id", uid.String(), "reason", reason, "err", err.Error())
			return
		}
		if transitioned && d.onRaised != nil {
			d.onRaised(reason)
		}
	}
}
