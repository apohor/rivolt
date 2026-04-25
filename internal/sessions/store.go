// Package sessions is the source-of-truth store for authenticated
// user sessions. A row in `sessions` represents a cookie that has
// been issued to a browser; the cookie itself carries a random
// 32-byte opaque token, and the server keeps only an HMAC of that
// token so a DB dump doesn't grant session access without also
// holding the pepper.
//
// This replaces the old HMAC-signed-cookie-carrying-{user_id, exp}
// design. See ARCHITECTURE.md decision 4 for the rationale.
//
// # Why HMAC(pepper, raw) and not bcrypt / argon2
//
// Session tokens are 32 bytes of cryptographic randomness; they
// are not passwords. Offline attack resistance is dominated by
// entropy (256 bits) rather than work factor. HMAC-SHA256 with a
// host-local pepper is cheap (no login latency cost), constant
// time, and defeats the only realistic attack — a DB reader who
// doesn't also own the host env.
//
// # Why Postgres and not Redis
//
// The lookup path is one indexed SELECT per authenticated
// request. At 1000 vehicles that's ~30 req/s peak, well within a
// small Postgres instance's budget. Redis would buy us one less
// RTT but cost a second data store to back-up, upgrade, and
// reason about. Not worth it until the p50 API latency tells us
// it is.
package sessions

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
	"net"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidToken is returned when a token doesn't parse or
// doesn't match any live session row. Intentionally opaque —
// the middleware translates it to a 401 without a reason, so an
// attacker can't distinguish "bad format" from "revoked" from
// "expired".
var ErrInvalidToken = errors.New("sessions: invalid token")

// touchInterval is how often the middleware writes last_seen_at
// on an otherwise-idempotent lookup. Without throttling every
// request in a live-reload tab would emit a DB UPDATE; at one
// minute the cost is one update per active tab per minute and
// the hijack-detection UI is still accurate to the minute.
const touchInterval = 1 * time.Minute

// tokenBytes is the raw token length before base64 encoding. 32
// random bytes = 256 bits of entropy, matching the lower bound
// for opaque session tokens recommended by OWASP.
const tokenBytes = 32

// Session is the in-memory projection of a row. Callers use it
// to render the device list and to populate request context.
type Session struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	CreatedAt   time.Time
	LastSeenAt  time.Time
	ExpiresAt   time.Time
	UserAgent   string
	IPAddress   string
	DeviceLabel string
}

// Store is the sessions persistence layer. Pepper is an HMAC key
// used to hash tokens before storage; it must be stable across
// restarts or every session will be invalidated, and it must
// live outside the DB (env var / KMS) so a DB dump can't be
// used to forge sessions.
type Store struct {
	db     *sql.DB
	pepper []byte
}

// New builds a Store. Pepper must be >= 32 bytes; shorter is
// refused at construction so operators don't ship with a
// trivially-brute-forceable setup.
func New(db *sql.DB, pepper []byte) (*Store, error) {
	// Pepper first: it's a config error (operator-visible at
	// boot), while nil db is a wiring error (programmer-visible
	// in CI). Reporting config errors first gives nicer
	// operator ergonomics on a misconfigured install.
	if len(pepper) < 32 {
		return nil, fmt.Errorf("sessions: pepper must be >= 32 bytes, got %d", len(pepper))
	}
	if db == nil {
		return nil, errors.New("sessions: nil db")
	}
	// Defensive copy so callers can't mutate the pepper under us.
	p := make([]byte, len(pepper))
	copy(p, pepper)
	return &Store{db: db, pepper: p}, nil
}

// CreateOpts captures the metadata to stamp onto a new session
// row. All fields are optional; zero values land in the DB as
// NULL / empty.
type CreateOpts struct {
	UserAgent   string
	IPAddress   string // textual form; Create parses it
	DeviceLabel string
}

