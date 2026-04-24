package api

import (
	"net/http"

	"github.com/apohor/rivolt/internal/auth"
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
