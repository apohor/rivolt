package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/google/uuid"
)

// impersonationHeader is the request header an admin's browser sets to
// render the app as another (non-admin) user. See impersonationMW.
const impersonationHeader = "X-Rivolt-Impersonate"

// requireUserMW is chi middleware that 401s any request the auth
// package hasn't resolved an identity for. It's separate from
// auth.Service.RequireUser (which wraps a single handler) because
// chi.Router.Use expects the standard Handler→Handler shape so we
// can apply it once to a Group.
//
// The check intentionally reads only from request context — the
// actual cookie-or-header resolution has already happened in
// auth.Service.Middleware earlier in the chain. Keeping this thin
// means swapping the auth issuer (OIDC, SSO, …) doesn't require
// touching any route wiring.
func requireUserMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.UserFromContext(r.Context()); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAdminMW gates a route group on `users.role = 'admin'`.
// Layered on top of requireUserMW (which has already established
// identity) — this just upgrades the gate from "any user" to
// "admin-only".
//
// roleLookup is the indirection that keeps internal/api free of a
// direct dependency on internal/db. main.go wires it to a closure
// over db.RoleFor; tests can stub.
//
// Returns 401 if there is no user in context (defense in depth —
// requireUserMW should have already 401'd), 403 when the user is
// not an admin, and 5xx if the role lookup itself errors. We treat
// a non-nil error as "fail closed" rather than risking a privilege
// upgrade on a transient DB blip.
func requireAdminMW(roleLookup func(ctx context.Context, uid uuid.UUID) (string, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := auth.UserFromContext(r.Context())
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// No privilege chaining: an impersonated request (admin
			// viewing as another user) may never reach an admin handler,
			// even for a GET. impersonationMW only ever swaps in a
			// non-admin target, so the role check below would also 403 —
			// this is the explicit, defence-in-depth refusal.
			if _, impersonating := auth.ImpersonatorFromContext(r.Context()); impersonating {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if roleLookup == nil {
				http.Error(w, "admin gate not configured", http.StatusServiceUnavailable)
				return
			}
			role, err := roleLookup(r.Context(), uid)
			if err != nil {
				http.Error(w, "admin gate lookup failed", http.StatusInternalServerError)
				return
			}
			if role != "admin" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// impersonationMW lets an admin render the app as another user for
// support/debugging, with zero write power. It runs after the auth
// middleware has resolved the real identity (and after requireUserMW),
// so any caller reaching it is a known user.
//
// The header is honoured only when ALL hold: the feature is enabled,
// the caller is an admin, and X-Rivolt-Impersonate names an existing
// non-admin user. A non-admin's header is ignored outright (never
// trusted — served as themselves); an admin's bad header gets a 4xx so
// the UI sees a clear signal.
//
// Guardrails enforced here:
//   - read-only: an impersonated request may only be a GET; any other
//     method is 405. The admin can look, not mutate the target's data.
//   - no admin targets: impersonating an admin is 403. Since the caller
//     is itself an admin, this also makes self-impersonation a 403.
//
// The /api/admin/* refusal lives in requireAdminMW (it 403s any
// impersonated request), so an impersonated context never reaches an
// admin handler. On success the context user id is swapped to the
// target — every downstream store/factory/RLS then renders the
// target's data transparently — and the admin id is recorded as the
// impersonator for the audit log and the guardrails above.
func impersonationMW(roleLookup func(ctx context.Context, uid uuid.UUID) (string, error), enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := strings.TrimSpace(r.Header.Get(impersonationHeader))
			if raw == "" || !enabled || roleLookup == nil {
				next.ServeHTTP(w, r)
				return
			}
			admin, ok := auth.UserFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			role, err := roleLookup(r.Context(), admin)
			if err != nil {
				http.Error(w, "impersonation gate lookup failed", http.StatusInternalServerError)
				return
			}
			// Never trust the header from a non-admin: ignore it and
			// serve the caller as themselves.
			if role != "admin" {
				next.ServeHTTP(w, r)
				return
			}
			target, err := uuid.Parse(raw)
			if err != nil || target == uuid.Nil {
				http.Error(w, "invalid impersonation target", http.StatusBadRequest)
				return
			}
			targetRole, err := roleLookup(r.Context(), target)
			if err != nil {
				http.Error(w, "impersonation target lookup failed", http.StatusInternalServerError)
				return
			}
			// role is NOT NULL, so an empty string means the target row
			// doesn't exist — surface that rather than swapping to a
			// ghost uid the admin would see as an empty app.
			if targetRole == "" {
				http.Error(w, "impersonation target not found", http.StatusNotFound)
				return
			}
			if targetRole == "admin" {
				http.Error(w, "cannot impersonate an admin", http.StatusForbidden)
				return
			}
			if r.Method != http.MethodGet {
				http.Error(w, "impersonation is read-only", http.StatusMethodNotAllowed)
				return
			}
			ctx := auth.WithImpersonator(r.Context(), admin)
			ctx = auth.WithUser(ctx, target)
			slog.InfoContext(ctx, "impersonation",
				"impersonator", admin.String(),
				"target", target.String(),
				"method", r.Method,
				"path", r.URL.Path)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
