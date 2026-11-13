package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/trips"
)

func shareRouter(t *testing.T, mutate func(*Deps)) http.Handler {
	t.Helper()
	lazy, err := sql.Open("pgx", "postgres://nobody@127.0.0.1:1/none") // nothing connects: these tests never reach the database
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lazy.Close() })
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: lazy, Trips: trips.NewFactory(lazy), Auth: svc, AuthEnforced: true, TripShare: true}
	if mutate != nil {
		mutate(&d)
	}
	return New(d)
}

func sharePost(h http.Handler, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, publicSharePath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The flag is off by default: no share route exists.
func TestTripShareRoutesAbsentWithFlagOff(t *testing.T) {
	h := shareRouter(t, func(d *Deps) { d.TripShare = false })
	if rec := sharePost(h, `{"token":"AAAAAAAAAAAAAAAAAAAAAA"}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("public: %d", rec.Code)
	}
	for _, c := range [][2]string{{"POST", "/api/trips/saved/00000000-0000-0000-0000-000000000001/share"}, {"DELETE", "/api/trips/shares/00000000-0000-0000-0000-000000000001"}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c[0], c[1], nil))
		if rec.Code == http.StatusCreated || rec.Code == http.StatusNoContent {
			t.Fatalf("%v answered %d", c, rec.Code)
		}
	}
}

// S7: the public lookup is outside the session chain. A cookie or a bearer token sent with it is neither read (a bad bearer would be a 401
// from the session middleware) nor answered with a Set-Cookie (a bad cookie would be cleared), and every miss is the same 404.
func TestTripSharePublicRouteIgnoresSessionAndSetsNoCookie(t *testing.T) {
	h := shareRouter(t, nil)
	var first string
	for i, mutate := range []func(*http.Request){
		nil,
		func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "rivolt_session", Value: "junk"}) },
		func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "junk"}) },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer junk") },
	} {
		for _, body := range []string{`{"token":"short"}`, `not json`, `{}`, `{"token":"!!!!!!!!!!!!!!!!!!!!!!"}`} {
			rec := sharePost(h, body, mutate)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("variant %d %q: %d", i, body, rec.Code)
			}
			if rec.Header().Get("Set-Cookie") != "" {
				t.Fatalf("variant %d: Set-Cookie %q", i, rec.Header().Get("Set-Cookie"))
			}
			if first == "" {
				first = rec.Body.String()
			} else if rec.Body.String() != first {
				t.Fatalf("404 bodies differ: %q vs %q", rec.Body.String(), first)
			}
			if rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Header().Get("X-Robots-Tag"), "noindex") {
				t.Fatalf("headers: %v", rec.Header())
			}
		}
	}
}

func TestTripShareOwnerRoutesNeedASession(t *testing.T) {
	h := shareRouter(t, nil)
	for _, c := range [][2]string{
		{"POST", "/api/trips/saved/00000000-0000-0000-0000-000000000001/share"},
		{"GET", "/api/trips/saved/00000000-0000-0000-0000-000000000001/shares"},
		{"DELETE", "/api/trips/shares/00000000-0000-0000-0000-000000000001"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c[0], c[1], strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%v: %d, want 401", c, rec.Code)
		}
	}
}

func TestTripSharePublicRouteIsPerIPLimited(t *testing.T) {
	h := shareRouter(t, nil)
	limited := false
	for i := 0; i < 80; i++ {
		if sharePost(h, `{"token":"short"}`, nil).Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("80 lookups from one address were never limited")
	}
}

func TestTripShareLookupPoolFullAnswers503(t *testing.T) {
	a := &tripShareAPI{slots: make(chan struct{}, 1), count: func(string) {}}
	a.slots <- struct{}{}
	rec := httptest.NewRecorder()
	a.view(rec, httptest.NewRequest(http.MethodPost, publicSharePath, strings.NewReader(`{"token":"AAAAAAAAAAAAAAAAAAAAAA"}`)))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

func TestTripSharePublicRouteNotOnAdminSite(t *testing.T) {
	h := shareRouter(t, func(d *Deps) { d.AdminOnly = true })
	if rec := sharePost(h, `{"token":"AAAAAAAAAAAAAAAAAAAAAA"}`, nil); rec.Code == http.StatusOK {
		t.Fatalf("answered on the admin site: %d", rec.Code)
	}
}
