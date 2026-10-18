// Package adminaudit records admin actions: who did what to whom, when and
// from where. Rows are written before the action runs (so an action cannot
// happen unrecorded) and finished with the outcome afterwards.
package adminaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Retention is how long rows are kept.
const Retention = 2 * 365 * 24 * time.Hour

// Entry is one admin action about to happen.
type Entry struct {
	Actor      uuid.UUID
	Action     string // e.g. "user.set_role", "flag.kill_switch"
	TargetType string // "user", "flag", "settings", "signup_request", ...
	TargetID   string
	TargetUser uuid.UUID // uuid.Nil when the action is not about a user
	Details    map[string]any
	IP         string
}

// Row is a stored entry, as the listing returns it.
type Row struct {
	ID         int64          `json:"id"`
	At         time.Time      `json:"at"`
	Actor      uuid.UUID      `json:"actor_user_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	TargetUser *uuid.UUID     `json:"target_user_id,omitempty"`
	Details    map[string]any `json:"details"`
	IP         string         `json:"ip"`
	Outcome    *string        `json:"outcome"` // nil: in flight or unknown
	Status     *int           `json:"status,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
}

// Store is the Postgres side.
type Store struct{ db *sql.DB }

// New wraps the pool.
func New(db *sql.DB) *Store { return &Store{db: db} }

var enumRe = regexp.MustCompile(`^[a-z0-9_.:-]{1,40}$`)

// Clean keeps only what may be stored: booleans, numbers, short lowercase
// enum tokens, and lists of such tokens (field names). Everything else, free
// text above all, is dropped. At most 20 keys.
func Clean(in map[string]any) map[string]any {
	out := map[string]any{}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(out) >= 20 || !enumRe.MatchString(k) {
			continue
		}
		switch v := in[k].(type) {
		case bool:
			out[k] = v
		case int:
			out[k] = v
		case int64:
			out[k] = v
		case float64:
			out[k] = v
		case string:
			if enumRe.MatchString(v) {
				out[k] = v
			}
		case []string:
			var ok []string
			for _, s := range v {
				if enumRe.MatchString(s) && len(ok) < 30 {
					ok = append(ok, s)
				}
			}
			out[k] = ok
		}
	}
	return out
}

// Begin inserts the row for an action about to run and returns its id. An
// error means the action must not run.
func (s *Store) Begin(ctx context.Context, e Entry) (int64, error) {
	raw, err := json.Marshal(Clean(e.Details))
	if err != nil {
		return 0, err
	}
	var target any
	if e.TargetUser != uuid.Nil {
		target = e.TargetUser
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO admin_audit (actor_user_id, action, target_type, target_id, target_user_id, details, ip)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7) RETURNING id`,
		e.Actor, e.Action, e.TargetType, e.TargetID, target, string(raw), e.IP).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("adminaudit: begin: %w", err)
	}
	return id, nil
}

// Finish records the HTTP status the action ended with.
func (s *Store) Finish(ctx context.Context, id int64, status int) error {
	outcome := "failed"
	if status >= 200 && status < 400 {
		outcome = "ok"
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_audit SET outcome = $2, status = $3, finished_at = now() WHERE id = $1`, id, outcome, status)
	return err
}

// NoteImpersonationStart records that admin began viewing as target, once per
// pair per window (every impersonated request would otherwise add a row). A
// successful impersonation.stop ends the session: the next view-as of the same
// target logs a fresh start, so a stop is never left without its start.
func (s *Store) NoteImpersonationStart(ctx context.Context, admin, target uuid.UUID, ip string, window time.Duration) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO admin_audit (actor_user_id, action, target_type, target_id, target_user_id, ip, outcome, status, finished_at)
		SELECT $1::uuid, 'impersonation.start', 'user', ($2::uuid)::text, $2::uuid, $3::text, 'ok', 200, now()
		 WHERE NOT EXISTS (
		       SELECT 1 FROM admin_audit s
		        WHERE s.actor_user_id = $1::uuid AND s.target_user_id = $2::uuid AND s.action = 'impersonation.start'
		          AND s.at > now() - $4::interval
		          AND NOT EXISTS (
		              SELECT 1 FROM admin_audit e
		               WHERE e.actor_user_id = s.actor_user_id AND e.target_user_id = s.target_user_id
		                 AND e.action = 'impersonation.stop' AND e.outcome = 'ok' AND e.at > s.at))`,
		admin, target, ip, fmt.Sprintf("%d seconds", int(window.Seconds())))
	return err
}

// Filter narrows a listing.
type Filter struct {
	Action     string
	Actor      uuid.UUID
	TargetUser uuid.UUID
	Before     int64 // return rows with id < Before (0 = from the newest)
	Limit      int
}

// List returns rows newest first and the cursor for the next page (0 = none).
func (s *Store) List(ctx context.Context, f Filter) ([]Row, int64, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	args := []any{f.Limit + 1}
	where := "TRUE"
	add := func(cond string, v any) {
		args = append(args, v)
		where += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.Before > 0 {
		add("id < $%d", f.Before)
	}
	if f.Action != "" {
		add("action = $%d", f.Action)
	}
	if f.Actor != uuid.Nil {
		add("actor_user_id = $%d", f.Actor)
	}
	if f.TargetUser != uuid.Nil {
		add("target_user_id = $%d", f.TargetUser)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, at, actor_user_id, action, target_type, target_id, target_user_id, details, ip, outcome, status, finished_at
		  FROM admin_audit WHERE `+where+` ORDER BY id DESC LIMIT $1`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var details []byte
		var target uuid.NullUUID
		var outcome sql.NullString
		var status sql.NullInt32
		var fin sql.NullTime
		if err := rows.Scan(&r.ID, &r.At, &r.Actor, &r.Action, &r.TargetType, &r.TargetID, &target, &details, &r.IP, &outcome, &status, &fin); err != nil {
			return nil, 0, err
		}
		if target.Valid {
			t := target.UUID
			r.TargetUser = &t
		}
		_ = json.Unmarshal(details, &r.Details)
		if outcome.Valid {
			o := outcome.String
			r.Outcome = &o
		}
		if status.Valid {
			st := int(status.Int32)
			r.Status = &st
		}
		if fin.Valid {
			t := fin.Time
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next int64
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// Purge deletes rows older than the retention window.
func (s *Store) Purge(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM admin_audit WHERE at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(Retention.Seconds())))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
