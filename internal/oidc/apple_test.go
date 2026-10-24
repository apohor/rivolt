package oidc

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
)

const (
	testAppleClientID = "dev.rivolt.web"
	testAppleTeamID   = "TEAM123456"
	testAppleKeyID    = "KEY1234567"
	testAppleSub      = "001234.abcdef0123456789.0042"
	testAppleRefresh  = "r-apple-refresh-token"
)

// verifyES256 checks a compact ES256 JWT's signature.
func verifyES256(token string, pub *ecdsa.PublicKey) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func jwtPart(t *testing.T, token string, i int) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[i])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func p8PEM(t *testing.T, k *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// fakeApple is Apple's OIDC side: discovery, JWKS, and a token endpoint that checks the minted client secret
// and the PKCE verifier like Apple would, then answers with a signed id_token.
type fakeApple struct {
	t        *testing.T
	srv      *httptest.Server
	idKey    *rsa.PrivateKey
	clientPK *ecdsa.PublicKey // the Sign in with Apple key's public half

	mu        sync.Mutex
	challenge string
	nonce     string
	claims    map[string]any // id_token claims on top of iss/aud/sub/nonce/iat/exp
	exchanges int
}

func newFakeApple(t *testing.T, clientPK *ecdsa.PublicKey) *fakeApple {
	idKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fa := &fakeApple{t: t, idKey: idKey, clientPK: clientPK}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                fa.srv.URL,
			"authorization_endpoint":                fa.srv.URL + "/auth/authorize",
			"token_endpoint":                        fa.srv.URL + "/auth/token",
			"jwks_uri":                              fa.srv.URL + "/auth/keys",
			"response_modes_supported":              []string{"query", "fragment", "form_post"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/auth/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &idKey.PublicKey, KeyID: "fake-1", Algorithm: "RS256", Use: "sig",
		}}})
	})
	mux.HandleFunc("/auth/token", fa.token)
	fa.srv = httptest.NewServer(mux)
	t.Cleanup(fa.srv.Close)
	return fa
}

func (fa *fakeApple) token(w http.ResponseWriter, r *http.Request) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.exchanges++
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form", http.StatusBadRequest)
		return
	}
	fail := func(msg string) {
		fa.t.Errorf("fake apple token endpoint: %s", msg)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
	}
	f := r.PostForm
	if f.Get("grant_type") != "authorization_code" || f.Get("code") != "the-code" || f.Get("client_id") != testAppleClientID {
		fail("grant_type/code/client_id: " + f.Encode())
		return
	}
	// PKCE: Apple's support isn't documented; the fake enforces it so the flow is shown to send it.
	if pkceChallenge(f.Get("code_verifier")) != fa.challenge {
		fail("code_verifier doesn't match the challenge")
		return
	}
	sec := f.Get("client_secret")
	if !verifyES256(sec, fa.clientPK) {
		fail("client_secret not signed with the Apple key")
		return
	}
	h, c := jwtPart(fa.t, sec, 0), jwtPart(fa.t, sec, 1)
	if h["alg"] != "ES256" || h["kid"] != testAppleKeyID {
		fail("client_secret header")
		return
	}
	now := float64(time.Now().Unix())
	if c["iss"] != testAppleTeamID || c["sub"] != testAppleClientID || c["aud"] != appleIssuer ||
		c["iat"].(float64) > now+5 || c["exp"].(float64) <= now || c["exp"].(float64)-c["iat"].(float64) > 15*60 {
		fail("client_secret claims")
		return
	}
	claims := map[string]any{
		"iss": fa.srv.URL, "aud": testAppleClientID, "sub": testAppleSub, "nonce": fa.nonce,
		"iat": time.Now().Unix(), "exp": time.Now().Add(10 * time.Minute).Unix(),
	}
	for k, v := range fa.claims {
		claims[k] = v
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: fa.idKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "fake-1"))
	if err != nil {
		fa.t.Fatal(err)
	}
	payload, _ := json.Marshal(claims)
	obj, err := signer.Sign(payload)
	if err != nil {
		fa.t.Fatal(err)
	}
	idTok, _ := obj.CompactSerialize()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "a-apple", "token_type": "Bearer", "expires_in": 3600,
		"refresh_token": testAppleRefresh, "id_token": idTok,
	})
}

