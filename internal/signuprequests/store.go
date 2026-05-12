// Package signuprequests manages the pre-account waitlist: someone
// visits /signup without a code, fills the "request access" form, and
// lands as a pending row that an admin later approves (minting a real
// invite_code) or rejects.
//
// Rows live outside the multi-tenant grain — there is no user_id and
// no RLS, because the row exists before the requester has an account.
// Admin-facing handlers run inside the existing requireAdmin gate so
// listing the table is safe.
package signuprequests

import (
	"context"
	"database/sql"
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

// Request is the read view of a signup_requests row.
type Request struct {
	ID          uuid.UUID
	Email       string
	Message     string
	Status      string
	InviteCode  *string
	DecidedBy   *uuid.UUID
	DecidedAt   *time.Time
	RequestedAt time.Time
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
		RETURNING id, email, message, status, invite_code, decided_by, decided_at, requested_at
	`, email, message).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		// invite_code / decided_by / decided_at are NULL on insert; use
		// nullable scan helpers.
		nullStringScanner{&r.InviteCode},
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
		SELECT id, email, message, status, invite_code, decided_by, decided_at, requested_at
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
			nullStringScanner{&r.InviteCode},
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
		SELECT id, email, message, status, invite_code, decided_by, decided_at, requested_at
		  FROM signup_requests
		 WHERE id = $1
	`, id).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		nullStringScanner{&r.InviteCode},
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

// Approve transitions a pending row to approved, attaching the
// invite_code minted by the caller. Single-statement so the pending →
// approved transition is atomic; ErrNotPending if the row was already
// decided.
func (s *Store) Approve(ctx context.Context, id, decidedBy uuid.UUID, inviteCode string) (Request, error) {
	var r Request
	err := s.db.QueryRowContext(ctx, `
		UPDATE signup_requests
		   SET status = 'approved',
		       invite_code = $1,
		       decided_by = $2,
		       decided_at = NOW()
		 WHERE id = $3 AND status = 'pending'
		 RETURNING id, email, message, status, invite_code, decided_by, decided_at, requested_at
	`, inviteCode, decidedBy, id).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		nullStringScanner{&r.InviteCode},
		nullUUIDScanner{&r.DecidedBy},
		nullTimeScanner{&r.DecidedAt},
		&r.RequestedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotPending
	}
	if err != nil {
		return Request{}, fmt.Errorf("signuprequests: approve: %w", err)
	}
	return r, nil
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
		 RETURNING id, email, message, status, invite_code, decided_by, decided_at, requested_at
	`, decidedBy, id).Scan(
		&r.ID, &r.Email, &r.Message, &r.Status,
		nullStringScanner{&r.InviteCode},
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
