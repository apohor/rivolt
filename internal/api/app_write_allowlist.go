package api

import (
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"

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
	"POST /api/auth/logout": true, // sign out (revokes this token)
	// Also allowed, by pattern (appSessionRevokePath): DELETE /api/auth/sessions/{uuid}, to sign one device out from the phone.
	// Not allowed: POST /api/auth/sessions/revoke-others, which from a leaked phone token would sign the owner out everywhere
	// at once and leave the attacker's session as the last one standing.
	"POST /api/push/apns/register":      true, // notifications
	"POST /api/push/apns/unregister":    true,
	"POST /api/push/apns/preferences":   true,
	"POST /api/push/apns/test":          true,
	"POST /api/push/apns/activity":      true, // charging Live Activity
	"POST /api/push/apns/activity/end":  true,
	"POST /api/push/apns/activity/test": true,
	"POST /api/milestones/dismiss":      true, // hide a milestone card
	"PATCH /api/settings/charging":      true, // the home rate, merged field by field (settings_charging.go)
	"PATCH /api/settings/units":         true, // mi/km and °C/°F, one setting for web and app (settings_units.go)
	// Home location (Essential Settings on iPhone, DA-approved Oct 25): set, clear, restore and the address search. The worst a
	// leaked token can do is move "home", which relabels and re-prices new charges and is undone by restore; it cannot read more
	// than the app's GET already does. Audited by field names, limited per user, and the account is emailed when the app sets it.
	"PUT /api/settings/home-location":          true,
	"POST /api/settings/home-location/restore": true,
	"POST /api/settings/home-location/search":  true,
	"POST /api/maps/valhalla/route":            true, // routing for the map: computed, nothing stored
	// The trip planner on the iPhone (DA-approved Oct 25): four POSTs that compute and store nothing. Plans count against the same
	// per-user hourly planner quota as the web (planleg), the two archive queries have their own per-minute limiters, and
	// plan/advice (the LLM) stays off the list. The worst a leaked token can do is spend that hour's quota.
	"POST /api/trips/plan":                 true,
	"POST /api/trips/plan-multiday":        true,
	"POST /api/trips/overnight-candidates": true,
	"POST /api/maps/chargers-along":        true,
	"POST /api/maps/valhalla/trace_route":  true,
}

// sessionsPrefix is where a single sign-in is addressed: DELETE /api/auth/sessions/{id}.
const sessionsPrefix = "/api/auth/sessions/"

// appSessionRevokePath reports whether p is exactly /api/auth/sessions/{id} with {id} a canonical (lower-case, hyphenated)
// UUID. The phone may sign one device out (a lost browser) by id; "revoke-others", the list path, a non-UUID and any other
// spelling are not this. The sessions API only ever revokes the caller's own sessions (another user's id reads as 404), and
// it refuses a viewing-as admin.
func appSessionRevokePath(p string) bool {
	id, ok := strings.CutPrefix(p, sessionsPrefix)
	if !ok {
		return false
	}
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

// vehicleProfileSuffix ends PUT /api/vehicles/{id}/profile: the truck's tire, wheel and load profile, which only feeds the
// efficiency and planner calculations.
const (
	vehiclesPrefix       = "/api/vehicles/"
	vehicleProfileSuffix = "/profile"
)

// appVehicleProfilePath reports whether p is exactly /api/vehicles/{id}/profile with {id} the gateway's vehicle id shape (letters,
// digits, dot, dash, underscore; 1 to 64 characters). Whose vehicle it is stays the ownership middleware's decision: another
// user's id is a 404.
func appVehicleProfilePath(p string) bool {
	rest, ok := strings.CutPrefix(p, vehiclesPrefix)
	if !ok {
		return false
	}
	id, ok := strings.CutSuffix(rest, vehicleProfileSuffix)
	if !ok || id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return id != "." && id != ".."
}

// appWriteAllowed is the whole list: the exact routes in appWrites, signing one device out by its id, and a vehicle's profile.
func appWriteAllowed(method, p string) bool {
	return appWrites[method+" "+p] ||
		(method == http.MethodDelete && appSessionRevokePath(p)) ||
		(method == http.MethodPut && appVehicleProfilePath(p))
}

// appWriteDenied is the text of the refusal; tests look for it.
const appWriteDenied = "this action is not available from the iPhone app"

// errorBody is the plain error answer: {"error": "..."}. The refusals that the clients read are pinned by fixtures
// (contract_test.go).
type errorBody struct {
	Error string `json:"error"`
}

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
				writeJSON(w, http.StatusForbidden, errorBody{Error: appWriteDenied})
				return
			}
			next.ServeHTTP(w, r)
			return
		case http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !app || appWriteAllowed(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		slog.WarnContext(r.Context(), "app session write refused", "method", r.Method, "path", r.URL.Path)
		writeJSON(w, http.StatusForbidden, errorBody{Error: appWriteDenied})
	})
}
