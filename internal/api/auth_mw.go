package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/google/uuid"
)

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
			// Defense in depth against privilege chaining: even if a
			// future admin route somehow lands here with an
			// impersonated context (the impersonation middleware
			// already refuses /api/admin/* outright, and the target
			// of an impersonation can never itself be an admin), an
			// impersonated caller never passes the admin gate. See
			// auth.IsImpersonating.
			if auth.IsImpersonating(r.Context()) {
				http.Error(w, "forbidden while impersonating", http.StatusForbidden)
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

// ImpersonateHeader is the header an admin sets to view the app
// exactly as another user sees it — read-only, for support and
// debugging (e.g. confirming what a user's vehicle card actually
// shows them). Trusted ONLY when the caller already carries
// role=admin via the real cookie/OIDC session established earlier
// in the chain; see impersonationMW.
const ImpersonateHeader = "X-Rivolt-Impersonate"

// impersonationMW lets an authenticated admin swap the request's
// active identity to another user's for the duration of one
// request, driven by the ImpersonateHeader. It must run after
// auth.Service.Middleware (which resolves the real identity) and
// before every route that reads auth.UserFromContext — the swap is
// then invisible to downstream handlers, stores, and Postgres RLS,
// which all key off that same context value.
//
// Guardrails, checked in order once a header is present:
//
//  1. RIVOLT_IMPERSONATION_DISABLED (disabled()): header ignored
//     outright, regardless of caller.
//  2. The caller must already be an admin, checked against the
//     REAL pre-swap uid. Non-admins: the header is silently
//     ignored — no error response, so a non-admin probing for the
//     mechanism learns nothing from the response shape.
//  3. No privilege chaining: /api/admin/* is refused (403) before
//     any identity swap happens. requireAdminMW additionally
//     refuses any already-impersonated context as a second layer.
//  4. Read-only: any method other than GET/HEAD is refused (405).
//  5. Cannot impersonate another admin (403) — bounds the blast
//     radius of a compromised admin session to non-admin data.
//
// On success both ids are stamped into context via
// auth.WithImpersonation (identity swap + audit fields for every
// subsequent log line) and one explicit "impersonation" audit line
// is emitted here.
func impersonationMW(roleLookup func(ctx context.Context, uid uuid.UUID) (string, error), disabled func() bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := strings.TrimSpace(r.Header.Get(ImpersonateHeader))
			if hdr == "" {
				next.ServeHTTP(w, r)
				return
			}
			if disabled != nil && disabled() {
				next.ServeHTTP(w, r)
				return
			}
			callerID, ok := auth.UserFromContext(r.Context())
			if !ok || roleLookup == nil {
				next.ServeHTTP(w, r)
				return
			}
			callerRole, err := roleLookup(r.Context(), callerID)
			if err != nil || callerRole != "admin" {
				// Never trust the header unless the resolved caller
				// is a confirmed admin. A lookup error fails closed
				// the same way — silently ignoring the header, not
				// surfacing an error that would confirm the header
				// is even inspected.
				next.ServeHTTP(w, r)
				return
			}
			targetID, err := uuid.Parse(hdr)
			if err != nil {
				http.Error(w, "invalid impersonation target", http.StatusBadRequest)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/admin") {
				http.Error(w, "cannot access admin routes while impersonating", http.StatusForbidden)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "impersonated sessions are read-only", http.StatusMethodNotAllowed)
				return
			}
			targetRole, err := roleLookup(r.Context(), targetID)
			if err != nil {
				http.Error(w, "impersonation target lookup failed", http.StatusInternalServerError)
				return
			}
			if targetRole == "admin" {
				http.Error(w, "cannot impersonate an admin", http.StatusForbidden)
				return
			}
			ctx := auth.WithImpersonation(r.Context(), callerID, targetID)
			slog.InfoContext(ctx, "impersonation",
				"impersonator_id", callerID.String(),
				"target_id", targetID.String(),
				"method", r.Method,
				"path", r.URL.Path,
			)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
