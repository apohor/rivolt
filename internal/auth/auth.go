// Package auth owns Rivolt's session-level authentication: the
// /api/auth/* endpoints, the cookie format, and the middleware that
// resolves an authenticated user into request context.
//
// # Issuers, one identity seam
//
// Middleware accepts two wire formats and collapses them onto the
// same user_id contract:
//
//  1. An OIDC sign-in (mounted under /api/auth/oidc/*, see
//     internal/oidc) terminates by calling Service.IssueSession,
//     which mints the same opaque session cookie used by every
//     other path.
//
//  2. A debug bypass: when Config.BypassUserID is set, Middleware
//     injects that user when no other issuer resolves. Intended
//     for local-dev only — main logs a loud warning at boot.
//
// Both routes hand off to the same Middleware identity
// contract (UserFromContext(ctx) returns the user UUID); handlers
// don't know which issuer authenticated the request.
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
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/apohor/rivolt/internal/logging"
)

// Config is the operator-supplied auth configuration. Built by main
// from env and handed to New. Keeping it a plain struct — rather
// than pulling os.Getenv here — makes this package trivially
// testable.
type Config struct {
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

	// OnAuth, when set, is told the method of every authenticated request
	// (cookie, kratos, bypass): a metrics hook. No user data.
	OnAuth func(method string)

	// KratosCookieName is the name of the Ory Kratos session cookie
	// (default "ory_kratos_session"). When KratosResolver is set, a
	// request bearing this cookie is checked against Kratos's
	// /sessions/whoami; on success the resolved Rivolt UUID is
	// installed in the request context, identical to the cookie
	// path. Empty disables the issuer.
	KratosCookieName string

	// KratosHost, when set, restricts the Kratos session issuer to
	// requests whose Host header names it (hostname, case-insensitive,
	// port ignored). It must be the host where Kratos cookies are
	// legitimately issued (RIVOLT_KRATOS_COOKIE_HOST, e.g. rivolt.dev),
	// the SAME value on every install that shares that Kratos, NOT the
	// install's own host: a Kratos cookie scoped to a parent domain is
	// also sent to sibling hosts (preview, demo), and keying on their own
	// host would let them accept it. Empty keeps the old behaviour.
	KratosHost string

	// KratosResolver, when non-nil, enables the Kratos session
	// issuer. The function receives the cookie value (verbatim)
	// and returns the Rivolt user UUID, an "ok" flag (false →
	// no active session), and an error for transient failures.
	// Wired by main to a closure that calls kratos.Whoami and
	// runs the resulting identity through UserIDFor to produce
	// the same UUID the cookie issuer would.
	//
	// The resolver is called with the request context so it
	// inherits the request's deadline.
	KratosResolver func(ctx context.Context, cookieHeader string) (uuid.UUID, bool, error)

	// UserIDFor maps a username to the stable tenant UUID. main
	// wires this to db.UserIDFor; the indirection keeps the auth
	// package free of any direct dependency on the db package.
	UserIDFor func(username string) uuid.UUID

	// UsernameFor resolves a user UUID back to a display username
	// for /api/auth/me. Optional — when nil, Me returns an empty
	// username, which the SPA tolerates. Wired by main to a DB
	// lookup so OIDC sessions surface the IdP's display name.
	UsernameFor func(ctx context.Context, uid uuid.UUID) (string, error)

	// RoleFor resolves a user UUID to its role string
	// ("user" / "admin"). Optional — when nil, Me omits the
	// `role` field, which the SPA treats as "user". main wires
	// this to db.RoleFor so /api/admin/* visibility flips on
	// without an extra round-trip.
	RoleFor func(ctx context.Context, uid uuid.UUID) (string, error)

	// DisabledFor reports whether a user has been admin-disabled.
	// Optional — when nil the gate is open. Wired by main to
	// db.IsDisabled so a flipped flag invalidates every in-flight
	// session on the next request without needing to revoke each
	// session row by hand. A non-nil error is treated as "fail
	// closed": better to bounce the user to /login on a transient
	// DB blip than to honor a session for someone who may have
	// just been disabled.
	DisabledFor func(ctx context.Context, uid uuid.UUID) (bool, error)

	// BypassUserID, when non-zero, makes Middleware inject this
	// user on every request that doesn't already have an identity
	// resolved by cookie. Debug / local-dev only —
	// effectively disables auth. main logs a loud warning at boot
	// and refuses to set this when SecureCookie is true.
	BypassUserID uuid.UUID
}

