// Package auth owns Rivolt's session-level authentication: the
// /api/auth/* endpoints, the cookie format, and the middleware that
// resolves an authenticated user into request context.
//
// # Two issuers, one identity seam
//
// Middleware accepts two wire formats and collapses them onto the
// same user_id contract:
//
//  1. A trusted upstream proxy (oauth2-proxy, Authelia, Keycloak
//     gatekeeper, …) writes X-Forwarded-User / -Email. Rivolt
//     believes the header only if the request's RemoteAddr falls
//     inside a CIDR in Config.TrustedProxyCIDRs; an empty list (the
//     default, and what homelab / docker-compose deployments use)
//     disables header-based auth entirely.
//
//  2. The built-in POST /api/auth/login handler checks the static
//     operator credential (RIVOLT_USERNAME / RIVOLT_PASSWORD) and
//     issues an HMAC-signed session cookie. This path is the local
//     fallback / homelab default.
//
// Both routes map a username to the same deterministic UUIDv5 via
// Config.UserIDFor, so swapping issuers never re-keys data.
// Handlers only ever see UserFromContext(ctx); they don't know
// which issuer authenticated the request.
//
// Why this shape
//
//   - One seam to extend. Adding OIDC direct later is a third
//     Middleware branch, no change to any handler or store.
//   - Session state lives in the cookie itself (HMAC-signed), not
//     in a server-side table. Zero DB writes per request, stateless
//     replicas, nothing to clean up on logout beyond clearing the
//     cookie.
//   - Credential comparison uses subtle.ConstantTimeCompare so the
//     obvious timing side-channel on the static-creds path doesn't
//     leak which field was wrong.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Config is the operator-supplied auth configuration. Built by main
// from env and handed to New. Keeping it a plain struct — rather
// than pulling os.Getenv here — makes this package trivially
// testable.
type Config struct {
	// Username and Password are the single operator credential. If
	// either is empty the auth layer refuses to mint tokens and
	// every /api call behind Middleware gets a 401.
	Username string
	Password string

	// CookieSecret is the HMAC key used to sign sessions. Must be
	// at least 32 bytes of entropy. If empty, New generates a
	// process-local random key — fine for dev, wrong for prod
	// because every restart invalidates every session.
	CookieSecret []byte

	// CookieName is the session cookie name. Defaults to
	// "rivolt_session".
	CookieName string

	// CookieTTL is how long a session is valid. Defaults to 30
	// days, short enough that a leaked cookie expires on its own
	// but long enough that users don't re-auth weekly.
	CookieTTL time.Duration

	// SecureCookie marks the cookie Secure so it's only sent over
	// HTTPS. Defaults to true; flip to false only for local http
	// development.
	SecureCookie bool

	// TrustedProxyCIDRs is the list of subnets whose
	// X-Forwarded-User / X-Forwarded-Email / X-Forwarded-Preferred-
	// Username headers are believed. Empty (the default) disables
	// header-based auth entirely, which is what self-hosted /
	// docker-compose deployments want: the in-app cookie login is
	// the only issuer.
	//
	// In a k8s deployment behind oauth2-proxy, Authelia, or
	// Keycloak gatekeeper, set this to the pod-network CIDR (e.g.
	// "10.0.0.0/8,127.0.0.1/32") — requests not coming from an
	// allowed source are treated as if the header weren't there,
	// so a direct client curl with a forged header falls back to
	// the cookie path and fails open to "unauthenticated". The
	// ingress is still expected to strip inbound X-Forwarded-*
	// from the client as a belt-and-braces measure.
	TrustedProxyCIDRs []*net.IPNet

	// HeaderUser / HeaderEmail are the header names the
	// upstream proxy writes the authenticated identity into.
	// Defaults follow oauth2-proxy conventions. Most proxies can
	// be configured to emit these exact names.
	HeaderUser  string
	HeaderEmail string

	// UserIDFor maps a username to the stable tenant UUID. main
	// wires this to db.UserIDFor; the indirection keeps the auth
	// package free of any direct dependency on the db package.
	UserIDFor func(username string) uuid.UUID
}

// Service wraps the configured credential + cookie signer and
// produces the login/logout/me HTTP handlers plus the authenticating
// middleware.
type Service struct {
	cfg         Config
	ensureUser  func() (uuid.UUID, error)
	cookieName  string
	ttl         time.Duration
	secret      []byte
	secureCooke bool
	trustedNets []*net.IPNet
	hdrUser     string
	hdrEmail    string
	userIDFor   func(string) uuid.UUID
}

