package api

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

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
	v, err := cfaccess.New(cfaccess.Config{TeamDomain: rigTeam, Aud: rigAud, Emails: []string{rigEmail}, CertsURL: jwks.URL}, nil)
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
