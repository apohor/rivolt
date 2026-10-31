package fence

import (
	"context"
	"sync/atomic"
	"time"
)

// UserSource resolves a user to the fence state of their group (the lease store implements it); the receipts syncer asks it.
type UserSource interface {
	FenceUser(ctx context.Context, uid string) (Token, State, error)
}

// Holder is the fence that consumers built before the lease store exists read through. It answers from the installed source on
// every call, so installing it late reaches consumers already running. Until then it answers Off, or Lost once Require(true) has
// said lead mode needs a fence: a consumer that starts before the lease store exists (the OTA syncer's on-connect fetch, the Live
// Activity source, the left-open job, the receipts syncer) then does nothing, rather than acting for a group this pod may not hold.
type Holder struct {
	src     atomic.Pointer[Source]
	require atomic.Bool
}

// Set installs the fence source (the lease store).
func (h *Holder) Set(s Source) { h.src.Store(&s) }

// Require says lead mode is on: an uninstalled holder reads Lost, not Off. Call it before any consumer starts.
func (h *Holder) Require(on bool) { h.require.Store(on) }

func (h *Holder) missing() State {
	if h.require.Load() {
		return Lost
	}
	return Off
}

// Fence answers from the installed source.
func (h *Holder) Fence(vehicleID string) (Token, State) {
	if p := h.src.Load(); p != nil {
		return (*p).Fence(vehicleID)
	}
	return Token{}, h.missing()
}

// FenceUser answers for a user's group from the installed source. A source that cannot resolve users counts as missing.
func (h *Holder) FenceUser(ctx context.Context, uid string) (Token, State, error) {
	if p := h.src.Load(); p != nil {
		if u, ok := (*p).(UserSource); ok {
			return u.FenceUser(ctx, uid)
		}
	}
	return Token{}, h.missing(), nil
}

// Deadline forwards the installed source's local lease deadline, none without one.
func (h *Holder) Deadline(vehicleID string) (time.Time, bool) {
	if p := h.src.Load(); p != nil {
		if d, ok := (*p).(Deadliner); ok {
			return d.Deadline(vehicleID)
		}
	}
	return time.Time{}, false
}

// Everything a consumer probes a fence source for.
var (
	_ Source     = (*Holder)(nil)
	_ UserSource = (*Holder)(nil)
	_ Deadliner  = (*Holder)(nil)
)
