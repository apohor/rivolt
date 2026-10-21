// Package appauth is the server side of the native app's sign-in (iOS design
// §2): the app never handles a password or an IdP token. It opens the
// server's own web sign-in in a system browser sheet and receives a one-time
// code, bound to its PKCE challenge, which it exchanges for a session token.
//
//	app:     verifier, challenge = S256(verifier)
//	browser: GET /api/auth/app/start?code_challenge=…   → Begin (state cookie)
//	         … the usual OIDC sign-in …
//	         GET /api/auth/app/finish (signed in, state cookie)
//	                                                     → Finish → 302 callback?code=…
//	app:     POST /api/auth/app/token {code, code_verifier}
//	                                                     → Redeem → user id
//
// What makes it safe (DA's conditions on the design):
//   - the callback is fixed by the server, never taken from the request;
//   - the state cookie binds finish to the browser that started, so a start
//     link sent to someone else can't complete into the sender's challenge;
//   - the code lives 60 s, works once, and is only good with the verifier
//     whose S256 is the challenge (a wrong verifier burns it);
//   - only HMACs of the state value and the code are stored.
package appauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const (
	// StartTTL is how long a started sign-in may take to come back to
	// finish (the user signs in with the IdP in between).
	StartTTL = 10 * time.Minute
	// CodeTTL is how long the one-time code is good for: the app redeems it
	// immediately after the browser sheet closes.
	CodeTTL = 60 * time.Second
	// purgeAfter keeps finished or abandoned rows a little past expiry so a
	// late request still reads as "expired", then they go.
	purgeAfter = time.Hour
)

// ErrInvalid covers every way a start value, code or verifier can be wrong:
// unknown, expired, already used, or not matching. Callers can't tell which,
// so neither can an attacker.
var ErrInvalid = errors.New("appauth: invalid or expired")

// challengeRE is an S256 challenge: base64url of a SHA-256 digest, 43 chars.
var challengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// verifierRE is an RFC 7636 code_verifier: 43–128 unreserved characters.
var verifierRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// ValidChallenge reports whether c looks like an S256 PKCE challenge.
func ValidChallenge(c string) bool { return challengeRE.MatchString(c) }

// Store keeps sign-in attempts in app_auth_requests.
type Store struct {
	db     *sql.DB
	pepper []byte
	now    func() time.Time
}

// New returns a Store. pepper is the session-token HMAC key (the cookie
// secret), so state values and codes are stored like session tokens.
func New(db *sql.DB, pepper []byte) (*Store, error) {
	if db == nil {
		return nil, errors.New("appauth: nil db")
	}
	if len(pepper) < 32 {
		return nil, errors.New("appauth: pepper must be at least 32 bytes")
	}
	return &Store{db: db, pepper: pepper, now: time.Now}, nil
}

// Begin records a sign-in attempt for challenge and returns the raw state
// value the caller puts in the browser's state cookie.
func (s *Store) Begin(ctx context.Context, challenge string) (string, error) {
	if !ValidChallenge(challenge) {
		return "", ErrInvalid
	}
	raw, err := random()
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	// Opportunistic clean-up: attempts are rare, so purging here keeps the
	// table at a handful of rows without a separate job.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM app_auth_requests WHERE expires_at < $1`, now.Add(-purgeAfter)); err != nil {
		return "", fmt.Errorf("appauth: purge: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO app_auth_requests (state_hash, challenge, created_at, expires_at) VALUES ($1, $2, $3, $4)`,
		s.hash(raw), challenge, now, now.Add(StartTTL)); err != nil {
		return "", fmt.Errorf("appauth: insert: %w", err)
	}
	return raw, nil
}

// Finish binds the signed-in user to the attempt the browser's state value
// names and returns the one-time code for the app. Each attempt yields one
// code: a second Finish (a replayed finish URL) fails.
func (s *Store) Finish(ctx context.Context, state string, uid uuid.UUID) (string, error) {
	if state == "" || uid == uuid.Nil {
		return "", ErrInvalid
	}
	code, err := random()
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE app_auth_requests
		   SET user_id = $2, code_hash = $3, code_expires_at = $4
		 WHERE state_hash = $1 AND expires_at > $5 AND code_hash IS NULL`,
		s.hash(state), uid, s.hash(code), now.Add(CodeTTL), now)
	if err != nil {
		return "", fmt.Errorf("appauth: finish: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrInvalid
	}
	return code, nil
}

// Redeem exchanges a code and its PKCE verifier for the user it was issued
// to. The row is deleted before the verifier is checked, so a code works
// once and a wrong verifier burns it.
func (s *Store) Redeem(ctx context.Context, code, verifier string) (uuid.UUID, error) {
	if code == "" || !verifierRE.MatchString(verifier) {
		return uuid.Nil, ErrInvalid
	}
	var (
		uid       uuid.NullUUID
		challenge string
		expires   sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `
		DELETE FROM app_auth_requests WHERE code_hash = $1
		RETURNING user_id, challenge, code_expires_at`, s.hash(code)).Scan(&uid, &challenge, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, ErrInvalid
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("appauth: redeem: %w", err)
	}
	if !uid.Valid || !expires.Valid || !s.now().Before(expires.Time) {
		return uuid.Nil, ErrInvalid
	}
	if subtle.ConstantTimeCompare([]byte(Challenge(verifier)), []byte(challenge)) != 1 {
		return uuid.Nil, ErrInvalid
	}
	return uid.UUID, nil
}

// Challenge is S256: base64url(SHA-256(verifier)), no padding (RFC 7636).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Store) hash(raw string) []byte {
	m := hmac.New(sha256.New, s.pepper)
	m.Write([]byte(raw))
	return m.Sum(nil)
}

func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("appauth: rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
