package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/sessions"
)

// SessionsStore is the part of sessions.Store the device list uses.
type SessionsStore interface {
	List(ctx context.Context, userID uuid.UUID) ([]sessions.Session, error)
	RevokeOwned(ctx context.Context, userID, id uuid.UUID) (bool, error)
	RevokeOthers(ctx context.Context, userID, keepID uuid.UUID) (int64, error)
}

// SessionsAPI serves the signed-in user's own sessions: the browsers and the iPhone
// app that are signed in as them, and "sign out everywhere else" (iOS design §2,
// DA 5). Never the token hash or the IP address.
type SessionsAPI struct {
	Store SessionsStore
	// Current returns the id of the session making the request, when it has one
	// (a cookie or app session; not header/Kratos/bypass auth). nil or false:
	// "current" is not marked and sign-out-everywhere-else is refused, since it
	// could not tell which session to keep.
	Current func(r *http.Request) (uuid.UUID, bool)
}

type sessionJSON struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`
	DeviceLabel string    `json:"device_label"`
	UserAgent   string    `json:"user_agent,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Current     bool      `json:"current"`
}

// user is the caller, refusing an admin who is viewing as someone else: another
// person's device list is not theirs to see or change.
func (a *SessionsAPI) user(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	uid, ok := auth.UserFromContext(r.Context())
	if !ok || uid == uuid.Nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return uuid.Nil, false
	}
	if auth.IsImpersonating(r.Context()) {
		http.Error(w, "not available while viewing as another user", http.StatusForbidden)
		return uuid.Nil, false
	}
	return uid, true
}

func (a *SessionsAPI) current(r *http.Request) (uuid.UUID, bool) {
	if a.Current == nil {
		return uuid.Nil, false
	}
	return a.Current(r)
}

// List serves GET /api/auth/sessions.
func (a *SessionsAPI) List(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.user(w, r)
	if !ok {
		return
	}
	rows, err := a.Store.List(r.Context(), uid)
	if err != nil {
		http.Error(w, "couldn't read sessions", http.StatusInternalServerError)
		return
	}
	cur, _ := a.current(r)
	out := make([]sessionJSON, 0, len(rows))
	for _, s := range rows {
		out = append(out, sessionJSON{
			ID: s.ID, Kind: s.Kind, DeviceLabel: s.DeviceLabel, UserAgent: truncateRunes(s.UserAgent, 200),
			CreatedAt: s.CreatedAt.UTC(), LastSeenAt: s.LastSeenAt.UTC(), ExpiresAt: s.ExpiresAt.UTC(),
			Current: cur != uuid.Nil && s.ID == cur,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// Revoke serves DELETE /api/auth/sessions/{id}: one of the caller's own sessions.
// Revoking the current one signs this device out.
func (a *SessionsAPI) Revoke(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.user(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	done, err := a.Store.RevokeOwned(r.Context(), uid, id)
	if err != nil {
		http.Error(w, "couldn't revoke", http.StatusInternalServerError)
		return
	}
	if !done {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevokeOthers serves POST /api/auth/sessions/revoke-others: sign out everywhere
// except the session making the request.
func (a *SessionsAPI) RevokeOthers(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.user(w, r)
	if !ok {
		return
	}
	cur, known := a.current(r)
	if !known || cur == uuid.Nil {
		http.Error(w, "this sign-in has no session to keep; sign in with a browser or the app first", http.StatusConflict)
		return
	}
	n, err := a.Store.RevokeOthers(r.Context(), uid, cur)
	if err != nil {
		http.Error(w, "couldn't revoke", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
