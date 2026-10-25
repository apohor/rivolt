package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
)

// asSession runs h with a signed-in user and, when kind is not "", a session of that kind
// in the request context (what the auth middleware leaves behind).
func asSession(h http.Handler, kind string) http.Handler {
	uid := uuid.New()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithUser(r.Context(), uid)
		if kind != "" {
			ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: kind})
		}
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

func denied(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), appWriteDenied)
}

// The whole router, full URLs: every mutating route it mounts is refused for an app
// session unless it is on the list, the listed ones are not refused by the list, and
// the same requests are untouched for a browser session or no session at all (the
// path-matching-middleware lesson of #569: test it through the real router).
func TestAppSessionWritesThroughTheRouter(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	// Stub map proxies, so the map mounts (a POST wildcard each) are walked too.
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	router := New(Deps{Auth: svc, ValhallaProxy: stub, TilesProxy: stub})
	send := func(kind, method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, strings.NewReader("{}"))
		req.RemoteAddr = "203.0.113.9:4000"
		asSession(router, kind).ServeHTTP(rec, req)
		return rec
	}
	pathOf := func(route string) string {
		p := strings.NewReplacer("{vehicleID}", "veh-x", "{id}", uuid.NewString(), "{provider}", "google", "/*", "/x").Replace(route)
		for strings.Contains(p, "{") {
			i := strings.Index(p, "{")
			j := strings.Index(p[i:], "}")
			p = p[:i] + "x" + p[i+j+1:]
		}
		return p
	}
	mutating, gets, listed, deniedGets := 0, 0, map[string]bool{}, map[string]bool{}
	err = chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/") {
			return nil
		}
		switch method {
		case http.MethodGet:
			// A read: refused for an app session only if appReadDenied names it, and
			// never for a browser session or no session.
			path := pathOf(route)
			gets++
			want := appReadDenied(path)
			if want {
				deniedGets[path] = true
			}
			if rec := send(sessions.KindApp, method, path); denied(rec) != want {
				t.Errorf("GET %s from an app session: refused=%v, want %v", path, denied(rec), want)
			}
			for _, kind := range []string{sessions.KindWeb, ""} {
				if rec := send(kind, method, path); denied(rec) {
					t.Errorf("GET %s refused for a %q session", path, kind)
				}
			}
			return nil
		case http.MethodHead, http.MethodOptions, http.MethodConnect, http.MethodTrace:
			return nil
		}
		mutating++
		path := pathOf(route)
		// A route mounted as "…/" is also served without the slash; the list holds that
		// spelling, and the slash spelling below must be refused.
		if t := strings.TrimSuffix(path, "/"); t != path && appWrites[method+" "+t] {
			listed[method+" "+t] = true
		}
		if appWriteAllowed(method, path) {
			if appWrites[method+" "+path] {
				listed[method+" "+path] = true
			}
			if rec := send(sessions.KindApp, method, path); denied(rec) {
				t.Errorf("%s %s is on the list but refused", method, path)
			}
			return nil
		}
		if rec := send(sessions.KindApp, method, path); !denied(rec) {
			t.Errorf("%s %s (%s) from an app session -> %d %q, want the refusal", method, path, route, rec.Code, rec.Body.String())
		}
		// A browser session and an anonymous caller are not this middleware's business.
		for _, kind := range []string{sessions.KindWeb, ""} {
			if rec := send(kind, method, path); denied(rec) {
				t.Errorf("%s %s refused for a %q session", method, path, kind)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if mutating < 40 {
		t.Fatalf("only %d mutating routes walked: the walk is not seeing the router", mutating)
	}
	if gets < 40 {
		t.Fatalf("only %d GET routes walked", gets)
	}
	// The three reads the app never makes are real routes of this router.
	for _, p := range []string{"/api/data/backup", "/api/export", "/api/state/veh-x/fresh"} {
		if !deniedGets[p] {
			t.Errorf("GET %s is not a route of the real router (the denylist has gone stale)", p)
		}
	}
	for _, want := range []string{"POST /api/auth/logout", "POST /api/push/apns/register", "POST /api/push/apns/activity/end", "PATCH /api/settings/charging"} {
		if !listed[want] {
			t.Errorf("%s is on the list but the real router does not mount it (the list has gone stale)", want)
		}
	}
	// Every route on the list that this router mounts passed the walk above; the ones that
	// need a database or a service are not mounted here, so also pin the list by hand.
	for k := range appWrites {
		method, path, _ := strings.Cut(k, " ")
		if rec := send(sessions.KindApp, method, path); denied(rec) {
			t.Errorf("%s is on the list but refused", k)
		}
	}
}

// The matcher itself: other spellings of a listed path, other methods, and the safe
// methods.
func TestAppWriteAllowlistMatching(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := asSession(appWriteAllowlistMW(ok), sessions.KindApp)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/auth/logout", 204},
		{"POST", "/api/auth/logout/", 403},
		{"POST", "/api/auth/logout?x=1", 204}, // the query is not part of the path
		{"POST", "/api/AUTH/logout", 403},
		{"POST", "//api/auth/logout", 403},
		{"POST", "/api/push/apns/activity/end", 204},
		{"POST", "/api/push/apns/activityx", 403},
		{"PUT", "/api/push/apns/register", 403},
		{"DELETE", "/api/auth/logout", 403},
		{"PATCH", "/api/milestones/dismiss", 403},
		{"POST", "/api/milestones/dismiss", 204},
		{"POST", "/api/settings/charging", 403},
		{"PUT", "/api/settings/home-location", 204}, // Essential Settings on iPhone
		{"POST", "/api/settings/home-location/restore", 204},
		{"POST", "/api/settings/home-location/search", 204},
		{"PUT", "/api/settings/home-location/", 403},
		{"PATCH", "/api/settings/home-location", 403},
		{"DELETE", "/api/settings/home-location", 403},
		{"POST", "/api/settings/home-location/search/", 403},
		{"PUT", "/api/settings/home-location/restore", 403},
		{"POST", "/api/settings/home-location/other", 403},
		{"PUT", "/api/vehicles/01-000000001/profile", 204},
		{"PUT", "/api/vehicles/veh_x.1/profile", 204},
		{"PUT", "/api/vehicles/01-000000001/profile/", 403},
		{"PUT", "/api/vehicles//profile", 403},
		{"PUT", "/api/vehicles/../profile", 403},
		{"PUT", "/api/vehicles/a/b/profile", 403},
		{"PUT", "/api/vehicles/a%2Fb/profile", 403},
		{"PUT", "/api/vehicles/" + strings.Repeat("a", 65) + "/profile", 403},
		{"POST", "/api/vehicles/01-000000001/profile", 403},
		{"PATCH", "/api/vehicles/01-000000001/profile", 403},
		{"DELETE", "/api/vehicles/01-000000001/profile", 403},
		{"PUT", "/api/vehicles/01-000000001", 403},
		{"PUT", "/api/vehicles/01-000000001/trailer", 403},
		{"GET", "/api/vehicles/01-000000001/profile", 204},
		{"POST", "/api/rivian/logout", 403},
		{"DELETE", "/api/account", 403},
		{"GET", "/api/settings/charging", 204},
		{"GET", "/api/ledger/monthly.csv", 204}, // the Money tab uses it
		{"GET", "/api/state/veh-x", 204},
		{"GET", "/api/state/veh-x/fresh", 403},
		{"GET", "/api/state/veh-x/fresh/", 403},
		{"HEAD", "/api/state/veh-x/fresh", 403},
		{"GET", "/api/state//fresh", 204}, // no id: not the route (the router 404s it)
		{"GET", "/api/export", 403},
		{"GET", "/api/export/", 403},
		{"GET", "//api/export", 403},
		{"GET", "/api/./export", 403},
		{"GET", "/api/data/backup", 403},
		{"GET", "/api/data/backup/", 403},
		{"GET", "/api/data", 204},
		{"GET", "/api/exports", 204}, // another path, not this one
		{"GET", "/api/state/veh-x/fresh/extra", 204},
		{"HEAD", "/api/charges", 204},
		{"OPTIONS", "/api/charges", 204},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, "https://rivolt.dev"+c.path, strings.NewReader("{}")))
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
	// No session, or a web session: nothing is refused.
	for _, kind := range []string{"", sessions.KindWeb} {
		rec := httptest.NewRecorder()
		asSession(appWriteAllowlistMW(ok), kind).ServeHTTP(rec, httptest.NewRequest("DELETE", "https://rivolt.dev/api/account", nil))
		if rec.Code != 204 {
			t.Errorf("a %q session was refused: %d", kind, rec.Code)
		}
	}
}

