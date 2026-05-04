package api

// Admin user-management handlers. Mounted under /api/admin/* with
// requireAdminMW; assume role='admin' on the caller. Errors map
// to plain JSON {"error": "..."} so the SPA can show a toast
// without a special parser.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// handleAdminUsersList — GET /api/admin/users
//
// Returns every user with role + display fields. Used by the
// admin SPA to render the user table. No paging (see ListUsers
// ForAdmin).
func handleAdminUsersList(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db unavailable"})
			return
		}
		users, err := db.ListUsersForAdmin(r.Context(), d)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if users == nil {
			users = []db.AdminUserRow{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": users})
	}
}

// handleAdminUserCreate — POST /api/admin/users
// Body: {"username": "...", "email": "...", "display_name": "...", "role": "user"|"admin", "disabled": bool}
//
// Pre-provisions a user row keyed by the deterministic UUIDv5 of
// the username. Auth is OIDC-only — this does NOT issue a password.
// When the user later signs in via OIDC with a matching
// preferred_username, EnsureUserFull lands on this row and the
// pre-set role/email/display_name survive.
//
// `disabled: true` lets an admin pre-block a username before the
// user has ever attempted to sign in (e.g. revoking access for a
// departing employee whose IdP entry is still alive). The Middleware
// disabled-gate refuses to mint a session for the row.
func handleAdminUserCreate(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db unavailable"})
			return
		}
		var body struct {
			Username    string `json:"username"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			Role        string `json:"role"`
			Disabled    bool   `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
			return
		}
		id, err := db.CreateUser(r.Context(), d, body.Username, body.Email, body.DisplayName, body.Role)
		if err != nil {
			if errors.Is(err, db.ErrUserExists) {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "user already exists"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		// Apply the optional disabled flag in a follow-up
		// UPDATE. CreateUser intentionally doesn't take it as
		// a parameter so the bootstrap-admin trigger can't see
		// the column on INSERT — keeping the create path
		// minimal makes the trigger logic easier to reason
		// about.
		if body.Disabled {
			if err := db.SetDisabled(r.Context(), d, id, true); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id.String()})
	}
}

// handleAdminUserSetDisabled — POST /api/admin/users/{id}/disabled
// Body: {"disabled": bool}
//
// Disabling a user revokes every existing session on the next
// request (the auth Middleware re-checks on each call). Refuses
// to disable the caller (an admin can't lock themselves out;
// ask another admin) and refuses to disable the last admin
// (would orphan the install). Same guards as the role demote /
// delete endpoints, on purpose — these are three flavours of
// the same "remove privilege" operation.
func handleAdminUserSetDisabled(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db unavailable"})
			return
		}
		target, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid user id"})
			return
		}
		var body struct {
			Disabled bool `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
			return
		}
		caller, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if body.Disabled && caller == target {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "cannot disable your own account"})
			return
		}
		if body.Disabled {
			role, err := db.RoleFor(r.Context(), d, target)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			if role == "admin" {
				n, err := db.CountAdmins(r.Context(), d)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
					return
				}
				if n <= 1 {
					writeJSON(w, http.StatusConflict, map[string]any{"error": "cannot disable the last admin"})
					return
				}
			}
		}
		if err := db.SetDisabled(r.Context(), d, target, body.Disabled); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleAdminUserSetRole — POST /api/admin/users/{id}/role
// Body: {"role": "user" | "admin"}.
//
// Refuses to demote the last remaining admin — that would lock
// the install out of every /api/admin/* surface (including this
// one). Self-demotion is allowed as long as another admin
// exists; the SPA still warns about it.
func handleAdminUserSetRole(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db unavailable"})
			return
		}
		uid, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid user id"})
			return
		}
		var body struct {
			Role string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
			return
		}
		// Refuse to demote the last admin. We check before the
		// UPDATE because doing it after would race with another
		// admin doing the same thing — the second writer would
		// see "1 admin" and proceed.
		if body.Role == "user" {
			cur, err := db.RoleFor(r.Context(), d, uid)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			if cur == "admin" {
				n, err := db.CountAdmins(r.Context(), d)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
					return
				}
				if n <= 1 {
					writeJSON(w, http.StatusConflict, map[string]any{"error": "cannot demote the last admin"})
					return
				}
			}
		}
		if err := db.SetRole(r.Context(), d, uid, body.Role); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleAdminUserDelete — DELETE /api/admin/users/{id}
//
// Cascade-deletes the user via FK ON DELETE CASCADE. Refuses to
// delete the caller (an admin can't suicide; ask a different
// admin) and refuses to delete the last admin.
func handleAdminUserDelete(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db unavailable"})
			return
		}
		target, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid user id"})
			return
		}
		caller, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if caller == target {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "cannot delete your own account"})
			return
		}
		role, err := db.RoleFor(r.Context(), d, target)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if role == "admin" {
			n, err := db.CountAdmins(r.Context(), d)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			if n <= 1 {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "cannot delete the last admin"})
				return
			}
		}
		if err := db.DeleteUser(r.Context(), d, target); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