// Service wraps the cookie signer and produces the logout/me HTTP
// handlers plus the authenticating middleware. Sign-in itself lives
// in sibling packages (internal/oidc) which call IssueSession.
type Service struct {
	cfg          Config
	cookieName   string
	ttl          time.Duration
	secret       []byte
	secureCooke  bool
	onAuth       func(method string)
	userIDFor    func(string) uuid.UUID
	usernameFor  func(ctx context.Context, uid uuid.UUID) (string, error)
	roleFor      func(ctx context.Context, uid uuid.UUID) (string, error)
	disabledFor  func(ctx context.Context, uid uuid.UUID) (bool, error)
	bypassUserID uuid.UUID

	kratosCookieName string
	kratosHost       string
	kratosResolver   func(ctx context.Context, cookieHeader string) (uuid.UUID, bool, error)

	// sessionStore, when non-nil, is the source of truth for
	// cookie identity: Login creates a row, Middleware looks up
	// by hashed token, Logout revokes. When nil (tests, legacy
	// cookie-only binaries), the HMAC-signed-cookie path below
	// still works — so stepping back to the old behaviour is a
	// matter of wiring, not code churn.
	sessionStore SessionStore

	// acceptBearer turns on `Authorization: Bearer` (the iPhone app's
	// session token). Off on the admin deployment.
	acceptBearer bool
}

// SessionStore is the slice of sessions.Store that auth actually
// uses, exposed as an interface so tests can stub without a live
// DB. The concrete implementation lives in internal/sessions.
type SessionStore interface {
	Create(ctx context.Context, userID uuid.UUID, ttl time.Duration, opts SessionCreateOpts) (SessionInfo, string, error)
	// Lookup resolves a browser session cookie (kind web only).
	Lookup(ctx context.Context, rawTokenCookie string) (SessionInfo, error)
	// LookupBearer resolves an iPhone app token (kind app only).
	LookupBearer(ctx context.Context, rawToken string) (SessionInfo, error)
	RevokeByToken(ctx context.Context, rawTokenCookie string) error
}

// SessionCreateOpts / SessionInfo mirror the sessions package's
// types so callers don't need to import both. The sessions
// package's Store satisfies auth.SessionStore via a thin
// adapter wired in main.go.
type SessionCreateOpts struct {
	UserAgent   string
	IPAddress   string
	DeviceLabel string
}

// SessionInfo is the projection auth needs — just the identity
// fields. Extra metadata (last_seen_at, device list) is out of
// scope for the auth seam.
type SessionInfo struct {
	ID     uuid.UUID
	UserID uuid.UUID
}

// SessionRef names the sessions row behind a request: set for the opaque
// cookie (Kind "web") and the iPhone app's bearer token (Kind "app"); absent
// for Kratos, bypass and legacy signed-cookie auth, which have no row.
// The sessions list uses it to mark "current" and to keep the caller's own
// session on "sign out other devices".
type SessionRef struct {
	ID   uuid.UUID
	Kind string // "web" | "app" (sessions.KindWeb / KindApp)
}

type sessionCtxKey struct{}

// WithSession records the request's session row.
func WithSession(ctx context.Context, ref SessionRef) context.Context {
	return context.WithValue(ctx, sessionCtxKey{}, ref)
}

// SessionFromContext returns the request's session row, if it has one.
func SessionFromContext(ctx context.Context) (SessionRef, bool) {
	ref, ok := ctx.Value(sessionCtxKey{}).(SessionRef)
	return ref, ok && ref.ID != uuid.Nil
}

// AcceptBearer turns the iPhone app's bearer tokens on or off (off by
// default; main turns it on except on the admin deployment). Chainable.
func (s *Service) AcceptBearer(on bool) *Service {
	s.acceptBearer = on
	return s
}

