// Package cfaccess verifies the Cloudflare Access JWT that fronts the admin
// deployment (RIVOLT_ADMIN_ONLY).
//
// Access puts a signed JWT in the Cf-Access-Jwt-Assertion header of every
// request it lets through. The plain Cf-Access-Authenticated-User-Email header
// is NOT used anywhere: it is an ordinary header, so anything that can reach
// the origin can set it (the same class of hole as header SSO). Only a JWT that
// verifies against the team's published keys counts: signature, issuer,
// audience (the Access app's AUD tag), expiry, and an email on the allowlist.
// Every failure is a 403 before any route runs.
package cfaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Header is where Access puts the JWT.
const Header = "Cf-Access-Jwt-Assertion"

const (
	leeway        = 30 * time.Second
	maxJWKSBody   = 1 << 20
	keysTTL       = time.Hour
	refreshMinGap = time.Minute // unknown-kid refreshes are rate limited
)

// Config is the Access application's identity and who may pass.
type Config struct {
	// TeamDomain is the Access team domain, e.g. "myteam.cloudflareaccess.com".
	// A bare team name ("myteam") is accepted too.
	TeamDomain string
	// Aud is the application's AUD tag.
	Aud string
	// Emails is the allowlist, compared case-insensitively.
	Emails []string
	// CertsURL overrides where the signing keys are fetched from. Production
	// leaves it empty (https://<team>/cdn-cgi/access/certs); tests point it
	// at a local server.
	CertsURL string
}

// ErrKeysUnavailable means no valid signing key could be had (the keys
// endpoint is down or served nothing usable and nothing is cached). The
// request is denied, with a 503 so an outage reads as one.
var ErrKeysUnavailable = errors.New("signing keys unavailable")

// Verifier checks Access JWTs against the team's JWKS.
type Verifier struct {
	issuer  string
	certs   string
	aud     string
	emails  map[string]struct{}
	client  *http.Client
	now     func() time.Time
	log     *slog.Logger
	mu      sync.Mutex
	keys    jose.JSONWebKeySet
	fetched time.Time // last fetch attempt that succeeded
	tried   time.Time // last fetch attempt of any outcome
}

// NormalizeTeamDomain returns the host form of a team domain, or "" when it is
// not a plausible host name.
func NormalizeTeamDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimSuffix(s, "/")
	if s == "" || strings.ContainsAny(s, "/:@?# ") {
		return ""
	}
	if !strings.Contains(s, ".") {
		s += ".cloudflareaccess.com"
	}
	return s
}

// ParseEmails splits a comma-separated allowlist into lower-case addresses.
func ParseEmails(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// New builds a Verifier. It fails on an empty or malformed config, so a
// half-configured admin deployment cannot start.
func New(cfg Config, log *slog.Logger) (*Verifier, error) {
	host := NormalizeTeamDomain(cfg.TeamDomain)
	if host == "" {
		return nil, errors.New("cfaccess: team domain is empty or malformed")
	}
	if strings.TrimSpace(cfg.Aud) == "" {
		return nil, errors.New("cfaccess: AUD is empty")
	}
	if len(cfg.Emails) == 0 {
		return nil, errors.New("cfaccess: email allowlist is empty")
	}
	v := &Verifier{
		issuer: "https://" + host,
		certs:  "https://" + host + "/cdn-cgi/access/certs",
		aud:    strings.TrimSpace(cfg.Aud),
		emails: map[string]struct{}{},
		client: &http.Client{Timeout: 5 * time.Second},
		now:    time.Now,
		log:    log,
	}
	if cfg.CertsURL != "" {
		v.certs = cfg.CertsURL
	}
	for _, e := range cfg.Emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			v.emails[e] = struct{}{}
		}
	}
	return v, nil
}