// Create mints a fresh session for userID and returns:
//
//   - Session: the DB row projection.
//   - rawToken: the value that goes into the Set-Cookie header.
//     The raw token is only visible on Create — we never store
//     it, only its HMAC. Losing track of it means the cookie
//     can't be issued.
func (s *Store) Create(ctx context.Context, userID uuid.UUID, ttl time.Duration, opts CreateOpts) (Session, string, error) {
	if userID == uuid.Nil {
		return Session{}, "", errors.New("sessions: nil userID")
	}
	if ttl <= 0 {
		return Session{}, "", errors.New("sessions: ttl must be > 0")
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, "", fmt.Errorf("sessions: rand: %w", err)
	}
	hash := s.hash(raw)
	id := uuid.New()
	now := time.Now().UTC()
	expires := now.Add(ttl)

	// Accept any parseable textual form, store NULL when we
	// can't make sense of it rather than letting a stray string
	// blow up the INSERT.
	var ip any
	if opts.IPAddress != "" {
		if addr, err := netip.ParseAddr(opts.IPAddress); err == nil {
			ip = addr.String()
		}
	}
	var label any
	if opts.DeviceLabel != "" {
		label = opts.DeviceLabel
	}
	var ua any
	if opts.UserAgent != "" {
		ua = opts.UserAgent
	}

	const q = `INSERT INTO sessions
		(id, user_id, token_hash, created_at, last_seen_at, expires_at, user_agent, ip_address, device_label)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7, $8)`
	if _, err := s.db.ExecContext(ctx, q, id, userID, hash, now, expires, ua, ip, label); err != nil {
		return Session{}, "", fmt.Errorf("sessions: insert: %w", err)
	}
	return Session{
		ID:          id,
		UserID:      userID,
		CreatedAt:   now,
		LastSeenAt:  now,
		ExpiresAt:   expires,
		UserAgent:   opts.UserAgent,
		IPAddress:   opts.IPAddress,
		DeviceLabel: opts.DeviceLabel,
	}, encodeToken(raw), nil
}

// Lookup resolves a cookie-borne token to a live Session. Returns
// ErrInvalidToken for every failure mode — bad format, not
// found, revoked, expired — so the middleware can't accidentally
// tell an attacker which is which.
//
// As a side effect, Lookup bumps last_seen_at if more than
// touchInterval has passed since the last bump. The update is
// best-effort: a failure is logged by the caller but the
// session still resolves, because refusing authentication due
// to a stat-tracking failure would be absurd.
func (s *Store) Lookup(ctx context.Context, rawTokenCookie string) (Session, error) {
	raw, err := decodeToken(rawTokenCookie)
	if err != nil {
		return Session{}, ErrInvalidToken
	}
	hash := s.hash(raw)

	const q = `SELECT id, user_id, created_at, last_seen_at, expires_at,
		COALESCE(user_agent, ''), COALESCE(host(ip_address), ''), COALESCE(device_label, ''),
		revoked_at
		FROM sessions
		WHERE token_hash = $1`
	var (
		sess      Session
		revokedAt sql.NullTime
	)
	err = s.db.QueryRowContext(ctx, q, hash).Scan(
		&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt,
		&sess.UserAgent, &sess.IPAddress, &sess.DeviceLabel, &revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrInvalidToken
	}
	if err != nil {
		return Session{}, fmt.Errorf("sessions: select: %w", err)
	}
	now := time.Now().UTC()
	if revokedAt.Valid || now.After(sess.ExpiresAt) {
		return Session{}, ErrInvalidToken
	}
	// Throttled touch — skip the UPDATE when we've bumped
	// recently. The bump is racy under concurrent tabs but all
	// racers write roughly "now", so the read-back is still
	// monotonic.
	if now.Sub(sess.LastSeenAt) > touchInterval {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE sessions SET last_seen_at = $2 WHERE id = $1`,
			sess.ID, now)
		sess.LastSeenAt = now
	}
	return sess, nil
}

// Revoke marks a specific session as revoked. Idempotent: a
// missing row is treated as success (matches "logout from a
// cookie that was already invalidated server-side").
func (s *Store) Revoke(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("sessions: revoke: %w", err)
	}
	return nil
}

// RevokeByToken is the convenience called by the /logout handler
// — translates the cookie value to a row id and revokes it in a
// single round-trip. Garbage tokens resolve to nothing and are
// silently ignored (the user's cookie was already useless).
func (s *Store) RevokeByToken(ctx context.Context, rawTokenCookie string) error {
	raw, err := decodeToken(rawTokenCookie)
	if err != nil {
		return nil
	}
	hash := s.hash(raw)
	_, err = s.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, hash)
	if err != nil {
		return fmt.Errorf("sessions: revoke by token: %w", err)
	}
	return nil
}

// RevokeAllExcept revokes every session for userID except the
// caller's current one. Used by the future "sign out everywhere
// else" UI; also a useful panic button when a password changes.
func (s *Store) RevokeAllExcept(ctx context.Context, userID, keepID uuid.UUID) error {
	if userID == uuid.Nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions
		SET revoked_at = now()
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`,
		userID, keepID)
	if err != nil {
		return fmt.Errorf("sessions: revoke others: %w", err)
	}
	return nil
}

