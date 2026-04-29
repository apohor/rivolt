// Package oidc adds an OIDC sign-in path alongside the static
// operator credential and the trusted-proxy header in
// internal/auth.
//
// # Why a separate package
//
// Auth's job is "given a request, produce a user_id and plant a
// session cookie". That contract is upheld by Login (static creds)
// and Middleware (header). Adding a third issuer in the same
// package would couple every code path to the OIDC dependency
// chain (go-oidc, go-jose, golang.org/x/oauth2). Keeping OIDC out
// here means a self-host build that doesn't enable OIDC pays zero
// runtime cost and zero binary-size cost beyond the imports
// themselves.
//
// # The flow
//
//	GET  /api/auth/oidc/{provider}/start
//	     → 302 to provider.AuthURL(state) with a state+nonce cookie
//
//	GET  /api/auth/oidc/{provider}/callback?code&state
//	     → verify state cookie matches query param
//	     → exchange code for tokens
//	     → verify ID token signature + nonce + audience
//	     → resolve identity → stable UUID via auth's UserIDFor
//	     → upsert users row → mint Rivolt session cookie
//	     → 302 back into the SPA (configurable post-login URL)
//
// # Identity resolution
//
// We use the verified email claim as the auth username when
// present (Google + most enterprise IdPs always emit it for sign-
// in scopes). Falling back to `iss + ":" + sub` keeps the
// deterministic-UUID contract working even for IdPs that withhold
// email. The same UUIDv5 namespace is shared with cookie / header
// auth, so a user who signs in once by password and once by OIDC
// (same email) lands on the same tenant key — no data fork.
//
// # Security checklist (RFC 6749 §10 + OpenID Connect Core §3.1.2)
//
//   - state: random 32 bytes, set in an HttpOnly+Secure cookie,
//     compared with constant time at callback. CSRF-class
//     defense.
//   - nonce: same 32-byte token reused as the OIDC nonce; verified
//     by go-oidc against the ID token claim.
//   - PKCE: S256, verifier stored in the same state cookie alongside
//     the state value. Defends against authcode interception on
//     hostile networks.
//   - Cookie scope: the state cookie is per-callback only,
//     SameSite=Lax (Strict would block the cross-site redirect
//     itself), Path=/api/auth/oidc, MaxAge=10m.
//
// # Multi-provider
//
// One Service hosts a registry of providers keyed by short name
// (e.g. "google", "authentik"). The router mounts every provider
// under /api/auth/oidc/{name}/{start|callback}; a provider that
// fails to bootstrap (DNS, bad issuer URL) is logged and skipped
// rather than crashing the whole server.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

// ProviderConfig is one IdP's static configuration, supplied by
// the operator via env. All fields are required except Scopes
// (which defaults to {"openid","email","profile"}).
type ProviderConfig struct {
	// Name is the short URL slug — e.g. "google". Lowercased and
	// alphanum-only is recommended; we don't enforce it because
	// the operator owns their own URL space.
	Name string

	// DisplayName is what the SPA puts on the button. "Google",
	// "Authentik", "GitHub". Falls back to Name if empty.
	DisplayName string

	// IssuerURL is the OIDC discovery root (without
	// /.well-known/openid-configuration; go-oidc appends it).
	// Examples: "https://accounts.google.com",
	// "https://auth.example.com".
	IssuerURL string

	// ClientID / ClientSecret are the OAuth2 application
	// credentials. ClientSecret may be empty for public clients
	// (PKCE-only), though most server-side flows ship one.
	ClientID     string
	ClientSecret string

	// RedirectURL is the absolute URL of /api/auth/oidc/{name}/
	// callback as the IdP will redirect the browser there. We
	// build it once at config time so a misconfigured base URL
	// fails fast.
	RedirectURL string

	// Scopes overrides the default scope set. Most IdPs are happy
	// with the default; some (Authentik) need an extra scope to
	// emit groups.
	Scopes []string
}