// New builds a Service. ensureUser is invoked once on successful
// login and must upsert the user row and return its UUID; main
// wires it up to db.EnsureUser so this package has no dependency
// on the db package itself.
func New(cfg Config, ensureUser func() (uuid.UUID, error)) (*Service, error) {
	if cfg.CookieName == "" {
		cfg.CookieName = "rivolt_session"
	}
	if cfg.CookieTTL == 0 {
		cfg.CookieTTL = 30 * 24 * time.Hour
	}
	if cfg.HeaderUser == "" {
		cfg.HeaderUser = "X-Forwarded-Preferred-Username"
	}
	if cfg.HeaderEmail == "" {
		cfg.HeaderEmail = "X-Forwarded-Email"
	}
	if cfg.UserIDFor == nil {
		return nil, fmt.Errorf("auth.New: UserIDFor is required")
	}
	secret := cfg.CookieSecret
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate cookie secret: %w", err)
		}
	} else if len(secret) < 32 {
		return nil, fmt.Errorf("RIVOLT_COOKIE_SECRET must be at least 32 bytes, got %d", len(secret))
	}
	return &Service{
		cfg:         cfg,
		ensureUser:  ensureUser,
		cookieName:  cfg.CookieName,
		ttl:         cfg.CookieTTL,
		secret:      secret,
		secureCooke: cfg.SecureCookie,
		trustedNets: cfg.TrustedProxyCIDRs,
		hdrUser:     cfg.HeaderUser,
		hdrEmail:    cfg.HeaderEmail,
		userIDFor:   cfg.UserIDFor,
	}, nil
}

// ParseTrustedCIDRs is a convenience for main: turn the comma-
// separated RIVOLT_TRUSTED_PROXY_CIDR env var into a slice of
// *net.IPNet suitable for Config.TrustedProxyCIDRs. Empty input
// returns a nil slice, which disables header-based auth.
func ParseTrustedCIDRs(list string) ([]*net.IPNet, error) {
	list = strings.TrimSpace(list)
	if list == "" {
		return nil, nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(list, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("bad CIDR %q: %w", p, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// token is the payload we sign into the cookie. Kept tiny — just
// the tenant uuid and an expiry. Real auth providers will hand us a
// JWT with claims instead, but the Middleware contract (resolve
// cookie/bearer → user_id → context) stays the same.
type token struct {
	UserID    uuid.UUID `json:"u"`
	ExpiresAt int64     `json:"e"` // unix seconds
}

// encode signs and base64-encodes a token. Format is
// `base64(json).base64(hmac)` — easy to inspect in the browser
// devtools, cheap to verify, and impossible to forge without the
// secret.
func (s *Service) encode(t token) (string, error) {
	body, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	bodyB64 := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(bodyB64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return bodyB64 + "." + sig, nil
}

// decode verifies the HMAC, parses the payload, and enforces the
// expiry. Returns an error (never a zero token) on any failure so
// callers can't accidentally treat an unverified token as valid.
func (s *Service) decode(raw string) (token, error) {
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 {
		return token{}, errors.New("malformed token")
	}
	bodyB64, sigB64 := parts[0], parts[1]
	want := hmac.New(sha256.New, s.secret)
	want.Write([]byte(bodyB64))
	wantSig := base64.RawURLEncoding.EncodeToString(want.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sigB64), []byte(wantSig)) != 1 {
		return token{}, errors.New("bad signature")
	}
	body, err := base64.RawURLEncoding.DecodeString(bodyB64)
	if err != nil {
		return token{}, fmt.Errorf("decode body: %w", err)
	}
	var t token
	if err := json.Unmarshal(body, &t); err != nil {
		return token{}, fmt.Errorf("unmarshal: %w", err)
	}
	if time.Now().Unix() > t.ExpiresAt {
		return token{}, errors.New("token expired")
	}
	if t.UserID == uuid.Nil {
		return token{}, errors.New("token missing user id")
	}
	return t, nil
}

// ctxKey is unexported so only this package can stuff values into
// request context under this key. Prevents unrelated middleware
// from spoofing the "authenticated user" by setting the wrong key.
type ctxKey struct{}

// UserFromContext returns the authenticated user_id, if any, that
// Middleware stored on the request context. Handlers that require
// auth should check the ok flag and 401 when it's false.
func UserFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(ctxKey{}).(uuid.UUID)
	return v, ok && v != uuid.Nil
}

// withUser returns a copy of ctx with uid stored under this
// package's private key. Exported for tests that want to build an
// authenticated context without going through the HTTP surface.
func withUser(ctx context.Context, uid uuid.UUID) context.Context {
	return context.WithValue(ctx, ctxKey{}, uid)
}

// Middleware resolves the active user from (in order) a trusted
// proxy header or a signed session cookie, and injects the user_id
// into request context. Requests with neither fall through with no
// user set — the individual handler (or RequireUser) decides what
// to do, so /api/auth/login itself stays reachable to the
// unauthenticated.
//
// Resolution order is fixed: header wins if and only if the
// request's RemoteAddr is inside a TrustedProxyCIDR *and* the
// header is present. This is deliberate:
//
//   - Homelab install has an empty CIDR list → header check is a
//     no-op, cookie-only, zero footgun.
//   - K8s install behind oauth2-proxy sets the pod-network CIDR →
//     header wins, and a forged header from outside the allowed
//     network falls back to the cookie path (which fails open to
//     "unauthenticated" for a client without a session).
//
// A header-authenticated request that also carries a cookie
// honours the header. A mismatched cookie in that case is harmless
// — we just don't read it.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil {
			next.ServeHTTP(w, r)
			return
		}
		if uid, ok := s.identityFromHeader(r); ok {
			next.ServeHTTP(w, r.WithContext(withUser(r.Context(), uid)))
			return
		}
		c, err := r.Cookie(s.cookieName)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		t, err := s.decode(c.Value)
		if err != nil {
			// Nudge the browser to drop the bad cookie so it
			// stops resending it; a new login will replace it.
			s.clearCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), t.UserID)))
	})
}

