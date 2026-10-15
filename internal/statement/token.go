package statement

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TokenTTL is how long an unsubscribe link in an email works.
const TokenTTL = 90 * 24 * time.Hour

// ErrBadToken is returned for any malformed, tampered or expired
// token; callers never learn which.
var ErrBadToken = errors.New("statement: invalid or expired token")

// Signer signs and verifies one-click unsubscribe tokens:
// base64url(user_id|period|expiry_unix) "." base64url(HMAC-SHA256).
// The key is derived from the server's session secret ("pepper") with
// a purpose label, so a session cookie can never be replayed as a
// token or the reverse.
type Signer struct{ key []byte }

// NewSigner derives the signing key from secret. A short or empty
// secret is refused by the caller (main), not here.
func NewSigner(secret []byte) *Signer {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("rivolt/statement-email/unsubscribe/v1"))
	return &Signer{key: m.Sum(nil)}
}

func (s *Signer) mac(payload string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// Sign returns a token for (uid, period) valid until exp.
func (s *Signer) Sign(uid uuid.UUID, period string, exp time.Time) string {
	payload := fmt.Sprintf("%s|%s|%d", uid, period, exp.Unix())
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(s.mac(payload))
}

// Verify checks signature and expiry (constant-time compare) and
// returns the user and period.
func (s *Signer) Verify(tok string, now time.Time) (uuid.UUID, string, error) {
	if s == nil || len(tok) > 512 {
		return uuid.Nil, "", ErrBadToken
	}
	p, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return uuid.Nil, "", ErrBadToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return uuid.Nil, "", ErrBadToken
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.mac(string(raw))) {
		return uuid.Nil, "", ErrBadToken
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return uuid.Nil, "", ErrBadToken
	}
	uid, err := uuid.Parse(parts[0])
	if err != nil || uid == uuid.Nil {
		return uuid.Nil, "", ErrBadToken
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || !now.Before(time.Unix(exp, 0)) {
		return uuid.Nil, "", ErrBadToken
	}
	return uid, parts[1], nil
}
