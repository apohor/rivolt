// Package signuprequests manages the pre-account waitlist: someone
// visits /signup without an invite, fills the "request access" form,
// and lands as a pending row that an admin later approves (minting a
// single-use magic-link signup token) or rejects.
//
// Rows live outside the multi-tenant grain — there is no user_id and
// no RLS, because the row exists before the requester has an account.
// Admin-facing handlers run inside the existing requireAdmin gate so
// listing the table is safe.
package signuprequests

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Status values stored in signup_requests.status. The DB CHECK
// constraint enforces this set.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// ErrAlreadyPending is returned when an email already has a pending
// row. Re-requests after a decision (approved/rejected) are allowed.
var ErrAlreadyPending = errors.New("signuprequests: email already has a pending request")

// ErrNotFound is returned for lookups against a missing id.
var ErrNotFound = errors.New("signuprequests: not found")

// ErrNotPending is returned when an admin tries to decide on a row
// that is not in the pending state (race or double-click).
var ErrNotPending = errors.New("signuprequests: not pending")

// ErrTokenInvalid is returned by LookupToken / ConsumeToken when the
// token does not match an approved row, has expired, or has already
// been redeemed. Single sentinel rather than separate errors so the
// frontend can show a uniform "this link is no longer valid" without
// leaking which row state caused the rejection.
var ErrTokenInvalid = errors.New("signuprequests: token invalid or expired")

// Request is the read view of a signup_requests row.
type Request struct {
	ID             uuid.UUID
	Email          string
	Message        string
	Status         string
	SignupToken    *string
	TokenExpiresAt *time.Time
	TokenUsedAt    *time.Time
	DecidedBy      *uuid.UUID
	DecidedAt      *time.Time
	RequestedAt    time.Time
}

// Store wraps the signup_requests table.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Create inserts a new pending request. Returns ErrAlreadyPending if
// the same email already has a pending row.
func (s *Store) Create(ctx context.Context, email, message string) (Request, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return Request{}, fmt.Errorf("signuprequests: empty email")
	}
	var r Request
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO signup_requests (email, message)
		VALUES ($1, $2)
		RETURNING id, email, message, status, signup_token, token_expires_at, token_used_at, decided_by, decided_at, requested_at
	`, email, message).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		// token + decided_* are NULL on insert; use nullable scanners.
		nullStringScanner{&r.SignupToken},
		nullTimeScanner{&r.TokenExpiresAt},
		nullTimeScanner{&r.TokenUsedAt},
		nullUUIDScanner{&r.DecidedBy},
		nullTimeScanner{&r.DecidedAt},
		&r.RequestedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Request{}, ErrAlreadyPending
		}
		return Request{}, fmt.Errorf("signuprequests: insert: %w", err)
	}
	return r, nil
}

// List returns rows filtered by status (empty string = all), newest
// first. Caller should already be admin-gated.
func (s *Store) List(ctx context.Context, status string) ([]Request, error) {
	var (
		rows *sql.Rows
		err  error
	)
	q := `
		SELECT id, email, message, status, signup_token, token_expires_at, token_used_at, decided_by, decided_at, requested_at
		  FROM signup_requests
	`
	if status != "" {
		rows, err = s.db.QueryContext(ctx, q+` WHERE status = $1 ORDER BY requested_at DESC`, status)
	} else {
		rows, err = s.db.QueryContext(ctx, q+` ORDER BY requested_at DESC`)
	}
	if err != nil {
		return nil, fmt.Errorf("signuprequests: list: %w", err)
	}
	defer rows.Close()
	out := make([]Request, 0)
	for rows.Next() {
		var r Request
		if err := rows.Scan(
			&r.ID, &r.Email, &r.Message, &r.Status,
			nullStringScanner{&r.SignupToken},
			nullTimeScanner{&r.TokenExpiresAt},
			nullTimeScanner{&r.TokenUsedAt},
			nullUUIDScanner{&r.DecidedBy},
			nullTimeScanner{&r.DecidedAt},
			&r.RequestedAt,
		); err != nil {
			return nil, fmt.Errorf("signuprequests: scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get fetches one row by id.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Request, error) {
	var r Request
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, message, status, signup_token, token_expires_at, token_used_at, decided_by, decided_at, requested_at
		  FROM signup_requests
		 WHERE id = $1
	`, id).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		nullStringScanner{&r.SignupToken},
		nullTimeScanner{&r.TokenExpiresAt},
		nullTimeScanner{&r.TokenUsedAt},
		nullUUIDScanner{&r.DecidedBy},
		nullTimeScanner{&r.DecidedAt},
		&r.RequestedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("signuprequests: get: %w", err)
	}
	return r, nil
}

// DefaultTokenTTL is how long a magic-link signup token stays
// valid. 14 days is generous — the requester might be on vacation
// when the approval lands — and short enough that an intercepted
// token can't lurk indefinitely.
const DefaultTokenTTL = 14 * 24 * time.Hour

// ApproveWithToken transitions a pending row to approved and stamps
// a single-use signup_token + expiry on it. The returned Request
// carries the token so the caller can email a sign-up link. Distinct
// from Approve (legacy invite_code path) so the two flows can
// coexist while we deprecate codes.
func (s *Store) ApproveWithToken(ctx context.Context, id, decidedBy uuid.UUID, ttl time.Duration) (Request, error) {
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	token, err := newSignupToken()
	if err != nil {
		return Request{}, fmt.Errorf("signuprequests: mint token: %w", err)
	}
	expires := time.Now().Add(ttl)
	var r Request
	err = s.db.QueryRowContext(ctx, `
		UPDATE signup_requests
		   SET status = 'approved',
		       signup_token = $1,
		       token_expires_at = $2,
		       token_used_at = NULL,
		       decided_by = $3,
		       decided_at = NOW()
		 WHERE id = $4 AND status = 'pending'
		 RETURNING id, email, message, status, signup_token, token_expires_at, token_used_at, decided_by, decided_at, requested_at
	`, token, expires, decidedBy, id).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		nullStringScanner{&r.SignupToken},
		nullTimeScanner{&r.TokenExpiresAt},
		nullTimeScanner{&r.TokenUsedAt},
		nullUUIDScanner{&r.DecidedBy},
		nullTimeScanner{&r.DecidedAt},
		&r.RequestedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotPending
	}
	if err != nil {
		return Request{}, fmt.Errorf("signuprequests: approve with token: %w", err)
	}
	return r, nil
}