// Service is the OIDC handler set, registered under
// /api/auth/oidc by api.go. Multiple providers live in one
// Service.
type Service struct {
	// authIssuer hands out cookies for an authenticated user_id.
	// Implemented by *auth.Service.IssueSession. Kept as a func
	// dependency rather than an import to avoid a cycle and to
	// make the handler trivially testable.
	issueSession func(w http.ResponseWriter, r *http.Request, uid uuid.UUID) error

	// ensureUser upserts the user row for (username, email,
	// displayName) and returns the stable UUID. Wired to
	// db.EnsureUserFull by main.
	ensureUser func(ctx context.Context, username, email, displayName string) (uuid.UUID, error)

	// userIDFor is shared with auth.Service. Currently unused by
	// OIDC (we always go through ensureUser, which calls
	// UserIDFor itself), but kept on the struct in case a future
	// path wants to resolve UUIDs without writing a DB row.
	userIDFor func(string) uuid.UUID

	// postLoginURL is where the callback redirects after a
	// successful sign-in. Defaults to "/" so the SPA's normal
	// boot flow takes over.
	postLoginURL string

	// secureCookie marks the state cookie Secure; should match
	// auth.Service's setting.
	secureCookie bool

	// log is the structured logger; defaults to slog.Default().
	log *slog.Logger

	// providers indexed by Name. Built once at New time; never
	// mutated, so reads are lock-free.
	providers map[string]*provider
}

type provider struct {
	cfg      ProviderConfig
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

// Config bundles the wiring main hands to New. Splitting it from
// ProviderConfig keeps the per-IdP knobs separate from the
// service-wide identity / cookie wiring.
type Config struct {
	IssueSession func(w http.ResponseWriter, r *http.Request, uid uuid.UUID) error
	EnsureUser   func(ctx context.Context, username, email, displayName string) (uuid.UUID, error)
	UserIDFor    func(string) uuid.UUID
	PostLoginURL string
	SecureCookie bool
	Logger       *slog.Logger
	Providers    []ProviderConfig
}

// New builds a Service. Providers that fail to bootstrap (DNS
// failure, invalid issuer URL, malformed redirect) are logged at
// WARN and skipped — the rest still come up. That's the right
// posture for a homelab where one IdP being temporarily down
// shouldn't take the whole login page offline.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.IssueSession == nil {
		return nil, errors.New("oidc.New: IssueSession is required")
	}
	if cfg.EnsureUser == nil {
		return nil, errors.New("oidc.New: EnsureUser is required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	post := strings.TrimSpace(cfg.PostLoginURL)
	if post == "" {
		post = "/"
	}
	s := &Service{
		issueSession: cfg.IssueSession,
		ensureUser:   cfg.EnsureUser,
		userIDFor:    cfg.UserIDFor,
		postLoginURL: post,
		secureCookie: cfg.SecureCookie,
		log:          log,
		providers:    make(map[string]*provider),
	}
	for _, pc := range cfg.Providers {
		p, err := buildProvider(ctx, pc)
		if err != nil {
			log.Warn("oidc: provider bootstrap failed; skipping",
				"provider", pc.Name, "issuer", pc.IssuerURL, "err", err)
			continue
		}
		s.providers[pc.Name] = p
		log.Info("oidc: provider ready", "provider", pc.Name, "issuer", pc.IssuerURL)
	}
	return s, nil
}

func buildProvider(ctx context.Context, pc ProviderConfig) (*provider, error) {
	if pc.Name == "" {
		return nil, errors.New("name required")
	}
	if pc.IssuerURL == "" {
		return nil, errors.New("issuer URL required")
	}
	if pc.ClientID == "" {
		return nil, errors.New("client ID required")
	}
	if pc.RedirectURL == "" {
		return nil, errors.New("redirect URL required")
	}
	op, err := oidc.NewProvider(ctx, pc.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	scopes := pc.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	return &provider{
		cfg: pc,
		verifier: op.Verifier(&oidc.Config{
			ClientID: pc.ClientID,
		}),
		oauth: &oauth2.Config{
			ClientID:     pc.ClientID,
			ClientSecret: pc.ClientSecret,
			RedirectURL:  pc.RedirectURL,
			// Force client_secret_post. Without this, Go's oauth2
			// library auto-detects (defaults to client_secret_basic
			// on the first attempt). Authelia's per-client
			// token_endpoint_auth_method enforcement rejects the
			// mismatched method outright instead of letting the
			// library fall back, so we pin the style here.
			Endpoint: func() oauth2.Endpoint {
				ep := op.Endpoint()
				ep.AuthStyle = oauth2.AuthStyleInParams
				return ep
			}(),
			Scopes: scopes,
		},
	}, nil
}

// Mount installs the OIDC routes onto r under /oidc:
//
//	GET /oidc/{provider}/start
//	GET /oidc/{provider}/callback
//
// Caller is api.go, which mounts this whole group under /api/auth.
func (s *Service) Mount(r chi.Router) {
	r.Route("/oidc", func(r chi.Router) {
		r.Get("/", s.handleProviders)
		r.Get("/{provider}/start", s.handleStart)
		r.Get("/{provider}/callback", s.handleCallback)
	})
}

// Providers returns the registered providers in a JSON-friendly
// shape — used by the SPA to decide whether to render any social-
// login buttons. Returning the start URL pre-built keeps the
// frontend free of routing knowledge.
func (s *Service) Providers() []ProviderListing {
	if s == nil {
		return nil
	}
	out := make([]ProviderListing, 0, len(s.providers))
	for _, p := range s.providers {
		display := p.cfg.DisplayName
		if display == "" {
			display = p.cfg.Name
		}
		out = append(out, ProviderListing{
			Name:        p.cfg.Name,
			DisplayName: display,
			StartURL:    "/api/auth/oidc/" + p.cfg.Name + "/start",
		})
	}
	return out
}

// ProviderListing is the public shape of a registered IdP.
type ProviderListing struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	StartURL    string `json:"start_url"`
}

func (s *Service) handleProviders(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Providers())
}

