package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/cfaccess"
	"github.com/apohor/rivolt/internal/db"
)

func TestAdminOnlyPublicSet(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/admin/users":               true,
		"/api/admin":                     true,
		"/api/admin/impersonation/stop":  true,
		"/api/auth/me":                   true,
		"/api/auth/logout":               true,
		"/api/auth/oidc/google/callback": true,
		"/api/health":                    true,
		"/api/config":                    true,
		"/api/config/":                   true,
		"/api/vehicles":                  false,
		"/api/charges":                   false,
		"/api/signup":                    false,
		"/api/events":                    false,
		"/api/adminx":                    false,
		"/api/authx/me":                  false,
		"/api/admin-evil":                false,
		"/api":                           false,
		"/api/demo":                      false,
	} {
		if got := adminOnlyPublic(path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
}

type accessClaims struct {
	jwt.Claims
	Email string `json:"email"`
}

type accessRig struct {
	key    *rsa.PrivateKey
	router http.Handler
}

const (
	rigTeam  = "jolly-river-3a16.cloudflareaccess.com"
	rigAud   = "admin-app-aud"
	rigEmail = "owner@example.com"
)

func newAccessRig(t *testing.T) *accessRig {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	}))
	t.Cleanup(jwks.Close)
	v, err := cfaccess.New(cfaccess.Config{TeamDomain: rigTeam, Aud: rigAud, Emails: []string{rigEmail}, ServiceTokens: []string{"qa.access"}, CertsURL: jwks.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	return &accessRig{key: k, router: New(Deps{Auth: svc, AuthEnforced: true, AdminOnly: true, Access: v})}
}

func (a *accessRig) jwt(t *testing.T, mut func(*jwt.Claims, *string)) string {
	t.Helper()
	c := jwt.Claims{
		Issuer: "https://" + rigTeam, Audience: jwt.Audience{rigAud},
		Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	email := rigEmail
	if mut != nil {
		mut(&c, &email)
	}
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: a.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := jwt.Signed(sg).Claims(accessClaims{c, email}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (a *accessRig) get(path, token string) int {
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set(cfaccess.Header, token)
	}
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return rec.Code
}

func TestAdminOnlyRouter_RefusesWithoutAValidAccessJWT(t *testing.T) {
	a := newAccessRig(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged := func() string {
		sg, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: other}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
		s, _ := jwt.Signed(sg).Claims(accessClaims{jwt.Claims{Issuer: "https://" + rigTeam, Audience: jwt.Audience{rigAud}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}, rigEmail}).Serialize()
		return s
	}()
	bad := map[string]string{
		"none":        "",
		"forged":      forged,
		"expired":     a.jwt(t, func(c *jwt.Claims, _ *string) { c.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Hour)) }),
		"wrong aud":   a.jwt(t, func(c *jwt.Claims, _ *string) { c.Audience = jwt.Audience{"another-app"} }),
		"wrong email": a.jwt(t, func(_ *jwt.Claims, e *string) { *e = "intruder@example.com" }),
	}
	// Every kind of path: admin, non-admin, SPA, config. All 403 before any route.
	for name, tok := range bad {
		for _, p := range []string{"/api/admin/users", "/api/auth/me", "/api/config", "/api/vehicles", "/", "/admin"} {
			if got := a.get(p, tok); got != 403 {
				t.Errorf("%s JWT on %s = %d, want 403", name, p, got)
			}
		}
	}
	// The plain email header is never trusted.
	req := httptest.NewRequest("GET", "/api/admin/users", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", rigEmail)
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("plain email header = %d, want 403", rec.Code)
	}
}

func TestAdminOnlyRouter_404sNonAdminRoutes(t *testing.T) {
	a := newAccessRig(t)
	tok := a.jwt(t, nil)
	for _, p := range []string{"/api/vehicles", "/api/charges", "/api/drives", "/api/signup", "/api/events", "/api/demo", "/api/trips/plan", "/api/state/x"} {
		if got := a.get(p, tok); got != 404 {
			t.Errorf("%s with a valid JWT = %d, want 404 on the admin deployment", p, got)
		}
	}
	// The admin surface exists: it gets past the gate and stops at Rivolt's own
	// session check (401), not a 404.
	if got := a.get("/api/admin/users", tok); got != 401 {
		t.Errorf("/api/admin/users with a valid JWT and no session = %d, want 401", got)
	}
	if got := a.get("/api/health", tok); got != 200 {
		t.Errorf("/api/health = %d, want 200", got)
	}
	// A view-as GET passes the gate but, with no admin session behind it, is
	// refused by the layer after impersonation (403, never data).
	vreq := httptest.NewRequest("GET", "/api/vehicles", nil)
	vreq.Header.Set(cfaccess.Header, tok)
	vreq.Header.Set(ImpersonateHeader, uuid.NewString())
	vrec := httptest.NewRecorder()
	a.router.ServeHTTP(vrec, vreq)
	if vrec.Code != 403 {
		t.Errorf("view-as GET without a session = %d, want 403", vrec.Code)
	}
	// Probes carry no JWT and are exempt.
	if got := a.get("/healthz", ""); got != 200 {
		t.Errorf("/healthz = %d, want 200", got)
	}
}

func TestAdminOnlyRouter_NilVerifierDeniesEverything(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	r := New(Deps{Auth: svc, AuthEnforced: true, AdminOnly: true})
	for _, p := range []string{"/api/admin/users", "/api/health", "/"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 403 {
			t.Errorf("%s with no verifier = %d, want 403", p, rec.Code)
		}
	}
}

// The view-as chain exactly as api.New wires it on the admin deployment:
// gate -> auth -> impersonationMW -> adminOnlyViewAsMW -> route.
func TestAdminOnlyViewAs(t *testing.T) {
	admin, other, target, anotherAdmin := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	roles := map[uuid.UUID]string{admin: "admin", other: "user", target: "user", anotherAdmin: "admin"}
	build := func(disabled bool) http.Handler {
		r := chi.NewRouter()
		r.Use(adminOnlyGateMW)
		r.Use(func(next http.Handler) http.Handler { // stands in for session auth: X-Test-User
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if u, err := uuid.Parse(req.Header.Get("X-Test-User")); err == nil {
					req = req.WithContext(auth.WithUser(req.Context(), u))
				}
				next.ServeHTTP(w, req)
			})
		})
		r.Use(impersonationMW(func(_ context.Context, uid uuid.UUID) (string, error) { return roles[uid], nil },
			func() bool { return disabled }))
		r.Use(adminOnlyViewAsMW)
		r.HandleFunc("/*", func(w http.ResponseWriter, req *http.Request) {
			if id, ok := auth.UserFromContext(req.Context()); ok {
				_, _ = w.Write([]byte(id.String()))
			}
		})
		return r
	}
	do := func(h http.Handler, method, path, user, view string) (int, string) {
		req := httptest.NewRequest(method, path, nil)
		if user != "" {
			req.Header.Set("X-Test-User", user)
		}
		if view != "" {
			req.Header.Set(ImpersonateHeader, view)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	h := build(false)

	if code, body := do(h, "GET", "/api/vehicles", admin.String(), target.String()); code != 200 || body != target.String() {
		t.Errorf("impersonated GET = %d %q, want 200 served as the target", code, body)
	}
	if code, _ := do(h, "HEAD", "/api/vehicles", admin.String(), target.String()); code != 200 {
		t.Errorf("impersonated HEAD = %d, want 200", code)
	}
	if code, _ := do(h, "GET", "/api/vehicles", admin.String(), ""); code != 404 {
		t.Errorf("non-impersonated user-data GET = %d, want 404", code)
	}
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		if code, _ := do(h, m, "/api/vehicles", admin.String(), target.String()); code != 404 {
			t.Errorf("impersonated %s = %d, want refused (404)", m, code)
		}
	}
	// A non-admin sending the header must not be served (as anyone).
	if code, body := do(h, "GET", "/api/vehicles", other.String(), target.String()); code != 403 || body == other.String() {
		t.Errorf("non-admin with the header = %d %q, want 403 and no data", code, body)
	}
	// No session at all, with the header.
	if code, _ := do(h, "GET", "/api/vehicles", "", target.String()); code != 403 {
		t.Errorf("anonymous with the header = %d, want 403", code)
	}
	// Still never an admin, never /api/admin while impersonating.
	if code, _ := do(h, "GET", "/api/vehicles", admin.String(), anotherAdmin.String()); code != 403 {
		t.Errorf("impersonating an admin = %d, want 403", code)
	}
	if code, _ := do(h, "GET", "/api/admin/users", admin.String(), target.String()); code != 403 {
		t.Errorf("/api/admin while impersonating = %d, want 403", code)
	}
	// Impersonation switched off: the header alone opens nothing.
	if code, body := do(build(true), "GET", "/api/vehicles", admin.String(), target.String()); code != 403 || body == target.String() {
		t.Errorf("header with impersonation disabled = %d %q, want 403 and no data", code, body)
	}
	// The admin set is unaffected.
	if code, _ := do(h, "GET", "/api/auth/me", admin.String(), ""); code != 200 {
		t.Errorf("/api/auth/me = %d, want 200", code)
	}
}

// A listed Access service token passes the outer door but is still just the
// door: the admin routes then need a Rivolt admin session (401 without one).
func TestAdminOnlyRouter_ServiceTokenOnlyPassesTheDoor(t *testing.T) {
	a := newAccessRig(t)
	svc := func(cn string) string {
		sg, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: a.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
		s, _ := jwt.Signed(sg).Claims(struct {
			jwt.Claims
			CommonName string `json:"common_name"`
		}{jwt.Claims{Issuer: "https://" + rigTeam, Audience: jwt.Audience{rigAud}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}, cn}).Serialize()
		return s
	}
	if got := a.get("/api/admin/users", svc("qa.access")); got != 401 {
		t.Errorf("listed service token, no session = %d, want 401 (past the door, stopped by the session check)", got)
	}
	if got := a.get("/api/admin/users", svc("other.access")); got != 403 {
		t.Errorf("unlisted service token = %d, want 403", got)
	}
}