// LookupToken returns the approved row whose signup_token matches.
// Used by the public GET /api/signup/token/{token} endpoint to
// prefill the email on the signup form. Returns ErrTokenInvalid for
// missing/expired/already-redeemed tokens.
func (s *Store) LookupToken(ctx context.Context, token string) (Request, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Request{}, ErrTokenInvalid
	}
	var r Request
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, message, status, signup_token, token_expires_at, token_used_at, decided_by, decided_at, requested_at
		  FROM signup_requests
		 WHERE signup_token = $1
	`, token).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		nullStringScanner{&r.SignupToken},
		nullTimeScanner{&r.TokenExpiresAt},
		nullTimeScanner{&r.TokenUsedAt},
		nullUUIDScanner{&r.DecidedBy},
		nullTimeScanner{&r.DecidedAt},
		&r.RequestedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrTokenInvalid
	}
	if err != nil {
		return Request{}, fmt.Errorf("signuprequests: lookup token: %w", err)
	}
	if r.Status != StatusApproved {
		return Request{}, ErrTokenInvalid
	}
	if r.TokenUsedAt != nil {
		return Request{}, ErrTokenInvalid
	}
	if r.TokenExpiresAt != nil && time.Now().After(*r.TokenExpiresAt) {
		return Request{}, ErrTokenInvalid
	}
	return r, nil
}

// ConsumeToken atomically marks the token as redeemed. Returns
// ErrTokenInvalid if the token was already used, has expired, or
// does not exist. The single UPDATE…WHERE checks all three so two
// concurrent submissions can't both succeed.
func (s *Store) ConsumeToken(ctx context.Context, token string) (Request, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Request{}, ErrTokenInvalid
	}
	var r Request
	err := s.db.QueryRowContext(ctx, `
		UPDATE signup_requests
		   SET token_used_at = NOW()
		 WHERE signup_token = $1
		   AND status = 'approved'
		   AND token_used_at IS NULL
		   AND (token_expires_at IS NULL OR token_expires_at > NOW())
		 RETURNING id, email, message, status, signup_token, token_expires_at, token_used_at, decided_by, decided_at, requested_at
	`, token).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		nullStringScanner{&r.SignupToken},
		nullTimeScanner{&r.TokenExpiresAt},
		nullTimeScanner{&r.TokenUsedAt},
		nullUUIDScanner{&r.DecidedBy},
		nullTimeScanner{&r.DecidedAt},
		&r.RequestedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrTokenInvalid
	}
	if err != nil {
		return Request{}, fmt.Errorf("signuprequests: consume token: %w", err)
	}
	return r, nil
}

// newSignupToken returns a 26-char URL-safe base32 string (130 bits
// of randomness, well above the ~80-bit floor for unguessable tokens
// served over public URLs).
func newSignupToken() (string, error) {
	b := make([]byte, 17) // ceil(26 * 5 / 8) = 17 → 27 chars; trim to 26
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	if len(s) > 26 {
		s = s[:26]
	}
	return strings.ToUpper(s), nil
}

// Reject transitions a pending row to rejected.
func (s *Store) Reject(ctx context.Context, id, decidedBy uuid.UUID) (Request, error) {
	var r Request
	err := s.db.QueryRowContext(ctx, `
		UPDATE signup_requests
		   SET status = 'rejected',
		       decided_by = $1,
		       decided_at = NOW()
		 WHERE id = $2 AND status = 'pending'
		 RETURNING id, email, message, status, signup_token, token_expires_at, token_used_at, decided_by, decided_at, requested_at
	`, decidedBy, id).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		nullStringScanner{&r.SignupToken},
		nullTimeScanner{&r.TokenExpiresAt},
		nullTimeScanner{&r.TokenUsedAt},
		nullUUIDScanner{&r.DecidedBy},
		nullTimeScanner{&r.DecidedAt},
		&r.RequestedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotPending
	}
	if err != nil {
		return Request{}, fmt.Errorf("signuprequests: reject: %w", err)
	}
	return r, nil
}

// --- nullable scan helpers ---

type nullStringScanner struct{ dst **string }

func (s nullStringScanner) Scan(src any) error {
	var n sql.NullString
	if err := n.Scan(src); err != nil {
		return err
	}
	if n.Valid {
		v := n.String
		*s.dst = &v
	}
	return nil
}

type nullTimeScanner struct{ dst **time.Time }

func (s nullTimeScanner) Scan(src any) error {
	var n sql.NullTime
	if err := n.Scan(src); err != nil {
		return err
	}
	if n.Valid {
		v := n.Time
		*s.dst = &v
	}
	return nil
}

type nullUUIDScanner struct{ dst **uuid.UUID }

func (s nullUUIDScanner) Scan(src any) error {
	if src == nil {
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("signuprequests: unexpected uuid scan type %T", src)
	}
	u, err := uuid.Parse(string(raw))
	if err != nil {
		return err
	}
	*s.dst = &u
	return nil
}

// isUniqueViolation detects the partial-index conflict from the
// pending-email constraint. pgx and lib/pq surface SQLSTATE 23505 in
// the error string.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}
