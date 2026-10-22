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
	mutating, listed := 0, map[string]bool{}
	err = chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/") {
			return nil
		}
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodConnect, http.MethodTrace:
			return nil
		}
		mutating++
		path := pathOf(route)
		// A route mounted as "…/" is also served without the slash; the list holds that
		// spelling, and the slash spelling below must be refused.
		if t := strings.TrimSuffix(path, "/"); t != path && appWrites[method+" "+t] {
			listed[method+" "+t] = true
		}
		if appWrites[method+" "+path] {
			listed[method+" "+path] = true
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
		{"POST", "/api/rivian/logout", 403},
		{"DELETE", "/api/account", 403},
		{"GET", "/api/settings/charging", 204},
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