// fakeLinks is AppleLinks over a map.
type fakeLinks struct {
	bySub   map[string]string
	linkErr error
	linked  []string // "uid sub refresh"
}

func (f *fakeLinks) UsernameBySub(_ context.Context, sub string) (string, bool, error) {
	u, ok := f.bySub[sub]
	return u, ok, nil
}

func (f *fakeLinks) Link(_ context.Context, uid uuid.UUID, sub, refresh string) error {
	if f.linkErr != nil {
		return f.linkErr
	}
	f.linked = append(f.linked, uid.String()+" "+sub+" "+refresh)
	return nil
}

type ensured struct {
	username, email, display string
	verified                 bool
}

type appleHarness struct {
	t        *testing.T
	fa       *fakeApple
	links    *fakeLinks
	router   chi.Router
	logs     *bytes.Buffer
	existing map[string]bool
	ensured  []ensured
	issued   []uuid.UUID
	capErr   error
}

func newAppleHarness(t *testing.T) *appleHarness {
	t.Helper()
	appleKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := &appleHarness{
		t:        t,
		fa:       newFakeApple(t, &appleKey.PublicKey),
		links:    &fakeLinks{bySub: map[string]string{}},
		logs:     &bytes.Buffer{},
		existing: map[string]bool{},
	}
	svc, err := New(context.Background(), Config{
		IssueSession: func(_ http.ResponseWriter, _ *http.Request, uid uuid.UUID) error {
			h.issued = append(h.issued, uid)
			return nil
		},
		EnsureUser: func(_ context.Context, username, email, display string, verified bool) (uuid.UUID, error) {
			h.ensured = append(h.ensured, ensured{username, email, display, verified})
			return uuid.NewSHA1(uuid.NameSpaceURL, []byte(username)), nil
		},
		UserExists: func(_ context.Context, username string) (bool, error) { return h.existing[username], nil },
		CapGuard:   func(context.Context, string, string, bool) error { return h.capErr },
		Logger:     slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Providers: []ProviderConfig{
			{
				Name: "apple", Kind: kindApple, DisplayName: "Apple", IssuerURL: h.fa.srv.URL,
				ClientID: testAppleClientID, RedirectURL: "https://rivolt.test/api/auth/oidc/apple/callback",
				AppleTeamID: testAppleTeamID, AppleKeyID: testAppleKeyID, ApplePrivateKey: p8PEM(t, appleKey),
			},
			// GitHub needs no discovery: the non-Apple provider for the cookie and method pins.
			{Name: "github", Kind: "oauth2-github", ClientID: "gh", RedirectURL: "https://rivolt.test/api/auth/oidc/github/callback"},
		},
		AppleLinks: h.links,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.providers) != 2 {
		t.Fatalf("providers ready = %d, want 2", len(svc.providers))
	}
	h.router = chi.NewRouter()
	h.router.Route("/api/auth", svc.Mount)
	return h
}

// start runs /start and returns the Apple state cookie and the authorize URL; it primes the fake with the
// flow's nonce and PKCE challenge.
func (h *appleHarness) start() (*http.Cookie, *url.URL) {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/apple/start", nil))
	if rec.Code != http.StatusFound {
		h.t.Fatalf("start = %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != appleStateCookie {
		h.t.Fatalf("start cookies = %+v, want only %s", cookies, appleStateCookie)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.fa.mu.Lock()
	h.fa.nonce = loc.Query().Get("nonce")
	h.fa.challenge = loc.Query().Get("code_challenge")
	h.fa.mu.Unlock()
	return cookies[0], loc
}

// callback POSTs Apple's form to the callback with the state cookie.
func (h *appleHarness) callback(c *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/oidc/apple/callback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c != nil {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *appleHarness) signIn(claims map[string]any, user string) *httptest.ResponseRecorder {
	h.t.Helper()
	h.fa.claims = claims
	c, loc := h.start()
	form := url.Values{"code": {"the-code"}, "state": {loc.Query().Get("state")}}
	if user != "" {
		form.Set("user", user)
	}
	return h.callback(c, form)
}

// noAppleIDsInLogs fails if a log line holds the sub, an email, or a token.
func (h *appleHarness) noAppleIDsInLogs(extra ...string) {
	h.t.Helper()
	for _, s := range append([]string{testAppleSub, testAppleRefresh, "@"}, extra...) {
		if strings.Contains(h.logs.String(), s) {
			h.t.Errorf("logs contain %q:\n%s", s, h.logs.String())
		}
	}
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("error")
}

// clearsAppleCookie reports whether rec expires the Apple state cookie (single use).
func clearsAppleCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == appleStateCookie && c.MaxAge < 0 && c.Path == "/api/auth/oidc/apple" {
			return true
		}
	}
	return false
}

func TestAppleStartCookieAndAuthorizeURL(t *testing.T) {
	h := newAppleHarness(t)
	c, loc := h.start()
	if c.Path != "/api/auth/oidc/apple" || c.Domain != "" || !c.Secure || !c.HttpOnly ||
		c.SameSite != http.SameSiteNoneMode || c.MaxAge <= 0 || c.MaxAge > 600 {
		t.Errorf("apple state cookie = %+v; want host-only, Path=/api/auth/oidc/apple, Secure, HttpOnly, SameSite=None, 0<Max-Age<=600", c)
	}
	q := loc.Query()
	if q.Get("response_mode") != "form_post" || q.Get("scope") != "name email" ||
		q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("nonce") != q.Get("state") ||
		q.Get("client_id") != testAppleClientID || q.Get("redirect_uri") != "https://rivolt.test/api/auth/oidc/apple/callback" {
		t.Errorf("authorize URL = %s", loc)
	}
}

// Pins DA's condition: only Apple's state cookie is SameSite=None; every other provider's stays Lax.
func TestOtherProvidersStateCookieStaysLax(t *testing.T) {
	h := newAppleHarness(t)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/github/start", nil))
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != stateCookieName || cookies[0].SameSite != http.SameSiteLaxMode ||
		cookies[0].Path != "/api/auth/oidc" {
		t.Fatalf("github start cookies = %+v, want one Lax %s", cookies, stateCookieName)
	}
	if strings.Contains(rec.Header().Get("Location"), "response_mode") {
		t.Errorf("non-Apple authorize URL carries response_mode: %s", rec.Header().Get("Location"))
	}
}

func TestAppleCallbackMethods(t *testing.T) {
	h := newAppleHarness(t)
	for _, tc := range []struct{ method, provider string }{
		{http.MethodGet, "apple"},   // Apple answers with form_post only
		{http.MethodPost, "github"}, // nobody else gets the cross-site POST
	} {
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, httptest.NewRequest(tc.method, "/api/auth/oidc/"+tc.provider+"/callback", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s callback = %d, want 405", tc.method, tc.provider, rec.Code)
		}
	}
}

func TestAppleNewAccount(t *testing.T) {
	h := newAppleHarness(t)
	rec := h.signIn(map[string]any{"email": "ada@example.com", "email_verified": "true", "is_private_email": "false"},
		`{"name":{"firstName":"Ada","lastName":"Lovelace"},"email":"ada@example.com"}`)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d → %s", rec.Code, rec.Header().Get("Location"))
	}
	if !clearsAppleCookie(rec) {
		t.Error("callback didn't clear the Apple state cookie")
	}
	want := ensured{"ada@example.com", "ada@example.com", "Ada Lovelace", true}
	if len(h.ensured) != 1 || h.ensured[0] != want {
		t.Errorf("ensureUser = %+v, want %+v", h.ensured, want)
	}
	if len(h.issued) != 1 || len(h.links.linked) != 1 ||
		h.links.linked[0] != h.issued[0].String()+" "+testAppleSub+" "+testAppleRefresh {
		t.Errorf("issued %v, linked %v", h.issued, h.links.linked)
	}
	h.noAppleIDsInLogs("Ada", "Lovelace")
}

// A linked Apple identity signs in to its account whatever Apple's email says; the account keeps its own
// address and name.
func TestAppleLinkedIdentity(t *testing.T) {
	h := newAppleHarness(t)
	h.links.bySub[testAppleSub] = "owner@example.com"
	h.existing["owner@example.com"] = true
	rec := h.signIn(map[string]any{"email": "x7q@privaterelay.appleid.com", "email_verified": true, "is_private_email": true},
		`{"name":{"firstName":"Someone","lastName":"Else"}}`)
	if errorCode(t, rec) != "" || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback → %s", rec.Header().Get("Location"))
	}
	want := ensured{"owner@example.com", "", "", false}
	if len(h.ensured) != 1 || h.ensured[0] != want {
		t.Errorf("ensureUser = %+v, want %+v", h.ensured, want)
	}
	if len(h.issued) != 1 || len(h.links.linked) != 1 {
		t.Errorf("issued %v, linked %v", h.issued, h.links.linked)
	}
	h.noAppleIDsInLogs("x7q", "Someone")
}