// Verify returns the verified, lower-cased email of an Access JWT.
func (v *Verifier) Verify(ctx context.Context, raw string) (string, error) {
	if v == nil {
		return "", errors.New("no verifier")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("no token")
	}
	// Access signs with RS256. Pinning the algorithm closes alg=none and
	// HS256-with-the-public-key confusion.
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return "", fmt.Errorf("malformed token: %w", err)
	}
	if len(tok.Headers) != 1 || tok.Headers[0].KeyID == "" {
		return "", errors.New("token has no single key id")
	}
	key, err := v.key(ctx, tok.Headers[0].KeyID)
	if err != nil {
		return "", err
	}
	var std jwt.Claims
	var extra struct {
		Email string `json:"email"`
	}
	if err := tok.Claims(key, &std, &extra); err != nil {
		return "", fmt.Errorf("bad signature: %w", err)
	}
	if std.Expiry == nil {
		return "", errors.New("token has no expiry")
	}
	if err := std.ValidateWithLeeway(jwt.Expected{
		Issuer:      v.issuer,
		AnyAudience: jwt.Audience{v.aud},
		Time:        v.now(),
	}, leeway); err != nil {
		return "", fmt.Errorf("invalid claims: %w", err)
	}
	email := strings.ToLower(strings.TrimSpace(extra.Email))
	if _, ok := v.emails[email]; !ok || email == "" {
		return "", errors.New("email not allowed")
	}
	return email, nil
}

// key returns the signing key for kid, fetching the JWKS when the cache is
// cold, stale, or does not know the kid (key rotation). Fetches are spaced at
// least refreshMinGap apart, so a flood of forged kids cannot hammer Cloudflare.
func (v *Verifier) key(ctx context.Context, kid string) (jose.JSONWebKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	find := func() (jose.JSONWebKey, bool) {
		ks := v.keys.Key(kid)
		if len(ks) == 0 {
			return jose.JSONWebKey{}, false
		}
		return ks[0], true
	}
	if k, ok := find(); ok && v.now().Sub(v.fetched) < keysTTL {
		return k, nil
	}
	if v.now().Sub(v.tried) >= refreshMinGap || v.tried.IsZero() {
		v.tried = v.now()
		if err := v.fetch(ctx); err != nil {
			if v.log != nil {
				v.log.Warn("cloudflare access: fetching signing keys failed", "err", err.Error())
			}
			// A failed refresh must not lock out a key we already hold.
			if k, ok := find(); ok {
				return k, nil
			}
			return jose.JSONWebKey{}, ErrKeysUnavailable
		}
	}
	if k, ok := find(); ok {
		return k, nil
	}
	return jose.JSONWebKey{}, errors.New("unknown signing key")
}

func (v *Verifier) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certs, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBody)).Decode(&set); err != nil {
		return err
	}
	if len(set.Keys) == 0 {
		return errors.New("empty key set")
	}
	v.keys = set
	v.fetched = v.now()
	return nil
}

type ctxKey struct{}

// WithEmail returns ctx carrying a verified Access email. Middleware sets it;
// it is exported for tests of code that records it.
func WithEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, ctxKey{}, email)
}

// Email is the verified Access email on ctx ("" when the request did not pass
// the Access check).
func Email(ctx context.Context) string {
	e, _ := ctx.Value(ctxKey{}).(string)
	return e
}

// Middleware answers 403 unless the request carries a valid Access JWT; exempt
// requests (the kubelet's probes) skip the check. A nil Verifier denies
// everything: a mode that requires Access never runs open.
func Middleware(v *Verifier, exempt func(*http.Request) bool, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt != nil && exempt(r) {
				next.ServeHTTP(w, r)
				return
			}
			email, err := v.Verify(r.Context(), r.Header.Get(Header))
			if err != nil {
				if log != nil {
					log.Warn("cloudflare access: request refused", "path", r.URL.Path, "reason", err.Error())
				}
				if errors.Is(err, ErrKeysUnavailable) {
					http.Error(w, "admin sign-in is temporarily unavailable: cannot reach Cloudflare Access keys", http.StatusServiceUnavailable)
					return
				}
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithEmail(r.Context(), email)))
		})
	}
}
