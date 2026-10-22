package api

import (
	"log/slog"
	"net/http"
	"path"
	"strings"

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
	"PATCH /api/settings/charging":        true, // the home rate, merged field by field (settings_charging.go)
	"POST /api/maps/valhalla/route":       true, // routing for the map: computed, nothing stored
	"POST /api/maps/valhalla/trace_route": true,
}

// appWriteDenied is the body of the refusal; tests look for it.
const appWriteDenied = "this action is not available from the iPhone app"

// appReadDenied reports whether a GET is one an app session may not make: the three
// reads that are not plain reads. The app never calls them, and a leaked phone token
// should not be able to wake the truck (state/{id}/fresh forces a read from Rivian) or pull
// the whole account in one request (data/backup, export). The path is cleaned first, so
// a trailing slash or a doubled one is the same route; the router is case-sensitive, so
// another case is a 404, not a bypass.
func appReadDenied(p string) bool {
	p = path.Clean("/" + p)
	switch p {
	case "/api/data/backup", "/api/export":
		return true
	}
	seg := strings.Split(strings.TrimPrefix(p, "/"), "/")
	return len(seg) == 4 && seg[0] == "api" && seg[1] == "state" && seg[3] == "fresh"
}

// appWriteAllowlistMW keeps an app session to the list: a mutating request is refused
// unless it is on appWrites, and a GET or HEAD is refused if it is one of the reads
// appReadDenied names. It runs after authentication has resolved the session.
func appWriteAllowlistMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref, signedIn := auth.SessionFromContext(r.Context())
		app := signedIn && ref.Kind == sessions.KindApp
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			if app && appReadDenied(r.URL.Path) {
				slog.WarnContext(r.Context(), "app session read refused", "method", r.Method, "path", r.URL.Path)
				writeJSON(w, http.StatusForbidden, map[string]any{"error": appWriteDenied})
				return
			}
			next.ServeHTTP(w, r)
			return
		case http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !app || appWrites[r.Method+" "+r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		slog.WarnContext(r.Context(), "app session write refused", "method", r.Method, "path", r.URL.Path)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": appWriteDenied})
	})
}