// identityFromHeader returns the user id claimed by a trusted
// upstream proxy, or (zero, false) if the request isn't on the
// trusted interface or no identity header is present.
//
// We prefer HeaderUser (username / preferred-username) because it
// matches the cookie path's identity key — the same login
// resolves to the same UUIDv5 whether it arrived by cookie or by
// header. HeaderEmail is used as a fallback for proxies that only
// emit email.
func (s *Service) identityFromHeader(r *http.Request) (uuid.UUID, bool) {
	if len(s.trustedNets) == 0 {
		return uuid.Nil, false
	}
	if !s.remoteAddrTrusted(r.RemoteAddr) {
		return uuid.Nil, false
	}
	user := strings.TrimSpace(r.Header.Get(s.hdrUser))
	if user == "" {
		user = strings.TrimSpace(r.Header.Get(s.hdrEmail))
	}
	if user == "" {
		return uuid.Nil, false
	}
	return s.userIDFor(user), true
}

// remoteAddrTrusted reports whether addr (as produced by
// net/http, which is "ip:port" — or wrapped by chi's RealIP
// middleware into the same shape) falls inside any configured
// TrustedProxyCIDR.
func (s *Service) remoteAddrTrusted(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Some test harnesses leave RemoteAddr as a bare IP.
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range s.trustedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// RequireUser wraps a handler so only authenticated requests reach
// it; everything else gets a 401.
func (s *Service) RequireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFromContext(r.Context()); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// Configured reports whether static credentials are wired. If
// false, login will always fail — the server is effectively a
// read-nothing box until the operator sets env vars.
func (s *Service) Configured() bool {
	return s != nil && s.cfg.Username != "" && s.cfg.Password != ""
}

// Login is the POST /api/auth/login handler. Expects
// `{ "username": "...", "password": "..." }` JSON. On success, sets
// the session cookie and returns `{ "username": "..." }`.
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// subtle.ConstantTimeCompare guards against the trivial timing
	// side-channel on string compare. We evaluate both sides every
	// time (no short-circuit on username mismatch) so an attacker
	// can't tell whether the username or the password was wrong.
	userOK := subtle.ConstantTimeCompare(
		[]byte(strings.ToLower(strings.TrimSpace(body.Username))),
		[]byte(strings.ToLower(strings.TrimSpace(s.cfg.Username))),
	)
	passOK := subtle.ConstantTimeCompare(
		[]byte(body.Password),
		[]byte(s.cfg.Password),
	)
	if userOK&passOK != 1 {
		// Same 401 wording regardless of which field was wrong.
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	uid, err := s.ensureUser()
	if err != nil {
		http.Error(w, "ensure user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tok, err := s.encode(token{
		UserID:    uid,
		ExpiresAt: time.Now().Add(s.ttl).Unix(),
	})
	if err != nil {
		http.Error(w, "sign token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCooke,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(s.ttl),
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"username": s.cfg.Username})
}

// Logout clears the session cookie. No-op when there isn't one.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// Me returns the active user for a session. Handy both for
// frontend bootstrap (decide whether to render the login page or
// the app) and for debugging.
func (s *Service) Me(w http.ResponseWriter, r *http.Request) {
	uid, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user_id":  uid.String(),
		"username": s.cfg.Username,
	})
}

func (s *Service) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCooke,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
