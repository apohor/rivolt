package api

import (
	"net/http"
	"strings"
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

// adminOnlyGateMW makes every /api route outside adminOnlyPublic a 404 on the
// admin deployment, whoever asks and whether signed in or not. Non-/api paths
// (the SPA shell and assets, the probes) are untouched. It mirrors
// demoOnlyGateMW and runs after the Cloudflare Access check, before
// authentication.
func adminOnlyGateMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")) && !adminOnlyPublic(r.URL.Path) {
			http.NotFound(w, r)
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