// A verified, non-relay email joins the existing account like Google/GitHub; the name doesn't overwrite it.
func TestAppleVerifiedEmailJoinsExisting(t *testing.T) {
	h := newAppleHarness(t)
	h.existing["ada@example.com"] = true
	rec := h.signIn(map[string]any{"email": "ada@example.com", "email_verified": "true"},
		`{"name":{"firstName":"Ada","lastName":"Lovelace"}}`)
	if errorCode(t, rec) != "" {
		t.Fatalf("callback → %s", rec.Header().Get("Location"))
	}
	want := ensured{"ada@example.com", "ada@example.com", "", true}
	if len(h.ensured) != 1 || h.ensured[0] != want {
		t.Errorf("ensureUser = %+v, want %+v", h.ensured, want)
	}
}

// A private relay address never joins an existing account.
func TestAppleRelayNeverJoins(t *testing.T) {
	h := newAppleHarness(t)
	h.existing["x7q@privaterelay.appleid.com"] = true
	rec := h.signIn(map[string]any{"email": "x7q@privaterelay.appleid.com", "email_verified": "true", "is_private_email": "true"}, "")
	if got := errorCode(t, rec); got != "apple_relay" {
		t.Fatalf("error = %q, want apple_relay", got)
	}
	if len(h.ensured) != 0 || len(h.issued) != 0 || len(h.links.linked) != 0 {
		t.Errorf("refused sign-in still ensured %v / issued %v / linked %v", h.ensured, h.issued, h.links.linked)
	}
	if !clearsAppleCookie(rec) {
		t.Error("refusal didn't clear the Apple state cookie")
	}
	h.noAppleIDsInLogs("x7q")
}

