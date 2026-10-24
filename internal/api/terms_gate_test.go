package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
)

// The Terms gate through the real router, with full paths and a real session cookie: a signed-in user who must accept the
// current Terms gets 403 terms_required on the data routes, and can still agree, export, back up, delete the account and
// sign out. (maxBodyBytes-style path rules have broken routes before: #569.)
type gateRig struct {
	router  http.Handler
	cookie  *http.Cookie
	terms   *atomic.Pointer[db.Terms]
	lookups atomic.Int32
	fail    atomic.Bool
}

func newGateRig(t *testing.T, t0 db.Terms) *gateRig {
	t.Helper()
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor, SecureCookie: false})
	if err != nil {
		t.Fatal(err)
	}
	g := &gateRig{terms: &atomic.Pointer[db.Terms]{}}
	g.terms.Store(&t0)
	g.router = New(Deps{Auth: svc, AuthEnforced: true, TermsLookup: func(context.Context, uuid.UUID) (db.Terms, error) {
		g.lookups.Add(1)
		if g.fail.Load() {
			return db.Terms{}, errors.New("db down")
		}
		return *g.terms.Load(), nil
	}})
	rec := httptest.NewRecorder()
	if err := svc.IssueSession(rec, httptest.NewRequest(http.MethodGet, "/", nil), uuid.New()); err != nil {
		t.Fatal(err)
	}
	cs := rec.Result().Cookies()
	if len(cs) == 0 {
		t.Fatal("no session cookie issued")
	}
	g.cookie = cs[0]
	return g
}

func (g *gateRig) call(method, path string) (int, string) {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(g.cookie)
	w := httptest.NewRecorder()
	g.router.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func (g *gateRig) gated(method, path string) bool {
	code, body := g.call(method, path)
	return code == http.StatusForbidden && strings.Contains(body, termsRequiredError)
}

var gatedRoutes = [][2]string{
	{"GET", "/api/vehicles"},
	{"GET", "/api/charges"},
	{"GET", "/api/drives"},
	{"GET", "/api/settings/charging"},
	{"PUT", "/api/settings/charging"},
	{"GET", "/api/ledger/summary"},
	{"POST", "/api/import/electrafi"},
	{"POST", "/api/data/restore"},
	{"DELETE", "/api/data/sessions"},
	{"POST", "/api/trips/plan"},
}

var openRoutes = [][2]string{
	{"POST", "/api/terms/accept"},
	{"GET", "/api/export"},
	{"GET", "/api/data/backup"},
	{"DELETE", "/api/data/account"},
	{"POST", "/api/auth/logout"},
	{"GET", "/api/health"},
	// switching notifications off
	{"POST", "/api/push/off"},
	{"POST", "/api/push/unsubscribe"},
	{"POST", "/api/push/apns/unregister"},
}

// Turning notifications on stays gated.
var gatedPushRoutes = [][2]string{
	{"POST", "/api/push/subscribe"},
	{"POST", "/api/push/optin"},
	{"POST", "/api/push/device"},
	{"POST", "/api/push/apns/register"},
	{"POST", "/api/push/apns/preferences"},
}

func TestTermsGateRefusesDataRoutesUntilAccepted(t *testing.T) {
	newUser := db.Terms{CreatedAt: termsEffectiveAt.Add(time.Hour)}
	g := newGateRig(t, newUser)
	for _, rt := range gatedRoutes {
		if !g.gated(rt[0], rt[1]) {
			code, body := g.call(rt[0], rt[1])
			t.Errorf("%s %s = %d %q, want 403 terms_required", rt[0], rt[1], code, strings.TrimSpace(body))
		}
	}
	for _, rt := range gatedPushRoutes {
		if !g.gated(rt[0], rt[1]) {
			code, body := g.call(rt[0], rt[1])
			t.Errorf("%s %s = %d %q, want 403 terms_required", rt[0], rt[1], code, strings.TrimSpace(body))
		}
	}
	for _, rt := range openRoutes {
		if g.gated(rt[0], rt[1]) {
			t.Errorf("%s %s is gated; a user who has not accepted must still reach it", rt[0], rt[1])
		}
	}
}

func TestTermsGateOpensOnAcceptanceAndNeverAppliesToOlderAccounts(t *testing.T) {
	v := currentTermsVersion
	for name, terms := range map[string]db.Terms{
		"accepted":                        {Version: &v, CreatedAt: termsEffectiveAt.Add(time.Hour)},
		"an account older than the Terms": {CreatedAt: termsEffectiveAt.Add(-48 * time.Hour)},
	} {
		g := newGateRig(t, terms)
		for _, rt := range gatedRoutes {
			if g.gated(rt[0], rt[1]) {
				t.Errorf("%s: %s %s is gated", name, rt[0], rt[1])
			}
		}
	}
	// Accepting takes effect on the next request.
	g := newGateRig(t, db.Terms{CreatedAt: termsEffectiveAt.Add(time.Hour)})
	if !g.gated("GET", "/api/vehicles") {
		t.Fatal("not gated before accepting")
	}
	g.terms.Store(&db.Terms{Version: &v, CreatedAt: termsEffectiveAt.Add(time.Hour)})
	if g.gated("GET", "/api/vehicles") {
		t.Error("still gated after accepting")
	}
}

// A user found clear costs no further lookups for this version; a database error fails closed with 503, not open.
func TestTermsGateCachesClearUsersAndFailsClosed(t *testing.T) {
	v := currentTermsVersion
	g := newGateRig(t, db.Terms{Version: &v, CreatedAt: termsEffectiveAt.Add(time.Hour)})
	for i := 0; i < 5; i++ {
		g.call("GET", "/api/vehicles")
	}
	if n := g.lookups.Load(); n != 1 {
		t.Errorf("%d lookups for 5 requests of a user who has accepted, want 1", n)
	}
	g2 := newGateRig(t, db.Terms{CreatedAt: termsEffectiveAt.Add(time.Hour)})
	g2.fail.Store(true)
	if code, _ := g2.call("GET", "/api/vehicles"); code != http.StatusServiceUnavailable {
		t.Errorf("lookup error gave %d, want 503", code)
	}
	if g2.gated("GET", "/api/vehicles") {
		t.Error("a lookup error must not read as a gate answer")
	}
}

// The public demo runs as the demo user, who accepts nothing: the gate must pass it explicitly, whatever the account's
// creation date, or the next Terms version that moves termsEffectiveAt would 403 the public demo. Through the real router
// with the demo on, an anonymous visitor on a read route is served as the demo user and is not gated.
func TestTermsGateDoesNotGateTheAnonymousDemo(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	var asked []uuid.UUID
	router := New(Deps{Auth: svc, AuthEnforced: true, TermsLookup: func(_ context.Context, uid uuid.UUID) (db.Terms, error) {
		asked = append(asked, uid)
		return db.Terms{CreatedAt: termsEffectiveAt.Add(time.Hour)}, nil // "unaccepted", created after the Terms took effect
	}})
	for _, path := range []string{"/api/vehicles", "/api/charges", "/api/drives", "/api/settings/charging", "/api/ledger/summary"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Header().Get(demoHeader) == "" {
			t.Fatalf("GET %s was not served as the demo (%d); the test is not exercising the demo user", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), termsRequiredError) {
			t.Errorf("GET %s: the demo user was gated: %d %q", path, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
	for _, uid := range asked {
		if uid == demo.UserID() {
			t.Error("the gate looked the demo user up; it should pass it without a query")
		}
	}
}
