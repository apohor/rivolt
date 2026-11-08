package main

import (
	"context"
	"errors"

	"github.com/apohor/rivolt/internal/fence"
)

// userFencer answers for a user's group (fence.Holder satisfies it).
type userFencer interface {
	FenceUser(ctx context.Context, uid string) (fence.Token, fence.State, error)
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
	return ctx
}

// reauthRefusedReason is the label of rivolt_reauth_raise_refused_total for a raise that was refused.
func reauthRefusedReason(err error) string {
	if errors.Is(err, fence.ErrFenced) {
		return "fenced"
	}
	return "not_owner"
}