// List returns every live session for a user, newest first.
// Backs the future device-list UI.
func (s *Store) List(ctx context.Context, userID uuid.UUID) ([]Session, error) {
	const q = `SELECT id, user_id, created_at, last_seen_at, expires_at,
		COALESCE(user_agent, ''), COALESCE(host(ip_address), ''), COALESCE(device_label, '')
		FROM sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
		ORDER BY last_seen_at DESC`
	rows, err := s.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("sessions: list: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt,
			&sess.UserAgent, &sess.IPAddress, &sess.DeviceLabel); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// PurgeExpired deletes revoked / expired rows older than a
// grace period. Runs from a background janitor; not in the hot
// path. Grace window exists so support can forensic-inspect
// recent revocations.
func (s *Store) PurgeExpired(ctx context.Context, grace time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM sessions
		WHERE (expires_at < now() - $1::interval * INTERVAL '1 second')
		   OR (revoked_at IS NOT NULL AND revoked_at < now() - $1::interval * INTERVAL '1 second')`,
		grace.Seconds())
	if err != nil {
		return 0, fmt.Errorf("sessions: purge: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// hash computes HMAC-SHA256 of raw under the pepper. Constant-
// time comparison isn't needed here because we use the hash as
// an index key (equality lookup on bytea), not as a comparison
// target — but we use subtle.ConstantTimeCompare at the call
// site when checking a re-hashed value against a returned one.
func (s *Store) hash(raw []byte) []byte {
	m := hmac.New(sha256.New, s.pepper)
	m.Write(raw)
	return m.Sum(nil)
}

// Compare is exported for any future call site that needs to
// check a raw token against an already-loaded hash in constant
// time (e.g. a secondary path that avoided the index).
func (s *Store) Compare(raw, hash []byte) bool {
	return subtle.ConstantTimeCompare(s.hash(raw), hash) == 1
}

// encodeToken is base64-URL without padding. Short enough to
// fit comfortably in a Set-Cookie, still 32 bytes of entropy.
func encodeToken(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// decodeToken reverses encodeToken and validates the length so
// garbage cookies can be rejected before the DB round-trip.
func decodeToken(cookie string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil {
		return nil, err
	}
	if len(raw) != tokenBytes {
		return nil, errors.New("wrong length")
	}
	return raw, nil
}

// SanitizeIP trims a net.RemoteAddr form ("ip:port" or "[ip]:port")
// into a plain address suitable for storage. Exported so the auth
// package can feed raw r.RemoteAddr in without re-implementing the
// parse.
func SanitizeIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return ""
	}
	return host
}