// A new relay account is fine: it's the user's own fresh account.
func TestAppleRelayNewAccount(t *testing.T) {
	h := newAppleHarness(t)
	rec := h.signIn(map[string]any{"email": "x7q@privaterelay.appleid.com", "email_verified": "true", "is_private_email": "true"}, "")
	if errorCode(t, rec) != "" || len(h.issued) != 1 || len(h.links.linked) != 1 {
		t.Fatalf("callback → %s; issued %v linked %v", rec.Header().Get("Location"), h.issued, h.links.linked)
	}
}

func TestAppleLinkConflictRefused(t *testing.T) {
	h := newAppleHarness(t)
	h.existing["ada@example.com"] = true
	h.links.linkErr = ErrAppleLinkConflict
	rec := h.signIn(map[string]any{"email": "ada@example.com", "email_verified": "true"}, "")
	if got := errorCode(t, rec); got != "apple_linked" {
		t.Fatalf("error = %q, want apple_linked", got)
	}
	if len(h.issued) != 0 {
		t.Errorf("session issued despite the link conflict")
	}
	h.noAppleIDsInLogs()
}

func TestAppleCapOverflowOmitsEmail(t *testing.T) {
	h := newAppleHarness(t)
	h.capErr = ErrCapExceeded
	rec := h.signIn(map[string]any{"email": "ada@example.com", "email_verified": "true"}, "")
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/signup/full?") || strings.Contains(loc, "email") || strings.Contains(loc, "@") {
		t.Errorf("cap overflow → %q, want /signup/full without the email", loc)
	}
}

