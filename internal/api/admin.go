package api

// Admin user-management handlers. Mounted under /api/admin/* with
// requireAdminMW; assume role='admin' on the caller. Errors map
// to plain JSON {"error": "..."} so the SPA can show a toast
// without a special parser.

import (
	"database/sql"
	"encoding/json"
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