// stateCookieName scopes the per-flow state to the OIDC subtree.
// One cookie per concurrent login attempt; a second attempt on
// the same browser overwrites the first, which is fine because we
// don't want the user juggling two pending flows anyway.
const stateCookieName = "rivolt_oidc_state"

// stateMaxAge is the user's window to complete the IdP roundtrip.
// Ten minutes is plenty for an interactive login (Google can take
// ~15s when the user has to consent to scopes for the first
// time); much longer just expands the replay window.
const stateMaxAge = 10 * time.Minute

// flowState is the body of the state cookie. We store it base64-
// JSON rather than as separate cookies to keep the bookkeeping
// (CSRF token, PKCE verifier, return URL) atomic — the browser
// either presents a complete state or none.
type flowState struct {
	Provider string `json:"p"`
	State    string `json:"s"` // CSRF/state token
	Nonce    string `json:"n"` // OIDC nonce
	Verifier string `json:"v"` // PKCE code_verifier
	Return   string `json:"r"` // post-login redirect target
}

func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "provider")
	p, ok := s.providers[name]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}

	// state == nonce: we generate one 32-byte random and use it
	// for both. Reusing is safe because the nonce is opaque to
	// us — only the IdP echoes it back inside the signed ID
	// token, and only we compare the state cookie. They can't
	// collide because they live in different protocols.
	stateRaw, err := randURL(32)
	if err != nil {
		http.Error(w, "rng", http.StatusInternalServerError)
		return
	}
	verifier, err := randURL(48) // 64 chars after base64 — within RFC 7636 43-128 range.
	if err != nil {
		http.Error(w, "rng", http.StatusInternalServerError)
		return
	}
	challenge := pkceChallenge(verifier)

	ret := strings.TrimSpace(r.URL.Query().Get("return"))
	// Only honour same-site, non-protocol-relative paths to avoid
	// open-redirect via ?return=https://evil.example.
	if ret == "" || !strings.HasPrefix(ret, "/") || strings.HasPrefix(ret, "//") {
		ret = s.postLoginURL
	}

	st := flowState{
		Provider: name,
		State:    stateRaw,
		Nonce:    stateRaw,
		Verifier: verifier,
		Return:   ret,
	}
	cookieVal, err := encodeState(st)
	if err != nil {
		http.Error(w, "encode state", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    cookieVal,
		Path:     "/api/auth/oidc",
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode, // Strict blocks the cross-site IdP→app redirect.
		MaxAge:   int(stateMaxAge.Seconds()),
	})

	authURL := p.oauth.AuthCodeURL(stateRaw,
		oidc.Nonce(stateRaw),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "provider")
	p, ok := s.providers[name]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	c, err := r.Cookie(stateCookieName)
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}
	st, err := decodeState(c.Value)
	if err != nil {
		http.Error(w, "bad state cookie", http.StatusBadRequest)
		return
	}
	// Defence in depth: provider mismatch means the user
	// completed a different flow than the cookie claims. Refuse.
	if st.Provider != name {
		http.Error(w, "provider mismatch", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		s.log.Warn("oidc: idp returned error",
			"provider", name,
			"error", errParam,
			"description", q.Get("error_description"))
		s.clearStateCookie(w)
		http.Error(w, "idp returned error: "+errParam, http.StatusBadRequest)
		return
	}
	gotState := q.Get("state")
	if subtle.ConstantTimeCompare([]byte(gotState), []byte(st.State)) != 1 {
		s.clearStateCookie(w)
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	code := q.Get("code")
	if code == "" {
		s.clearStateCookie(w)
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	tok, err := p.oauth.Exchange(r.Context(), code,
		oauth2.SetAuthURLParam("code_verifier", st.Verifier),
	)
	if err != nil {
		s.log.Warn("oidc: token exchange failed", "provider", name, "err", err)
		s.clearStateCookie(w)
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		s.clearStateCookie(w)
		http.Error(w, "missing id_token", http.StatusBadGateway)
		return
	}
	idTok, err := p.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		s.log.Warn("oidc: id_token verify failed", "provider", name, "err", err)
		s.clearStateCookie(w)
		http.Error(w, "id_token verify failed", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idTok.Nonce), []byte(st.Nonce)) != 1 {
		s.clearStateCookie(w)
		http.Error(w, "nonce mismatch", http.StatusUnauthorized)
		return
	}

	var claims struct {
		Sub               string `json:"sub"`
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}
	if err := idTok.Claims(&claims); err != nil {
		s.clearStateCookie(w)
		http.Error(w, "claims decode", http.StatusInternalServerError)
		return
	}
	username, email := resolveIdentity(p.cfg.IssuerURL, claims.Sub, claims.Email, claims.EmailVerified, claims.PreferredUsername)
	if username == "" {
		// Without a stable identity we can't safely mint a
		// session — refusing is correct.
		s.clearStateCookie(w)
		http.Error(w, "id token missing identity claim", http.StatusUnauthorized)
		return
	}
	display := strings.TrimSpace(claims.Name)
	if display == "" {
		display = strings.TrimSpace(claims.PreferredUsername)
	}

	uid, err := s.ensureUser(r.Context(), username, email, display)
	if err != nil {
		s.log.Error("oidc: ensure user", "err", err)
		s.clearStateCookie(w)
		http.Error(w, "ensure user", http.StatusInternalServerError)
		return
	}
	if err := s.issueSession(w, r, uid); err != nil {
		s.log.Error("oidc: issue session", "err", err)
		s.clearStateCookie(w)
		http.Error(w, "issue session", http.StatusInternalServerError)
		return
	}
	s.clearStateCookie(w)

	ret := st.Return
	if ret == "" {
		ret = s.postLoginURL
	}
	http.Redirect(w, r, ret, http.StatusFound)
}

func (s *Service) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/api/auth/oidc",
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// resolveIdentity picks the best deterministic key for this IdP
// principal. Verified email wins because it joins cleanly across
// auth methods (a user who signs in by password with the same
// email lands on the same UUIDv5). preferred_username is second
// best for IdPs without email scope. iss+sub is the floor —
// always present, but means the user-key is unique per IdP and
// can't be reached from a non-OIDC sign-in path.
func resolveIdentity(iss, sub, email string, emailVerified bool, preferred string) (username, returnedEmail string) {
	email = strings.ToLower(strings.TrimSpace(email))
	preferred = strings.ToLower(strings.TrimSpace(preferred))
	if email != "" && emailVerified {
		return email, email
	}
	if preferred != "" {
		// We still surface email if the IdP gave one, even
		// unverified — it's useful display info even when we
		// don't trust it as the identity key.
		return preferred, email
	}
	if email != "" {
		// Last resort: unverified email, but better than a
		// per-IdP synthetic key.
		return email, email
	}
	if sub == "" {
		return "", email
	}
	// Synthetic key. Lowercased so case-insensitive UUIDv5 stays
	// stable.
	return strings.ToLower(strings.TrimSpace(iss) + ":" + strings.TrimSpace(sub)), email
}

// randURL returns n random bytes encoded URL-safe base64. Stable
// length: 4 * ceil(n/3) − padding.
func randURL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallenge derives the S256 code_challenge from a verifier.
// RFC 7636 §4.2: BASE64URL(SHA256(verifier)).
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func encodeState(st flowState) (string, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeState(raw string) (flowState, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return flowState{}, err
	}
	var st flowState
	if err := json.Unmarshal(b, &st); err != nil {
		return flowState{}, err
	}
	return st, nil
}

// ParseProvidersFromEnv reads the operator's env soup and produces
// a slice of ProviderConfig. Format:
//
//	RIVOLT_OIDC_PROVIDERS=google,authentik
//	RIVOLT_OIDC_GOOGLE_ISSUER=https://accounts.google.com
//	RIVOLT_OIDC_GOOGLE_CLIENT_ID=...
//	RIVOLT_OIDC_GOOGLE_CLIENT_SECRET=...
//	RIVOLT_OIDC_GOOGLE_DISPLAY_NAME=Google         # optional
//	RIVOLT_OIDC_GOOGLE_SCOPES=openid,email,profile # optional
//
// baseURL is the public origin Rivolt is reachable at; we build
// the redirect URL automatically as
// {baseURL}/api/auth/oidc/{name}/callback.
//
// Missing or empty RIVOLT_OIDC_PROVIDERS yields an empty slice
// (and OIDC stays disabled). Invalid per-provider config returns
// an error so a deploy fails fast rather than silently dropping a
// provider.
func ParseProvidersFromEnv(getenv func(string) string, baseURL string) ([]ProviderConfig, error) {
	list := strings.TrimSpace(getenv("RIVOLT_OIDC_PROVIDERS"))
	if list == "" {
		return nil, nil
	}
	bu := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if bu == "" {
		return nil, errors.New("RIVOLT_OIDC_PROVIDERS set but RIVOLT_BASE_URL is empty")
	}
	if _, err := url.Parse(bu); err != nil {
		return nil, fmt.Errorf("RIVOLT_BASE_URL invalid: %w", err)
	}
	var out []ProviderConfig
	seen := make(map[string]struct{})
	for _, raw := range strings.Split(list, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("RIVOLT_OIDC_PROVIDERS lists %q twice", name)
		}
		seen[name] = struct{}{}
		prefix := "RIVOLT_OIDC_" + strings.ToUpper(name) + "_"
		issuer := strings.TrimSpace(getenv(prefix + "ISSUER"))
		clientID := strings.TrimSpace(getenv(prefix + "CLIENT_ID"))
		clientSecret := strings.TrimSpace(getenv(prefix + "CLIENT_SECRET"))
		display := strings.TrimSpace(getenv(prefix + "DISPLAY_NAME"))
		scopesRaw := strings.TrimSpace(getenv(prefix + "SCOPES"))
		if issuer == "" {
			return nil, fmt.Errorf("%s_ISSUER is empty", strings.ToUpper(name))
		}
		if clientID == "" {
			return nil, fmt.Errorf("%s_CLIENT_ID is empty", strings.ToUpper(name))
		}
		var scopes []string
		if scopesRaw != "" {
			for _, s := range strings.Split(scopesRaw, ",") {
				if t := strings.TrimSpace(s); t != "" {
					scopes = append(scopes, t)
				}
			}
		}
		out = append(out, ProviderConfig{
			Name:         name,
			DisplayName:  display,
			IssuerURL:    issuer,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  bu + "/api/auth/oidc/" + name + "/callback",
			Scopes:       scopes,
		})
	}
	return out, nil
}