// ViaBearer reports whether the request's user came from an iPhone app
// bearer token. The admin gate, impersonation and the app sign-in pages
// refuse such a caller.
func ViaBearer(ctx context.Context) bool {
	return logging.AuthMethodFromContext(ctx) == logging.AuthBearer
}

// bearerToken returns the token of an `Authorization: Bearer` header, and
// whether the header names the Bearer scheme at all.
func bearerToken(r *http.Request) (string, bool) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// unauthorizedBearer is the answer to a bearer token that isn't a live app
// session (or isn't accepted here): 401, so the app knows to sign in again.
func unauthorizedBearer(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// WithSessionStore installs the opaque-session backend. Chainable
// from New for compact wiring. When unset, auth falls back to
// the HMAC-signed cookie issued inline.
func (s *Service) WithSessionStore(store SessionStore) *Service {
	s.sessionStore = store
	return s
}

// New builds a Service from the supplied Config.
func New(cfg Config) (*Service, error) {
	if cfg.CookieName == "" {
		cfg.CookieName = "rivolt_session"
	}
	if cfg.CookieTTL == 0 {
		cfg.CookieTTL = 30 * 24 * time.Hour
	}
	if cfg.KratosCookieName == "" {
		cfg.KratosCookieName = "ory_kratos_session"
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
		cfg:          cfg,
		cookieName:   cfg.CookieName,
		ttl:          cfg.CookieTTL,
		secret:       secret,
		secureCooke:  cfg.SecureCookie,
		onAuth:       cfg.OnAuth,
		userIDFor:    cfg.UserIDFor,
		usernameFor:  cfg.UsernameFor,
		roleFor:      cfg.RoleFor,
		disabledFor:  cfg.DisabledFor,
		bypassUserID: cfg.BypassUserID,

		kratosCookieName: cfg.KratosCookieName,
		kratosHost:       normalizeHost(cfg.KratosHost),
		kratosResolver:   cfg.KratosResolver,
	}, nil
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

// impersonationCtxKey marks a request context as impersonated and
// carries the real (admin) caller's id. Unexported for the same
// reason as ctxKey — only this package's helpers can set it.
type impersonationCtxKey struct{}

// UserFromContext returns the authenticated user_id, if any, that
// Middleware stored on the request context. Handlers that require
// auth should check the ok flag and 401 when it's false.
func UserFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(ctxKey{}).(uuid.UUID)
	return v, ok && v != uuid.Nil
}

// WithUser returns a copy of ctx with uid stored under this
// package's private key. Exported for tests (including tests in
// sibling packages) that want to build an authenticated context
// without replaying the full cookie / header handshake. The
// context key itself remains unexported, so only this package's
// code paths can inject an identity.
func WithUser(ctx context.Context, uid uuid.UUID) context.Context {
	// Also stamp the logging context so every slog line emitted
	// downstream carries `user_id` automatically. Cheap; the helper
	// no-ops on uuid.Nil.
	ctx = logging.WithUserID(ctx, uid)
	// Stamp the active OTel span with user.id so Tempo can filter
	// traces by user (TraceQL: `{ .user.id = "<uid>" }`). No-op when
	// no span is recording (e.g. health checks excluded from
	// sampling) or uuid.Nil.
	if uid != uuid.Nil {
		if span := trace.SpanFromContext(ctx); span.IsRecording() {
			span.SetAttributes(attribute.String("user.id", uid.String()))
		}
	}
	return context.WithValue(ctx, ctxKey{}, uid)
}

// WithImpersonation swaps the request's active identity from
// impersonatorID (the real, already-authenticated admin) to
// targetID, so every downstream call that reads UserFromContext —
// stores, RLS's app.user_id, handlers — transparently renders the
// target's data. Building this on top of WithUser means the swap
// gets the same logging.WithUserID + span-attribute stamping for
// free; it additionally stamps both ids via logging.WithImpersonation
// (see that doc for why user_id alone isn't enough for an audit
// trail) and marks the context as impersonated for
// IsImpersonating/ImpersonatorFromContext callers (the /api/admin/*
// no-privilege-chaining guard in particular).
//
// Callers (internal/api's impersonation middleware) are responsible
// for every authorization decision — admin-only, read-only,
// target-not-admin, feature flag — before calling this. It performs
// no checks of its own; it's pure context plumbing.
func WithImpersonation(ctx context.Context, impersonatorID, targetID uuid.UUID) context.Context {
	ctx = logging.WithImpersonation(ctx, impersonatorID, targetID)
	ctx = context.WithValue(ctx, impersonationCtxKey{}, impersonatorID)
	return WithUser(ctx, targetID)
}

// ImpersonatorFromContext returns the real admin identity behind an
// impersonated request, and whether the request is impersonated at
// all. UserFromContext on the same context returns the *target's*
// id — this is the complementary "who's actually driving" lookup.
func ImpersonatorFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(impersonationCtxKey{}).(uuid.UUID)
	return v, ok && v != uuid.Nil
}

