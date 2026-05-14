// Package trips persists per-user named trip templates for the
// planner. Inputs are required; the last computed plan and AI advice
// are stored alongside as opaque JSON snapshots so the UI can render
// "the trip I saved last week" without a Rivian round-trip on click.
package trips

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SavedTrip is one row of saved_trips. Inputs/Plan/Advice are kept as
// json.RawMessage so the package stays decoupled from rivian.TripPlan
// and the frontend shape — handlers can read/write them verbatim.
type SavedTrip struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	Inputs    json.RawMessage `json:"inputs"`
	Plan      json.RawMessage `json:"plan,omitempty"`
	Advice    json.RawMessage `json:"advice,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// ErrNotFound is returned when Get/Delete/Update target a row that
// either does not exist or belongs to a different user.
var ErrNotFound = errors.New("trips: not found")

// Store wraps access to saved_trips for a single user.
type Store struct {
	db     *sql.DB
	userID uuid.UUID
}

// OpenStore binds a pooled connection to a user_id. The pool is owned
// by the caller.
func OpenStore(db *sql.DB, userID uuid.UUID) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("trips: db is nil")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("trips: userID is zero")
	}
	return &Store{db: db, userID: userID}, nil
}

// Close is a no-op; the pool is managed by main.
func (s *Store) Close() error { return nil }

// List returns every saved trip for this user, most-recently-updated
// first. The inputs / plan / advice JSON is returned untouched.
func (s *Store) List(ctx context.Context) ([]SavedTrip, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, inputs, plan, advice, created_at, updated_at
		FROM saved_trips
		WHERE user_id = $1
		ORDER BY updated_at DESC`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedTrip
	for rows.Next() {
		t, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns a single saved trip by id, scoped to this user. Returns
// ErrNotFound when the row is missing or belongs to someone else.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*SavedTrip, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, inputs, plan, advice, created_at, updated_at
		FROM saved_trips
		WHERE user_id = $1 AND id = $2`, s.userID, id)
	t, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Create inserts a new trip under (user_id, name). Returns the
// generated UUID + timestamps. The unique (user_id, name) constraint
// turns a duplicate name into a Postgres unique-violation error; the
// caller (handler) is responsible for surfacing that as 409.
func (s *Store) Create(ctx context.Context, name string, inputs, plan, advice json.RawMessage) (*SavedTrip, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO saved_trips (user_id, name, inputs, plan, advice)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, name, inputs, plan, advice, created_at, updated_at`,
		s.userID, name, inputs, nilIfEmpty(plan), nilIfEmpty(advice),
	)
	t, err := scanRow(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Update overwrites name + inputs + plan + advice on an existing row.
// Returns ErrNotFound if the id doesn't belong to this user.
func (s *Store) Update(ctx context.Context, id uuid.UUID, name string, inputs, plan, advice json.RawMessage) (*SavedTrip, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE saved_trips
		   SET name = $3, inputs = $4, plan = $5, advice = $6, updated_at = NOW()
		 WHERE user_id = $1 AND id = $2
		RETURNING id, name, inputs, plan, advice, created_at, updated_at`,
		s.userID, id, name, inputs, nilIfEmpty(plan), nilIfEmpty(advice),
	)
	t, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Delete removes a saved trip by id. Returns ErrNotFound if no row
// matched so callers can return 404 instead of a silent 204.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM saved_trips WHERE user_id = $1 AND id = $2`,
		s.userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanRow is shared by List/Get/Create/Update so the column order
// stays in lockstep across query strings.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRow(r rowScanner) (SavedTrip, error) {
	var t SavedTrip
	var plan, advice sql.RawBytes
	if err := r.Scan(&t.ID, &t.Name, &t.Inputs, &plan, &advice, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return t, err
	}
	if len(plan) > 0 {
		t.Plan = append(json.RawMessage(nil), plan...)
	}
	if len(advice) > 0 {
		t.Advice = append(json.RawMessage(nil), advice...)
	}
	return t, nil
}

// nilIfEmpty maps an empty json.RawMessage to nil so the column lands
// as NULL instead of the literal string `null`. Lets the frontend
// safely send `{}` shapes without optional fields.
func nilIfEmpty(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}