func TestAppleCallbackRejectsBadState(t *testing.T) {
	h := newAppleHarness(t)
	c, _ := h.start()
	rec := h.callback(c, url.Values{"code": {"the-code"}, "state": {"forged"}})
	if got := errorCode(t, rec); got != "session_expired" || !clearsAppleCookie(rec) {
		t.Errorf("forged state: error %q, cleared %v", got, clearsAppleCookie(rec))
	}
	// No cookie at all (a replay after the first callback cleared it).
	_, loc := h.start()
	rec = h.callback(nil, url.Values{"code": {"the-code"}, "state": {loc.Query().Get("state")}})
	if got := errorCode(t, rec); got != "session_expired" {
		t.Errorf("no cookie: error %q", got)
	}
	// Apple reports the user cancelled.
	c, loc = h.start()
	rec = h.callback(c, url.Values{"error": {"user_cancelled_authorize"}, "state": {loc.Query().Get("state")}})
	if got := errorCode(t, rec); got != "idp_error" {
		t.Errorf("cancel: error %q", got)
	}
	if h.fa.exchanges != 0 || len(h.issued) != 0 {
		t.Errorf("exchanges %d, issued %v; want none", h.fa.exchanges, h.issued)
	}
}

func TestAppleCallbackBodyCap(t *testing.T) {
	h := newAppleHarness(t)
	c, loc := h.start()
	rec := h.callback(c, url.Values{"code": {"the-code"}, "state": {loc.Query().Get("state")},
		"user": {strings.Repeat("a", appleFormMax)}})
	if got := errorCode(t, rec); got != "session_expired" || h.fa.exchanges != 0 {
		t.Errorf("oversized form: error %q, exchanges %d", got, h.fa.exchanges)
	}
}

func TestAppleNonceMismatch(t *testing.T) {
	h := newAppleHarness(t)
	h.fa.claims = map[string]any{"email": "ada@example.com", "email_verified": "true"}
	c, loc := h.start()
	h.fa.nonce = "another-flow"
	rec := h.callback(c, url.Values{"code": {"the-code"}, "state": {loc.Query().Get("state")}})
	if got := errorCode(t, rec); got != "verification_failed" || len(h.issued) != 0 {
		t.Errorf("nonce mismatch: error %q, issued %v", got, h.issued)
	}
}