// IsImpersonating reports whether the request context was produced
// by WithImpersonation. Used as a defense-in-depth check (on top of
// the impersonation middleware's own path-based block) anywhere
// privilege escalation via impersonation must be refused outright,
// e.g. requireAdminMW.
func IsImpersonating(ctx context.Context) bool {
	_, ok := ImpersonatorFromContext(ctx)
	return ok
}

// HasCredentials reports whether the request presented a session
// credential (the Rivolt session cookie or the Kratos session cookie),
// whether or not it resolved to a user. A request with none is simply
// signed out; one with a credential that did not resolve carries an
// expired or invalid session.
//
// It looks at the two session cookies only.
func (s *Service) HasCredentials(r *http.Request) bool {
	if s == nil {
		return false
	}
	if s.cookieName != "" {
		if _, err := r.Cookie(s.cookieName); err == nil {
			return true
		}
	}
	// The Kratos cookie is a credential only where a resolver can turn it into a user; with the resolver off it is ignored, like any
	// other cookie the app does not use.
	if s.kratosResolver != nil && s.kratosCookieName != "" && s.kratosHostOK(r) {
		if _, err := r.Cookie(s.kratosCookieName); err == nil {
			return true
		}
	}
	return false
}

// kratosHostOK reports whether the Kratos session issuer may be used for
// r: always when no KratosHost is configured, else only when the request
// Host names it.
func (s *Service) kratosHostOK(r *http.Request) bool {
	return s.kratosHost == "" || normalizeHost(r.Host) == s.kratosHost
}

// normalizeHost lower-cases a host or host:port and drops the port and
// any trailing dot. A full URL (scheme://host/...) is reduced to its host.
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
		if j := strings.IndexAny(h, "/?#"); j >= 0 {
			h = h[:j]
		}
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
}

// ErrSignupClosed is what a Kratos resolver returns for a valid
// session whose identity has no Rivolt account while sign-ups are
// disabled. The middleware leaves the request unauthenticated and
// sets AuthErrorHeader.
var ErrSignupClosed = errors.New("auth: sign-ups are closed for this identity")

// AuthErrorHeader carries a stable machine-readable reason on the
// 401 for an IdP-authenticated but not-provisioned identity.
const AuthErrorHeader = "X-Rivolt-Auth-Error"