// Every write the iPhone app makes today (its APIClient) is on the list, so the app
// keeps working: sign-out, notification settings and the Live Activity, hiding a
// milestone and routing for the map.
func TestAppWriteAllowlistCoversTheAppsCalls(t *testing.T) {
	for _, call := range []string{
		"POST /api/auth/logout",
		"POST /api/push/apns/register", "POST /api/push/apns/preferences", "POST /api/push/apns/unregister", "POST /api/push/apns/test",
		"POST /api/push/apns/activity", "POST /api/push/apns/activity/end", "POST /api/push/apns/activity/test",
		"POST /api/milestones/dismiss",
		"POST /api/maps/valhalla/route", "POST /api/maps/valhalla/trace_route",
	} {
		if !appWrites[call] {
			t.Errorf("%s is something the app does and is not on the list", call)
		}
	}
}

// The phone may sign one device out by id, and nothing else under /api/auth/sessions: exactly /api/auth/sessions/{uuid} on
// DELETE passes the list (and reaches the sessions API, which only revokes the caller's own sessions and refuses view-as);
// revoke-others, the list path, a non-UUID, another spelling and another method do not.
func TestAppSessionMayRevokeOneDeviceOnly(t *testing.T) {
	id := uuid.NewString()
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"DELETE", "/api/auth/sessions/" + id, true},
		{"DELETE", "/api/auth/sessions/" + id + "/", false},
		{"DELETE", "/api/auth/sessions/" + strings.ToUpper(id), false},
		{"DELETE", "/api/auth/sessions/revoke-others", false},
		{"DELETE", "/api/auth/sessions/", false},
		{"DELETE", "/api/auth/sessions", false},
		{"DELETE", "/api/auth/sessions/not-a-uuid", false},
		{"DELETE", "/api/auth/sessions/" + id + "/x", false},
		{"DELETE", "/api/auth/sessions/../logout", false},
		{"DELETE", "/api/auth/sessionsx/" + id, false},
		{"POST", "/api/auth/sessions/revoke-others", false},
		{"POST", "/api/auth/sessions/" + id, false},
		{"PUT", "/api/auth/sessions/" + id, false},
		{"PATCH", "/api/auth/sessions/" + id, false},
		{"POST", "/api/auth/logout", true},
	} {
		if got := appWriteAllowed(c.method, c.path); got != c.want {
			t.Errorf("%s %s: allowed=%v, want %v", c.method, c.path, got, c.want)
		}
	}
}

