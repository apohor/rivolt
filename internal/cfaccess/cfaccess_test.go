package cfaccess

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	team = "jolly-river-3a16.cloudflareaccess.com"
	aud  = "app-aud-tag"
	who  = "owner@example.com"
)

type fixture struct {
	t     *testing.T
	key   *rsa.PrivateKey
	kid   string
	hits  atomic.Int32
	srv   *httptest.Server
	v     *Verifier
	clock time.Time
	keys  []jose.JSONWebKey // what the fake certs endpoint serves
	over  atomic.Pointer[http.HandlerFunc]
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, key: k, kid: "kid-1", clock: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	f.keys = []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: f.kid, Algorithm: "RS256", Use: "sig"}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if h := f.over.Load(); h != nil {
			(*h)(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: f.keys})
	}))
	t.Cleanup(f.srv.Close)
	v, err := New(Config{TeamDomain: team, Aud: aud, Emails: []string{"Owner@Example.com"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v.certs = f.srv.URL
	v.now = func() time.Time { return f.clock }
	f.v = v
	return f
}

type claims struct {
	jwt.Claims
	Email string `json:"email"`
}

func (f *fixture) good() claims {
	return claims{
		Claims: jwt.Claims{
			Issuer:   "https://" + team,
			Audience: jwt.Audience{aud},
			Expiry:   jwt.NewNumericDate(f.clock.Add(time.Hour)),
			IssuedAt: jwt.NewNumericDate(f.clock),
		},
		Email: who,
	}
}

func (f *fixture) sign(key *rsa.PrivateKey, kid string, c claims) string {
	f.t.Helper()
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		f.t.Fatal(err)
	}
	s, err := jwt.Signed(sg).Claims(c).Serialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func TestVerifyAccepts(t *testing.T) {
	f := newFixture(t)
	got, err := f.v.Verify(t.Context(), f.sign(f.key, f.kid, f.good()))
	if err != nil || got != who {
		t.Fatalf("Verify = %q, %v; want %q", got, err, who)
	}
}

func TestVerifyRejects(t *testing.T) {
	f := newFixture(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	mut := func(fn func(*claims)) string { c := f.good(); fn(&c); return f.sign(f.key, f.kid, c) }
	cases := map[string]string{
		"empty":          "",
		"garbage":        "not.a.jwt",
		"forged signer":  f.sign(other, f.kid, f.good()), // right kid, wrong private key
		"unknown kid":    f.sign(f.key, "nope", f.good()),
		"expired":        mut(func(c *claims) { c.Expiry = jwt.NewNumericDate(f.clock.Add(-time.Hour)) }),
		"no expiry":      mut(func(c *claims) { c.Expiry = nil }),
		"wrong aud":      mut(func(c *claims) { c.Audience = jwt.Audience{"someone-else"} }),
		"wrong issuer":   mut(func(c *claims) { c.Issuer = "https://evil.cloudflareaccess.com" }),
		"wrong email":    mut(func(c *claims) { c.Email = "intruder@example.com" }),
		"no email":       mut(func(c *claims) { c.Email = "" }),
		"not yet valid":  mut(func(c *claims) { c.NotBefore = jwt.NewNumericDate(f.clock.Add(time.Hour)) }),
		"email, not aud": mut(func(c *claims) { c.Audience = nil }),
	}
	for name, tok := range cases {
		if got, err := f.v.Verify(t.Context(), tok); err == nil {
			t.Errorf("%s: accepted as %q", name, got)
		}
	}
	// alg=none and HS256 must not get through even with the right claims.
	for name, tok := range map[string]string{
		"alg none": "eyJhbGciOiJub25lIiwia2lkIjoia2lkLTEifQ.eyJlbWFpbCI6Im93bmVyQGV4YW1wbGUuY29tIn0.",
	} {
		if _, err := f.v.Verify(t.Context(), tok); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestExpiryLeewayAndBoundary(t *testing.T) {
	f := newFixture(t)
	c := f.good()
	c.Expiry = jwt.NewNumericDate(f.clock.Add(-10 * time.Second)) // inside the 30 s leeway
	if _, err := f.v.Verify(t.Context(), f.sign(f.key, f.kid, c)); err != nil {
		t.Errorf("10 s past expiry should pass inside the leeway: %v", err)
	}
	c.Expiry = jwt.NewNumericDate(f.clock.Add(-2 * time.Minute))
	if _, err := f.v.Verify(t.Context(), f.sign(f.key, f.kid, c)); err == nil {
		t.Error("2 min past expiry must fail")
	}
}

func TestKeyCacheAndRotation(t *testing.T) {
	f := newFixture(t)
	tok := f.sign(f.key, f.kid, f.good())
	for i := 0; i < 3; i++ {
		if _, err := f.v.Verify(t.Context(), tok); err != nil {
			t.Fatal(err)
		}
	}
	if f.hits.Load() != 1 {
		t.Errorf("JWKS fetched %d times for 3 verifications, want 1 (cached)", f.hits.Load())
	}

	// Rotation: a token signed by a new key shows an unknown kid. The cache
	// refreshes, but not more than once a minute.
	nk, _ := rsa.GenerateKey(rand.Reader, 2048)
	f.keys = append(f.keys, jose.JSONWebKey{Key: &nk.PublicKey, KeyID: "kid-2", Algorithm: "RS256", Use: "sig"})
	rot := f.sign(nk, "kid-2", f.good())
	if _, err := f.v.Verify(t.Context(), rot); err == nil {
		t.Error("an unknown kid inside the refresh gap should not trigger a fetch")
	}
	f.clock = f.clock.Add(2 * time.Minute)
	if _, err := f.v.Verify(t.Context(), rot); err != nil {
		t.Errorf("after the gap the new key should be fetched: %v", err)
	}

	// Forged kids cannot hammer the endpoint.
	before := f.hits.Load()
	for i := 0; i < 20; i++ {
		_, _ = f.v.Verify(t.Context(), f.sign(f.key, "junk", f.good()))
	}
	if f.hits.Load()-before > 1 {
		t.Errorf("20 forged kids caused %d fetches, want at most 1", f.hits.Load()-before)
	}
}

func TestJWKSOutageKeepsKnownKey(t *testing.T) {
	f := newFixture(t)
	tok := f.sign(f.key, f.kid, f.good())
	if _, err := f.v.Verify(t.Context(), tok); err != nil {
		t.Fatal(err)
	}
	f.srv.Close()
	f.clock = f.clock.Add(2 * time.Hour) // cache stale, endpoint down
	f.v.now = func() time.Time { return f.clock }
	c := f.good()
	c.Expiry = jwt.NewNumericDate(f.clock.Add(time.Hour))
	if _, err := f.v.Verify(t.Context(), f.sign(f.key, f.kid, c)); err != nil {
		t.Errorf("a failed refresh must not lock out a key we hold: %v", err)
	}
}

func TestMiddleware(t *testing.T) {
	f := newFixture(t)
	var seen string
	h := Middleware(f.v, func(r *http.Request) bool { return r.URL.Path == "/healthz" }, nil)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = Email(r.Context()); w.WriteHeader(204) }))
	do := func(path, tok, spoof string) int {
		seen = ""
		req := httptest.NewRequest("GET", path, nil)
		if tok != "" {
			req.Header.Set(Header, tok)
		}
		if spoof != "" {
			req.Header.Set("Cf-Access-Authenticated-User-Email", spoof)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do("/api/admin/users", f.sign(f.key, f.kid, f.good()), ""); c != 204 || seen != who {
		t.Errorf("valid JWT: %d, email %q", c, seen)
	}
	if c := do("/api/admin/users", "", who); c != 403 {
		t.Errorf("the plain email header alone must be refused, got %d", c)
	}
	if c := do("/api/admin/users", "", ""); c != 403 {
		t.Errorf("no JWT must be 403, got %d", c)
	}
	if c := do("/healthz", "", ""); c != 204 {
		t.Errorf("probe path is exempt, got %d", c)
	}
	// No verifier at all denies everything.
	deny := Middleware(nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	rec := httptest.NewRecorder()
	deny.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 403 {
		t.Errorf("nil verifier = %d, want 403", rec.Code)
	}
}

func TestNewRefusesIncompleteConfig(t *testing.T) {
	for name, c := range map[string]Config{
		"no team":   {Aud: aud, Emails: []string{who}},
		"bad team":  {TeamDomain: "evil.com/x", Aud: aud, Emails: []string{who}},
		"no aud":    {TeamDomain: team, Emails: []string{who}},
		"no emails": {TeamDomain: team, Aud: aud},
	} {
		if _, err := New(c, nil); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
	if NormalizeTeamDomain("jolly-river-3a16") != team || NormalizeTeamDomain("https://"+team+"/") != team {
		t.Error("team domain normalisation")
	}
}

func TestHS256WithPublicKeyAndOtherAlgsRefused(t *testing.T) {
	f := newFixture(t)
	// Classic confusion: HS256 signed with the (public) key bytes as the secret.
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("any shared secret that is long enough")},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", f.kid))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Signed(sg).Claims(f.good()).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.v.Verify(t.Context(), tok); err == nil {
		t.Error("HS256 token accepted")
	}
	// RS384 is not RS256 either: the algorithm is pinned.
	sg, _ = jose.NewSigner(jose.SigningKey{Algorithm: jose.RS384, Key: f.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", f.kid))
	tok, _ = jwt.Signed(sg).Claims(f.good()).Serialize()
	if _, err := f.v.Verify(t.Context(), tok); err == nil {
		t.Error("RS384 token accepted")
	}
	// No kid at all.
	sg, _ = jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithType("JWT"))
	tok, _ = jwt.Signed(sg).Claims(f.good()).Serialize()
	if _, err := f.v.Verify(t.Context(), tok); err == nil {
		t.Error("token without a kid accepted")
	}
}

func TestKeysUnavailableDeniesWith503(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"down":      func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) },
		"garbage":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>not json")) },
		"empty set": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"keys":[]}`)) },
	} {
		f := newFixture(t)
		f.over.Store(&handler) // swap what the certs endpoint serves
		tok := f.sign(f.key, f.kid, f.good())
		h := Middleware(f.v, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
		req := httptest.NewRequest("GET", "/api/admin/users", nil)
		req.Header.Set(Header, tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503 (never allow)", name, rec.Code)
		}
	}
}
