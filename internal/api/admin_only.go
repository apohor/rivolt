package api

import (
	"net/http"
	"strings"

	"github.com/apohor/rivolt/internal/auth"
)

// adminOnlyPublic reports whether path may be served by the admin deployment
// (RIVOLT_ADMIN_ONLY): the admin API, sign-in (/api/auth: login, logout, me and
// the OIDC callback), health and config. Everything else under /api is absent.
func adminOnlyPublic(path string) bool {
	p := strings.TrimSuffix(path, "/")
	switch p {
	case "/api/health", "/api/config", "/api/admin", "/api/auth":
		return true
	}
	return strings.HasPrefix(p, "/api/admin/") || strings.HasPrefix(p, "/api/auth/")
}

// adminOnlyViewAs reports a request that may pass the gate although its path is
// outside the admin set: a GET/HEAD carrying X-Rivolt-Impersonate. That is the
// support tool ("view as"), which has to read user routes from this host. The
// header only gets it to the next layer: impersonationMW then demands an admin
// session and a non-admin target, and adminOnlyViewAsMW refuses any such
// request that did not end up impersonating.
func adminOnlyViewAs(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		strings.TrimSpace(r.Header.Get(ImpersonateHeader)) != ""
}

// adminOnlyGateMW makes every /api route outside adminOnlyPublic a 404 on the
// admin deployment, whoever asks and whether signed in or not, except a
// GET/HEAD that asks to be viewed as someone (adminOnlyViewAs). Non-/api paths
// (the SPA shell and assets, the probes) are untouched. It mirrors
// demoOnlyGateMW and runs after the Cloudflare Access check, before
// authentication.
func adminOnlyGateMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")) && !adminOnlyPublic(r.URL.Path) && !adminOnlyViewAs(r) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminOnlyViewAsMW runs right after impersonationMW. A route outside the
// admin set is served only when impersonation actually took effect; the
// header alone (a non-admin caller, impersonation switched off, a bad
// target) gets a 403 rather than the caller's own data.
func adminOnlyViewAsMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")) && !adminOnlyPublic(r.URL.Path) && !auth.IsImpersonating(r.Context()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// accessExempt lists what skips the Cloudflare Access check: the kubelet's
// probes, which reach the pod directly and carry no JWT. They answer with
// nothing but liveness.
func accessExempt(r *http.Request) bool {
	return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
}
