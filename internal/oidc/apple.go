package oidc

// Sign in with Apple (App Store guideline 4.8; architect's design, DA-approved Oct 24, 2026). Apple is an OIDC
// provider with four differences, each handled here:
//
//   - the client secret is a short-lived ES256 JWT signed with the Sign in with Apple key (.p8), minted for each
//     token exchange (appleConfig.clientSecret);
//   - asking for the email (and name) makes Apple answer with response_mode=form_post: the callback is a
//     cross-site POST, so this provider's state cookie is SameSite=None (only this one; every other provider's
//     stays Lax), host-only, Secure, HttpOnly, scoped to its own callback path, 10 minutes, cleared on use;
//   - the id_token's email_verified and is_private_email may be the strings "true"/"false" (flexBool);
//   - the name comes once, unsigned, in the form's "user" field on the first authorization; it only prefills a
//     new account's display name and is never stored on its own or logged.
//
// Accounts: an Apple identity already linked to an account (AppleLinks) signs in to that account whatever its
// email says now. An unlinked one joins by verified email like Google and GitHub, except a private relay
// address (is_private_email): it never joins an existing account. The refresh token is handed to AppleLinks,
// which keeps it sealed for revocation; the access and id tokens are not kept. No Apple identifier (sub, relay
// email, token) is logged.

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/logging"
)

const (
	// kindApple is the provider kind (RIVOLT_OIDC_<NAME>_KIND=apple).
	kindApple = "apple"
	// appleIssuer is Apple's OIDC issuer and the client secret's audience.
	appleIssuer = "https://appleid.apple.com"
	// appleStateCookie is the Apple flow's state cookie. __Secure- makes the browser refuse it without Secure;
	// it's host-only (no Domain) so no other subdomain ever receives it.
	appleStateCookie = "__Secure-rivolt_oidc_apple"
	// appleSecretTTL is how long a minted client secret is valid: one exchange needs seconds; Apple allows up to
	// six months.
	appleSecretTTL = 5 * time.Minute
	// appleFormMax bounds the form_post body (code, state, id_token, user).
	appleFormMax = 64 << 10
)

// ErrAppleLinkConflict is returned by AppleLinks.Link when the identity or the account is already linked
// elsewhere.
var ErrAppleLinkConflict = errors.New("oidc: Apple identity linked to another account")

// AppleLinks is how the callback finds and records the account an Apple identity belongs to (internal/appleid).
type AppleLinks interface {
	// UsernameBySub returns the account key of the account linked to sub, or ok=false.
	UsernameBySub(ctx context.Context, sub string) (username string, ok bool, err error)
	// Link records that uid signs in with sub and keeps refreshToken (may be "") for revocation. A conflict is
	// ErrAppleLinkConflict.
	Link(ctx context.Context, uid uuid.UUID, sub, refreshToken string) error
}

// appleConfig is an Apple provider's signing identity.
type appleConfig struct {
	teamID    string
	keyID     string
	key       *ecdsa.PrivateKey
	revokeURL string // discovery's revocation_endpoint
}

// parseAppleKey reads the Sign in with Apple key: the .p8 file's PKCS#8 PEM (an EC P-256 key).
func parseAppleKey(p8 string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(p8)))
	if block == nil {
		return nil, errors.New("not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve.Params().Name != "P-256" {
		return nil, errors.New("not an EC P-256 key")
	}
	return ec, nil
}

