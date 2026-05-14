package api

// Admin user-management handlers. Mounted under /api/admin/* with
// requireAdminMW; assume role='admin' on the caller. Errors map
// to plain JSON {"error": "..."} so the SPA can show a toast
// without a special parser.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/idp"
	"github.com/apohor/rivolt/internal/rivian"
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
// Body: {"email": "...", "display_name": "...", "role": "user"|"admin", "disabled": bool}
//
// Email is the canonical identifier (matches Kratos's identity
// schema, which registers email as the password credential
// identifier). The rivolt row's stable internal handle (`username`)
// is derived from the email local-part, so the deterministic
// UUIDv5 stays consistent across logins. `display_name` is
// optional — defaults to email — so admins can add a teammate
// without inventing a label. Older callers may still send
// `username` explicitly; if present it overrides the local-part
// derivation, but new code should not.
//
// `disabled: true` lets an admin pre-block an account before the
// user has ever signed in (e.g. revoking access for a departing
// employee whose IdP entry is still alive). The auth Middleware
// disabled-gate refuses to mint a session for the row.
//
// When an idp.UserProvider is wired in (Kratos), the handler also
// provisions the user in the IdP, generates a one-time random
// password, and returns it in the 201 response under `password`.
// The plaintext is shown to the admin once and never persisted on
// rivolt's side — the admin is responsible for delivering it to
// the user out-of-band. If IdP provisioning fails AFTER the rivolt
// row is created, we delete the rivolt row to avoid leaving a
// half-provisioned account, then surface the error.
func handleAdminUserCreate(d *sql.DB, ac idp.UserProvider, log *slog.Logger) http.HandlerFunc {
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
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		body.Username = strings.TrimSpace(body.Username)
		body.DisplayName = strings.TrimSpace(body.DisplayName)
		// Email is required. Derive a stable rivolt-side handle from
		// the local-part when the caller doesn't supply one (new
		// shape) and default display_name to the email so the form
		// only really needs one field plus role.
		if body.Email == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "email is required"})
			return
		}
		if body.Username == "" {
			body.Username = emailLocalPart(body.Email)
		}
		if body.DisplayName == "" {
			body.DisplayName = body.Email
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
		resp := map[string]any{"id": id.String()}
		if ac != nil && ac.Enabled() {
			pwd, err := ac.CreateUserGeneratePassword(r.Context(), idp.CreateRequest{
				Username:    body.Username,
				Email:       body.Email,
				DisplayName: body.DisplayName,
				Role:        body.Role,
			})
			if err != nil && pwd == "" {
				if derr := db.DeleteUser(r.Context(), d, id); derr != nil && log != nil {
					log.Error("admin: rollback after idp create failed",
						"id", id.String(), "err", derr.Error())
				}
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
				return
			}
			if err != nil && log != nil {
				log.Warn("admin: idp create partially failed (user written, sync may be delayed)",
					"id", id.String(), "err", err.Error())
			}
			resp["password"] = pwd
			resp["idp_provisioned"] = true
		} else {
			resp["idp_provisioned"] = false
		}
		writeJSON(w, http.StatusCreated, resp)
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
//
// Also removes the user from the IdP backend (Kratos) when one is
// wired in. Best-effort: if the rivolt-side delete succeeds but
// IdP removal fails, the operation returns 200 — leaving an orphan
// IdP entry that no longer matches a rivolt user (and therefore
// can't sign in past the
// EnsureUser bootstrap gate). The error is logged so the admin
// can clean up via the script.
func handleAdminUserDelete(d *sql.DB, ac idp.UserProvider, log *slog.Logger) http.HandlerFunc {
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
		// Resolve the username BEFORE the cascade delete so we
		// can still tell the IdP who to drop after the rivolt
		// row is gone.
		var username string
		if ac != nil && ac.Enabled() {
			u, uerr := db.RawUsernameByID(r.Context(), d, target)
			if uerr != nil && log != nil {
				log.Warn("admin: lookup username for idp delete failed",
					"id", target.String(), "err", uerr.Error())
			}
			username = u
		}
		if err := db.DeleteUser(r.Context(), d, target); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if ac != nil && ac.Enabled() && username != "" {
			if err := ac.DeleteUser(r.Context(), username); err != nil && log != nil {
				log.Warn("admin: idp delete failed (rivolt row already removed)",
					"username", username, "err", err.Error())
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleAdminUserSyncRivian — POST /api/admin/users/{id}/sync-rivian
//
// Force-runs primeUserVehicles for the target user from the calling
// pod's AccountRegistry. Used to seed the vehicles table for users
// who connected Rivian before the eager-prime fix shipped, without
// asking them to reload the app. Idempotent — re-running on a user
// who already has rows just refreshes their metadata.
func handleAdminUserSyncRivian(reg rivian.AccountRegistry, d *sql.DB, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil || d == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "rivian or db unavailable"})
			return
		}
		target, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid user id"})
			return
		}
		lc := reg.For(target)
		if lc == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no rivian client for user"})
			return
		}
		if !lc.Authenticated() {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "user has no active rivian session"})
			return
		}
		primeUserVehicles(r.Context(), lc, d, target, log)
		var n int
		if err := d.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM vehicles WHERE user_id = $1`, target,
		).Scan(&n); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "vehicle_count": n})
	}
}
