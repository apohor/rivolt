package cfaccess

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
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

type svcClaims struct {
	jwt.Claims
	Email      string `json:"email,omitempty"`
	CommonName string `json:"common_name,omitempty"`
}

func (f *fixture) signSvc(key *rsa.PrivateKey, mut func(*svcClaims)) string {
	f.t.Helper()
	c := svcClaims{Claims: f.good().Claims, CommonName: "abc123.access"}
	if mut != nil {
		mut(&c)
	}
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", f.kid))
	if err != nil {
		f.t.Fatal(err)
	}
	s, err := jwt.Signed(sg).Claims(c).Serialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func withTokens(t *testing.T, f *fixture, ids ...string) {
	t.Helper()
	v, err := New(Config{TeamDomain: team, Aud: aud, Emails: []string{who}, ServiceTokens: ids, CertsURL: f.srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v.now = func() time.Time { return f.clock }
	f.v = v
}

func TestServiceTokens(t *testing.T) {
	f := newFixture(t)
	withTokens(t, f, "abc123.access", " other.access ")
	if got, err := f.v.Verify(t.Context(), f.signSvc(f.key, nil)); err != nil || got != "svc:abc123.access" {
		t.Errorf("listed service token = %q, %v; want svc:abc123.access", got, err)
	}
	// Unlisted client id.
	if got, err := f.v.Verify(t.Context(), f.signSvc(f.key, func(c *svcClaims) { c.CommonName = "intruder.access" })); err == nil {
		t.Errorf("unlisted service token accepted as %q", got)
	}
	// Neither an email nor a common name.
	if _, err := f.v.Verify(t.Context(), f.signSvc(f.key, func(c *svcClaims) { c.CommonName = "" })); err == nil {
		t.Error("token with no identity accepted")
	}
	// Still fully verified: forged signer, expired, wrong aud, wrong issuer.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	bad := map[string]string{
		"forged":  f.signSvc(other, nil),
		"expired": f.signSvc(f.key, func(c *svcClaims) { c.Expiry = jwt.NewNumericDate(f.clock.Add(-time.Hour)) }),
		"aud":     f.signSvc(f.key, func(c *svcClaims) { c.Audience = jwt.Audience{"another"} }),
		"issuer":  f.signSvc(f.key, func(c *svcClaims) { c.Issuer = "https://evil.cloudflareaccess.com" }),
		"no exp":  f.signSvc(f.key, func(c *svcClaims) { c.Expiry = nil }),
	}
	for name, tok := range bad {
		if got, err := f.v.Verify(t.Context(), tok); err == nil {
			t.Errorf("%s service token accepted as %q", name, got)
		}
	}
	// A token that names an email goes the email way only: a listed common
	// name does not rescue a disallowed email.
	if got, err := f.v.Verify(t.Context(), f.signSvc(f.key, func(c *svcClaims) { c.Email = "intruder@example.com" })); err == nil {
		t.Errorf("disallowed email rescued by a listed common_name: %q", got)
	}
	// Users still work.
	if got, err := f.v.Verify(t.Context(), f.sign(f.key, f.kid, f.good())); err != nil || got != who {
		t.Errorf("user token = %q, %v", got, err)
	}
}

func TestServiceTokensEmptyByDefault(t *testing.T) {
	f := newFixture(t) // no ServiceTokens configured
	if got, err := f.v.Verify(t.Context(), f.signSvc(f.key, nil)); err == nil {
		t.Errorf("emailless token accepted with no service tokens configured: %q", got)
	}
}

// newKey publishes a second signing key (kid-2) and returns a token for it.
func (f *fixture) newKey() string {
	f.t.Helper()
	nk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.t.Fatal(err)
	}
	f.keys = append(f.keys, jose.JSONWebKey{Key: &nk.PublicKey, KeyID: "kid-2", Algorithm: "RS256", Use: "sig"})
	return f.sign(nk, "kid-2", f.good())
}

func waitHits(t *testing.T, f *fixture, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.hits.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("JWKS endpoint hit %d times, want %d", f.hits.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A refresh in flight must not hold up requests whose kid is already cached;
// only an unknown kid waits for it.
func TestRefreshDoesNotBlockKnownKid(t *testing.T) {
	f := newFixture(t)
	known := f.sign(f.key, f.kid, f.good())
	if _, err := f.v.Verify(t.Context(), known); err != nil { // warm the cache
		t.Fatal(err)
	}
	rot := f.newKey()
	f.clock = f.clock.Add(2 * time.Minute)
	f.v.now = func() time.Time { return f.clock }
	release := make(chan struct{})
	var blocked http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
		<-release
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: f.keys})
	}
	f.over.Store(&blocked)

	type res struct {
		email string
		err   error
	}
	unknownDone := make(chan res, 1)
	go func() { e, err := f.v.Verify(t.Context(), rot); unknownDone <- res{e, err} }()
	waitHits(t, f, 2) // the refresh is now stuck at the endpoint

	// Known kid: answered at once from the cache, with the lock free.
	knownDone := make(chan res, 1)
	go func() { e, err := f.v.Verify(t.Context(), known); knownDone <- res{e, err} }()
	select {
	case r := <-knownDone:
		if r.err != nil {
			t.Errorf("known kid during a refresh: %v", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a known kid waited for the JWKS refresh")
	}
	select {
	case <-unknownDone:
		t.Fatal("the unknown kid returned before the refresh finished")
	default:
	}
	close(release)
	if r := <-unknownDone; r.err != nil || r.email != who {
		t.Errorf("unknown kid after the refresh = %q, %v; want the new key to verify", r.email, r.err)
	}
}

// Concurrent unknown-kid requests share one fetch.
func TestConcurrentRefreshesShareOneFetch(t *testing.T) {
	f := newFixture(t)
	if _, err := f.v.Verify(t.Context(), f.sign(f.key, f.kid, f.good())); err != nil {
		t.Fatal(err)
	}
	rot := f.newKey()
	f.clock = f.clock.Add(2 * time.Minute)
	f.v.now = func() time.Time { return f.clock }
	release := make(chan struct{})
	var blocked http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
		<-release
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: f.keys})
	}
	f.over.Store(&blocked)
	before := f.hits.Load()

	const n = 12
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { _, err := f.v.Verify(t.Context(), rot); errs <- err }()
	}
	waitHits(t, f, before+1)
	time.Sleep(100 * time.Millisecond) // let the others pile up behind the flight
	close(release)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("waiter %d: %v", i, err)
		}
	}
	if got := f.hits.Load() - before; got != 1 {
		t.Errorf("%d concurrent requests caused %d fetches, want 1", n, got)
	}
}

// A refresh that fails still denies the unknown kid as unavailable (503) and a
// held key keeps working: the fail-closed behaviour is unchanged.
func TestFailedSharedRefreshStaysFailClosed(t *testing.T) {
	f := newFixture(t)
	known := f.sign(f.key, f.kid, f.good())
	if _, err := f.v.Verify(t.Context(), known); err != nil {
		t.Fatal(err)
	}
	rot := f.newKey()
	f.clock = f.clock.Add(2 * time.Minute)
	f.v.now = func() time.Time { return f.clock }
	var down http.HandlerFunc = func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) }
	f.over.Store(&down)
	if _, err := f.v.Verify(t.Context(), rot); !errors.Is(err, ErrKeysUnavailable) {
		t.Errorf("unknown kid with the endpoint down = %v, want ErrKeysUnavailable", err)
	}
	if _, err := f.v.Verify(t.Context(), known); err != nil {
		t.Errorf("a held key must survive a failed refresh: %v", err)
	}
}