func TestAppleWithoutLinksRefuses(t *testing.T) {
	h := newAppleHarness(t)
	h.router = chi.NewRouter()
	svc, err := New(context.Background(), Config{
		IssueSession: func(http.ResponseWriter, *http.Request, uuid.UUID) error { t.Fatal("session issued"); return nil },
		EnsureUser: func(context.Context, string, string, string, bool) (uuid.UUID, error) {
			t.Fatal("ensureUser called")
			return uuid.Nil, nil
		},
		Logger: slog.New(slog.NewTextHandler(h.logs, nil)),
		Providers: []ProviderConfig{{
			Name: "apple", Kind: kindApple, IssuerURL: h.fa.srv.URL, ClientID: testAppleClientID,
			RedirectURL: "https://rivolt.test/api/auth/oidc/apple/callback",
			AppleTeamID: testAppleTeamID, AppleKeyID: testAppleKeyID, ApplePrivateKey: p8PEM(t, mustP256(t)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.router.Route("/api/auth", svc.Mount)
	h.fa.mu.Lock()
	h.fa.clientPK = &svc.providers["apple"].apple.key.PublicKey
	h.fa.mu.Unlock()
	if got := errorCode(t, h.signIn(map[string]any{"email": "ada@example.com", "email_verified": "true"}, "")); got != "server_error" {
		t.Errorf("error = %q, want server_error", got)
	}
}

func mustP256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestAppleClientSecret(t *testing.T) {
	k := mustP256(t)
	a := &appleConfig{teamID: testAppleTeamID, keyID: testAppleKeyID, key: k}
	now := time.Unix(1_800_000_000, 0)
	sec, err := a.clientSecret(testAppleClientID, now)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyES256(sec, &k.PublicKey) {
		t.Fatal("signature doesn't verify")
	}
	if verifyES256(sec, &mustP256(t).PublicKey) {
		t.Fatal("verifies under another key")
	}
	h, c := jwtPart(t, sec, 0), jwtPart(t, sec, 1)
	if h["alg"] != "ES256" || h["kid"] != testAppleKeyID {
		t.Errorf("header = %v", h)
	}
	if c["iss"] != testAppleTeamID || c["sub"] != testAppleClientID || c["aud"] != "https://appleid.apple.com" ||
		c["iat"] != float64(now.Unix()) || c["exp"] != float64(now.Add(5*time.Minute).Unix()) {
		t.Errorf("claims = %v", c)
	}
}

func TestParseAppleKey(t *testing.T) {
	if _, err := parseAppleKey(p8PEM(t, mustP256(t))); err != nil {
		t.Errorf("P-256 p8: %v", err)
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 1024)
	rsaDER, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	for name, in := range map[string]string{
		"empty":  "",
		"junk":   "not a key",
		"P-384":  p8PEM(t, p384),
		"RSA":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaDER})),
		"broken": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
	} {
		if _, err := parseAppleKey(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFlexBool(t *testing.T) {
	for in, want := range map[string]bool{
		`true`: true, `"true"`: true, `false`: false, `"false"`: false, `null`: false, `"yes"`: false, `1`: false,
	} {
		var v struct {
			B flexBool `json:"b"`
		}
		if err := json.Unmarshal([]byte(`{"b":`+in+`}`), &v); err != nil || bool(v.B) != want {
			t.Errorf("%s → %v (%v), want %v", in, v.B, err, want)
		}
	}
}

func TestAppleFirstName(t *testing.T) {
	for in, want := range map[string]string{
		"": "",
		`{"name":{"firstName":"Ada","lastName":"Lovelace"},"email":"a@b.c"}`: "Ada Lovelace",
		`{"name":{"firstName":" Ada "}}`:                                     "Ada",
		`{"email":"a@b.c"}`:                                                  "",
		`not json`:                                                           "",
		`{"name":{"firstName":"` + strings.Repeat("x", 300) + `"}}`:          strings.Repeat("x", 100),
	} {
		if got := appleFirstName(in); got != want {
			t.Errorf("appleFirstName(%.40q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseProvidersFromEnvApple(t *testing.T) {
	p8 := p8PEM(t, mustP256(t))
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	base := map[string]string{
		"RIVOLT_OIDC_PROVIDERS":         "apple",
		"RIVOLT_OIDC_APPLE_KIND":        "apple",
		"RIVOLT_OIDC_APPLE_CLIENT_ID":   testAppleClientID,
		"RIVOLT_OIDC_APPLE_TEAM_ID":     testAppleTeamID,
		"RIVOLT_OIDC_APPLE_KEY_ID":      testAppleKeyID,
		"RIVOLT_OIDC_APPLE_PRIVATE_KEY": p8,
	}
	got, err := ParseProvidersFromEnv(env(base), "https://rivolt.test")
	if err != nil {
		t.Fatal(err)
	}
	pc := got[0]
	if pc.Kind != kindApple || pc.IssuerURL != appleIssuer || pc.AppleTeamID != testAppleTeamID ||
		pc.AppleKeyID != testAppleKeyID || pc.ApplePrivateKey != p8 || pc.ClientSecret != "" ||
		pc.RedirectURL != "https://rivolt.test/api/auth/oidc/apple/callback" {
		t.Errorf("config = %+v", pc)
	}
	for _, drop := range []string{"RIVOLT_OIDC_APPLE_TEAM_ID", "RIVOLT_OIDC_APPLE_KEY_ID", "RIVOLT_OIDC_APPLE_PRIVATE_KEY", "RIVOLT_OIDC_APPLE_CLIENT_ID"} {
		m := map[string]string{}
		for k, v := range base {
			if k != drop {
				m[k] = v
			}
		}
		if _, err := ParseProvidersFromEnv(env(m), "https://rivolt.test"); err == nil {
			t.Errorf("without %s: accepted", drop)
		}
	}
	base["RIVOLT_OIDC_APPLE_PRIVATE_KEY"] = "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"
	if _, err := ParseProvidersFromEnv(env(base), "https://rivolt.test"); err == nil || strings.Contains(err.Error(), "AAAA") {
		t.Errorf("bad key: err = %v (must fail without echoing the key)", err)
	}
}