// Through the real router with the sessions API mounted: an app session's DELETE of a device id reaches the handler and
// revokes only its own user's session (another user's id reads as 404, nothing revoked), revoke-others is refused by the
// list before the handler, and a viewing-as admin is refused by the handler.
func TestAppSessionRevokeThroughTheRouter(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	me, stranger := uuid.New(), uuid.New()
	mine, theirs := uuid.New(), uuid.New()
	store := &fakeSessions{owner: me, rows: []sessions.Session{{ID: mine, UserID: me, Kind: sessions.KindWeb}, {ID: uuid.New(), UserID: me, Kind: sessions.KindApp}}}
	router := New(Deps{Auth: svc, Sessions: &SessionsAPI{Store: store, Current: func(r *http.Request) (uuid.UUID, bool) {
		ref, ok := auth.SessionFromContext(r.Context())
		return ref.ID, ok
	}}})
	send := func(uid uuid.UUID, method, path string, impersonating bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, strings.NewReader("{}"))
		req.RemoteAddr = "203.0.113.9:4000"
		ctx := auth.WithSession(auth.WithUser(req.Context(), uid), auth.SessionRef{ID: uuid.New(), Kind: sessions.KindApp})
		if impersonating {
			ctx = auth.WithImpersonation(ctx, uuid.New(), uid)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	if rec := send(me, "DELETE", "/api/auth/sessions/"+mine.String(), false); rec.Code != http.StatusNoContent || len(store.revoked) != 1 || store.revoked[0] != mine {
		t.Fatalf("own device: %d, revoked %v", rec.Code, store.revoked)
	}
	if rec := send(stranger, "DELETE", "/api/auth/sessions/"+mine.String(), false); rec.Code != http.StatusNotFound || len(store.revoked) != 1 {
		t.Errorf("another user's session id: %d, revoked %v, want 404 and nothing more revoked", rec.Code, store.revoked)
	}
	if rec := send(me, "DELETE", "/api/auth/sessions/"+theirs.String(), false); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown session id: %d, want 404", rec.Code)
	}
	if rec := send(me, "POST", "/api/auth/sessions/revoke-others", false); !denied(rec) || store.keptOther != uuid.Nil {
		t.Errorf("revoke-others from an app token: %d %s (kept %v), want the app-write refusal and no call", rec.Code, rec.Body.String(), store.keptOther)
	}
	for _, p := range []string{"/api/auth/sessions", "/api/auth/sessions/not-a-uuid"} {
		if rec := send(me, "DELETE", p, false); !denied(rec) {
			t.Errorf("DELETE %s from an app token: %d, want the app-write refusal", p, rec.Code)
		}
	}
	if rec := send(me, "DELETE", "/api/auth/sessions/"+mine.String(), true); rec.Code != http.StatusForbidden || len(store.revoked) != 1 {
		t.Errorf("under view-as: %d, revoked %v, want 403 and nothing more revoked", rec.Code, store.revoked)
	}
}
