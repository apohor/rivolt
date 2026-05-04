package api

import (
	"context"
	"net/http"

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