// Middleware resolves the active user from (in order) an app bearer
// token, a Kratos session, a signed session cookie, or the debug bypass,
// and injects the user_id into request context. Requests with none of
// those fall through with no user set: the individual handler
// (or RequireUser) decides what to do, so /api/auth/oidc/* (the
// only sign-in surface in OIDC-only mode) stays reachable to the
// unauthenticated.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil {
			next.ServeHTTP(w, r)
			return
		}
		// iPhone app token. A request carrying one is decided here and
		// nowhere else: never a cookie, Kratos or bypass fallback,
		// so a bad or revoked token is a 401 rather than some other identity.
		if tok, ok := bearerToken(r); ok {
			if !s.acceptBearer || s.sessionStore == nil || tok == "" {
				unauthorizedBearer(w)
				return
			}
			info, err := s.sessionStore.LookupBearer(r.Context(), tok)
			if err != nil {
				unauthorizedBearer(w)
				return
			}
			r = r.WithContext(WithSession(r.Context(), SessionRef{ID: info.ID, Kind: "app"}))
			s.serveAsUser(next, w, r, info.UserID, false, logging.AuthBearer)
			return
		}
		// Kratos session issuer. Cheap pre-check: only call out
		// to Kratos when the inbound request actually carries
		// the Kratos session cookie. This keeps the per-request
		// cost at zero for the common case (Rivolt's own cookie),
		// and bounded to a single in-cluster GET when the user
		// signed in via Hydra/Kratos.
		if s.kratosResolver != nil && s.kratosHostOK(r) {
			if _, err := r.Cookie(s.kratosCookieName); err == nil {
				uid, ok, err := s.kratosResolver(r.Context(), r.Header.Get("Cookie"))
				if err == nil && ok {
					s.serveAsUser(next, w, r, uid, false, logging.AuthKratos)
					return
				}
				if errors.Is(err, ErrSignupClosed) {
					// A valid IdP session for an identity that has no
					// account on a closed server. Stay unauthenticated
					// but tell the SPA why, so it shows the message
					// instead of a bare sign-in page.
					w.Header().Set(AuthErrorHeader, "signups_disabled")
				}
				// Resolver said "no session" or hit a transient
				// error: fall through to the cookie path so a
				// stale Kratos cookie doesn't lock out users
				// who also have a valid Rivolt cookie.
			}
		}
		c, err := r.Cookie(s.cookieName)
		if err != nil {
			s.maybeBypass(next, w, r)
			return
		}
		// Opaque-session path: cookie is a random 32-byte
		// token; the DB is the source of truth. Any failure
		// (bad format, revoked, expired) is collapsed into
		// "no user" plus a cookie-clear so the browser stops
		// resending the stale value.
		if s.sessionStore != nil {
			info, err := s.sessionStore.Lookup(r.Context(), c.Value)
			if err != nil {
				s.clearCookie(w)
				s.maybeBypass(next, w, r)
				return
			}
			r = r.WithContext(WithSession(r.Context(), SessionRef{ID: info.ID, Kind: "web"}))
			s.serveAsUser(next, w, r, info.UserID, true, logging.AuthCookie)
			return
		}
		// Legacy HMAC-signed-cookie path. Kept so tests and
		// single-binary builds without a DB continue to work.
		t, err := s.decode(c.Value)
		if err != nil {
			// Nudge the browser to drop the bad cookie so it
			// stops resending it; a new login will replace it.
			s.clearCookie(w)
			s.maybeBypass(next, w, r)
			return
		}
		s.serveAsUser(next, w, r, t.UserID, true, logging.AuthCookie)
	})
}

// serveAsUser is the single chokepoint that turns a resolved
// uid into a request-scoped identity. It enforces the disabled
// flag — if the user has been admin-disabled, drop the cookie
// and continue unauthenticated (or as the bypass user). clearCookie
// is gated on cookieAuth because other issuers (bearer, Kratos)
// have no cookie of ours to clear.
func (s *Service) serveAsUser(next http.Handler, w http.ResponseWriter, r *http.Request, uid uuid.UUID, cookieAuth bool, method string) {
	if s.disabledFor != nil && uid != uuid.Nil {
		disabled, err := s.disabledFor(r.Context(), uid)
		// Fail closed on lookup errors so a transient DB blip
		// can't honour a session for a freshly-disabled user.
		if err != nil || disabled {
			if method == logging.AuthBearer {
				unauthorizedBearer(w)
				return
			}
			if cookieAuth {
				s.clearCookie(w)
			}
			s.maybeBypass(next, w, r)
			return
		}
	}
	s.observe(method)
	next.ServeHTTP(w, r.WithContext(logging.WithAuthMethod(WithUser(r.Context(), uid), method)))
}

// observe counts an authenticated request by method (see Config.OnAuth).
func (s *Service) observe(method string) {
	if s.onAuth != nil {
		s.onAuth(method)
	}
}

