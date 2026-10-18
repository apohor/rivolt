package api

import (
	"net/http"
	"strings"
)

// adminDisabledMW answers 404 for the admin surface when the deployment
// serves none (RIVOLT_ADMIN_DISABLED): the SPA's /admin page and the
// /api/admin API, for every caller, an admin session included. A 404 (not
// 403) says nothing about what would be there.
func adminDisabledMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAdminPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isAdminPath matches /admin, /api/admin and anything under them, but not
// a path that merely starts with the letters (/administrator, /api/admins).
func isAdminPath(p string) bool {
	for _, prefix := range []string{"/admin", "/api/admin"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}