// clientSecret mints the client_secret for one token exchange: an ES256 JWT, iss the Team ID, sub the client
// (Services) ID, aud Apple's issuer, kid the key ID.
func (a *appleConfig) clientSecret(clientID string, now time.Time) (string, error) {
	enc := func(v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	h, err := enc(map[string]string{"alg": "ES256", "kid": a.keyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	c, err := enc(map[string]any{
		"iss": a.teamID,
		"iat": now.Unix(),
		"exp": now.Add(appleSecretTTL).Unix(),
		"aud": appleIssuer,
		"sub": clientID,
	})
	if err != nil {
		return "", err
	}
	signing := h + "." + c
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, sum[:])
	if err != nil {
		return "", err
	}
	// JWS ES256: r and s, each left-padded to 32 bytes.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// flexBool is a boolean claim that may come as true/false or as the strings "true"/"false" (Apple's
// email_verified and is_private_email). Anything else reads as false.
type flexBool bool

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	switch strings.Trim(strings.TrimSpace(string(raw)), `"`) {
	case "true":
		*b = true
	default:
		*b = false
	}
	return nil
}

// appleFirstName reads the display name from the form's "user" field (first authorization only; unsigned, so
// it's used for nothing but a new account's display name).
func appleFirstName(userField string) string {
	if strings.TrimSpace(userField) == "" {
		return ""
	}
	var u struct {
		Name struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"name"`
	}
	if json.Unmarshal([]byte(userField), &u) != nil {
		return ""
	}
	name := strings.TrimSpace(u.Name.FirstName + " " + u.Name.LastName)
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

// appleCookiePath is the Apple state cookie's path: this provider's routes only.
func appleCookiePath(name string) string { return "/api/auth/oidc/" + name }

// setStateCookie writes the flow-state cookie for p: Lax for every provider, except Apple's, which must survive
// Apple's cross-site POST (see the file comment).
func (s *Service) setStateCookie(w http.ResponseWriter, p *provider, value string) {
	if p.cfg.Kind == kindApple {
		http.SetCookie(w, &http.Cookie{
			Name:     appleStateCookie,
			Value:    value,
			Path:     appleCookiePath(p.cfg.Name),
			HttpOnly: true,
			Secure:   true, // always: SameSite=None and the __Secure- prefix both require it
			SameSite: http.SameSiteNoneMode,
			MaxAge:   int(stateMaxAge.Seconds()),
		})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    value,
		Path:     "/api/auth/oidc",
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode, // Strict blocks the cross-site IdP→app redirect.
		MaxAge:   int(stateMaxAge.Seconds()),
	})
}

// stateCookieFor reads p's flow-state cookie.
func stateCookieFor(r *http.Request, p *provider) (*http.Cookie, error) {
	if p.cfg.Kind == kindApple {
		return r.Cookie(appleStateCookie)
	}
	return r.Cookie(stateCookieName)
}

// clearAppleCookie expires the Apple state cookie (single use: every callback outcome clears it).
func clearAppleCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     appleStateCookie,
		Value:    "",
		Path:     appleCookiePath(name),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		MaxAge:   -1,
	})
}

// exchangeSecretFor returns the client secret for one exchange with p: Apple's is minted, others are static.
func exchangeSecretFor(p *provider, now time.Time) (string, error) {
	if p.cfg.Kind != kindApple {
		return p.oauth.ClientSecret, nil
	}
	if p.apple == nil {
		return "", errors.New("apple provider without a signing key")
	}
	sec, err := p.apple.clientSecret(p.cfg.ClientID, now)
	if err != nil {
		return "", fmt.Errorf("mint apple client secret: %w", err)
	}
	return sec, nil
}

// appleAccountChoice is the account an Apple sign-in goes to.
type appleAccountChoice struct {
	username string
	email    string // "" for a linked account: it keeps its own address
	linked   bool   // the identity is already linked to this account
	isNew    bool   // no account exists yet (the name may prefill it)
}

// appleAccount picks the account for an Apple identity: the linked account when there is one, else key's (a
// verified email joins like any provider), except that a private relay address never joins an account that
// already exists. ok=false means the response has been written.
func (s *Service) appleAccount(w http.ResponseWriter, r *http.Request, sub string, privateEmail bool, key identityKey) (appleAccountChoice, bool) {
	if s.appleLinks == nil {
		s.log.Error("oidc: apple provider without AppleLinks")
		s.failRedirect(w, r, "server_error", "Something went wrong on our end. Please try again.")
		return appleAccountChoice{}, false
	}
	if strings.TrimSpace(sub) == "" {
		s.failRedirect(w, r, "verification_failed", "Your provider didn't share enough information to sign you in.")
		return appleAccountChoice{}, false
	}
	linked, found, err := s.appleLinks.UsernameBySub(r.Context(), sub)
	if err != nil {
		s.log.Error("oidc: apple link lookup", "err", logging.ScrubErr(err))
		s.failRedirect(w, r, "server_error", "Something went wrong on our end. Please try again.")
		return appleAccountChoice{}, false
	}
	if found {
		return appleAccountChoice{username: linked, linked: true}, true
	}
	if key.Username == "" {
		return appleAccountChoice{email: key.Email, isNew: true}, true // refused by the caller
	}
	exists := true // fails closed: no relay join, no name prefill
	if s.userExists != nil {
		if exists, err = s.userExists(r.Context(), key.Username); err != nil {
			s.log.Error("oidc: account lookup", "err", logging.ScrubErr(err))
			s.failRedirect(w, r, "server_error", "Something went wrong on our end. Please try again.")
			return appleAccountChoice{}, false
		}
	}
	if exists && privateEmail {
		s.log.Info("oidc: sign-in refused", "provider", "apple", "reason", "apple_relay: relay address matches an unlinked account")
		s.failRedirect(w, r, "apple_relay", "This Apple ID hides its email, and an account already uses that address. Sign in the way you did before.")
		return appleAccountChoice{}, false
	}
	return appleAccountChoice{username: key.Username, email: key.Email, isNew: !exists}, true
}

// ErrAppleNotConfigured is AppleRevoke's answer when no Apple provider is ready (not configured, or discovery still
// retrying): the caller queues the revoke for later.
var ErrAppleNotConfigured = errors.New("oidc: no Apple provider ready")

// appleRevokeClient bounds a revoke call; the caller's context may be longer.
var appleRevokeClient = &http.Client{Timeout: 15 * time.Second}

// AppleRevoke revokes a Sign in with Apple refresh token (and with it the user's authorization of Rivolt) at
// Apple's revocation endpoint, with a freshly minted client secret. Apple answering invalid_grant (the token is
// already revoked or expired) counts as done. Errors never include the token.
func (s *Service) AppleRevoke(ctx context.Context, refreshToken string) error {
	if s == nil {
		return ErrAppleNotConfigured
	}
	var p *provider
	s.mu.RLock()
	for _, cand := range s.providers {
		if cand.cfg.Kind == kindApple && cand.apple != nil {
			p = cand
			break
		}
	}
	s.mu.RUnlock()
	if p == nil {
		return ErrAppleNotConfigured
	}
	secret, err := exchangeSecretFor(p, time.Now())
	if err != nil {
		return err
	}
	form := url.Values{
		"client_id":       {p.cfg.ClientID},
		"client_secret":   {secret},
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apple.revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("apple revoke: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := appleRevokeClient.Do(req)
	if err != nil {
		// *url.Error prints the URL, never the form body.
		return fmt.Errorf("apple revoke: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var apiErr struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &apiErr)
	if resp.StatusCode == http.StatusBadRequest && apiErr.Error == "invalid_grant" {
		return nil // already revoked or expired: nothing left to revoke
	}
	if apiErr.Error != "" {
		return fmt.Errorf("apple revoke: %s: %s", resp.Status, apiErr.Error)
	}
	return fmt.Errorf("apple revoke: %s", resp.Status)
}
