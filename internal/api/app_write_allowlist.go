package api

import (
	"log/slog"
	"net/http"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/sessions"
)

// An iPhone app session (a bearer token, sessions.KindApp) is meant to read, and to
// do a handful of things of its own. Until this, a token passed every write route the
// browser cookie does (change settings, delete charges, restore a backup, delete the
// account...). So the app may now send a mutating request only to the routes below,
// and anything else is a 403 before the router sees it. Today's app actions, nothing
// else; a new app write (the charging rates, for one) is added here in its own PR.
//
// The match is on the method and the exact path as received (the router sees the same
// path; a trailing slash or other spelling is not on the list). GET, HEAD and OPTIONS
// are not writes and pass.
var appWrites = map[string]bool{
	"POST /api/auth/logout":               true, // sign out (revokes this token)
	"POST /api/push/apns/register":        true, // notifications
	"POST /api/push/apns/unregister":      true,
	"POST /api/push/apns/preferences":     true,
	"POST /api/push/apns/test":            true,
	"POST /api/push/apns/activity":        true, // charging Live Activity
	"POST /api/push/apns/activity/end":    true,
	"POST /api/push/apns/activity/test":   true,
	"POST /api/milestones/dismiss":        true, // hide a milestone card
	"POST /api/maps/valhalla/route":       true, // routing for the map: computed, nothing stored
	"POST /api/maps/valhalla/trace_route": true,
}

// appWriteDenied is the body of the refusal; tests look for it.
const appWriteDenied = "this action is not available from the iPhone app"

// appWriteAllowlistMW refuses a mutating request from an app session unless it is on
// appWrites. It runs after authentication has resolved the session.
func appWriteAllowlistMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		ref, ok := auth.SessionFromContext(r.Context())
		if !ok || ref.Kind != sessions.KindApp || appWrites[r.Method+" "+r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		slog.WarnContext(r.Context(), "app session write refused", "method", r.Method, "path", r.URL.Path)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": appWriteDenied})
	})
}