// maybeBypass injects the configured bypass user into the request
// context when no real issuer resolved one. When BypassUserID is
// the zero UUID this is a no-op and the request continues
// unauthenticated.
func (s *Service) maybeBypass(next http.Handler, w http.ResponseWriter, r *http.Request) {
	if s.bypassUserID != uuid.Nil {
		s.observe(logging.AuthBypass)
		next.ServeHTTP(w, r.WithContext(logging.WithAuthMethod(WithUser(r.Context(), s.bypassUserID), logging.AuthBypass)))
		return
	}
	next.ServeHTTP(w, r)
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

// Configured reports whether the auth Service is wired. Used by
// the API router to decide whether to mount the auth middleware.
// Enforcement (whether unauthenticated requests get 401'd) is
// computed by main from the set of configured issuers.
func (s *Service) Configured() bool {
	return s != nil
}

// IssueSession plants the session cookie for an already-resolved
// user_id. Used by the OIDC callback (the only sign-in surface in
// OIDC-only mode); kept as its own method so future issuers
// (magic-link, dev-shortcut) can reuse the exact same cookie path
// without duplicating the opaque-session/HMAC-fallback fork.
//
// Wire shape:
//
//   - With a sessionStore: a row is created (random token + HMACed
//     row key) and the cookie carries only the raw token.
//   - Without one: a self-describing HMAC-signed token is signed
//     into the cookie. This branch keeps the legacy cookie-only
//     binaries (and the auth tests that don't spin up a sessions
//     store) working.
//
// Note that we deliberately do not write the response body here —
// that's the caller's responsibility because the OIDC callback
// wants to send a 302 redirect.
func (s *Service) IssueSession(w http.ResponseWriter, r *http.Request, uid uuid.UUID) error {
	if s == nil {
		return errors.New("auth: nil service")
	}
	if uid == uuid.Nil {
		return errors.New("auth: zero user id")
	}
	if s.sessionStore != nil {
		_, raw, err := s.sessionStore.Create(r.Context(), uid, s.ttl, SessionCreateOpts{
			UserAgent: r.UserAgent(),
			IPAddress: firstNonEmpty(r.RemoteAddr),
		})
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     s.cookieName,
			Value:    raw,
			Path:     "/",
			HttpOnly: true,
			Secure:   s.secureCooke,
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Now().Add(s.ttl),
		})
		return nil
	}
	tok, err := s.encode(token{
		UserID:    uid,
		ExpiresAt: time.Now().Add(s.ttl).Unix(),
	})
	if err != nil {
		return fmt.Errorf("sign token: %w", err)
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
	return nil
}

// firstNonEmpty returns the first non-empty string in vs.
// Sentinel helper kept local so the auth package doesn't grow
// a util.go for one function.
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// Logout clears the session cookie. No-op when there isn't one.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	// Opaque-session path: revoke the row so the cookie is
	// permanently dead, even if some stale tab replays it.
	// Best-effort — a DB error doesn't block cookie-clear.
	if s.sessionStore != nil {
		// The iPhone app signs out with its bearer token: revoke that one.
		if tok, ok := bearerToken(r); ok && ViaBearer(r.Context()) {
			_ = s.sessionStore.RevokeByToken(r.Context(), tok)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if c, err := r.Cookie(s.cookieName); err == nil {
			_ = s.sessionStore.RevokeByToken(r.Context(), c.Value)
		}
	}
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
	var username string
	if s.usernameFor != nil {
		if name, err := s.usernameFor(r.Context(), uid); err == nil {
			username = name
		}
	}
	var role string
	if s.roleFor != nil {
		if r2, err := s.roleFor(r.Context(), uid); err == nil {
			role = r2
		}
	}
	if role == "" {
		role = "user"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user_id":  uid.String(),
		"username": username,
		"role":     role,
	})
}

// ClearKratosCookie expires the Kratos session cookie (host-only, like Kratos sets it) when the request carries one. The Kratos
// session behind it is ended separately; this stops the browser sending a dead cookie, and is what a person expects of "Sign out".
func (s *Service) ClearKratosCookie(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.kratosCookieName == "" {
		return
	}
	if _, err := r.Cookie(s.kratosCookieName); err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.kratosCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCooke,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
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
