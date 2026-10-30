package milestones

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/fence"
)

type pgStore struct{ db *sql.DB }

// NewStore returns the Postgres store.
func NewStore(db *sql.DB) *PGStore { return &PGStore{pgStore{db: db}} }

// PGStore is the Postgres store; it also serves the card API.
type PGStore struct{ pgStore }

var _ Store = (*PGStore)(nil)

func (s *pgStore) Ready(ctx context.Context) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM milestones_backfill WHERE id = 1`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *pgStore) MarkReady(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO milestones_backfill (id) VALUES (1) ON CONFLICT DO NOTHING`)
	return err
}

func (s *pgStore) Insert(ctx context.Context, r Row) (bool, error) {
	detail, err := json.Marshal(r.Detail)
	if err != nil {
		return false, err
	}
	// The owner's insert carries the group lease (fence.Exec; plain without a token): a zombie's first
	// insert would flip `inserted` and cost the owner its notification. CatchUp and Backfill carry no token.
	n, err := fence.Exec(ctx, s.db, `
		INSERT INTO milestones (user_id, vehicle_id, key, status, achieved_at, detail)
		SELECT $1::uuid, $2::uuid, $3::text, $4::text, $5::timestamptz, $6::jsonb
		 WHERE `+fence.OK+`
		ON CONFLICT (user_id, vehicle_id, key) DO NOTHING`,
		r.User, r.Vehicle, r.Key, r.Status, r.AchievedAt.UTC(), string(detail))
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *pgStore) Vehicle(ctx context.Context, uid uuid.UUID, rivianID string) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2 LIMIT 1`, uid, rivianID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return id, err == nil, err
}

func (s *pgStore) Vehicles(ctx context.Context, active time.Time) ([]VehicleRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.user_id, v.id, v.rivian_vehicle_id
		  FROM vehicles v JOIN users u ON u.id = v.user_id AND NOT u.disabled
		 WHERE v.rivian_vehicle_id <> '' AND v.rivian_vehicle_id NOT LIKE 'electrafi-%'
		   AND ($1::timestamptz IS NULL
		        OR EXISTS (SELECT 1 FROM drives d WHERE d.vehicle_id = v.id AND d.ended_at > $1::timestamptz)
		        OR EXISTS (SELECT 1 FROM charges c WHERE c.vehicle_id = v.id AND c.ended_at > $1::timestamptz))`,
		nullTime(active))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VehicleRef
	for rows.Next() {
		var v VehicleRef
		if err := rows.Scan(&v.User, &v.ID, &v.RivianID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

// PendingPush: see Store. The claim key is the one the push is sent under
// (milestone:<key>:<vehicle id>), so "pushed" is read from engagement_sends and
// nothing new is stored. Pending means no engagement_sends row at all: engage
// leaves no row for a refused send, and never re-sends a claimed key (sent,
// failed or stuck), so a failed push must not hold the account's one re-offer
// slot.
func (s *pgStore) PendingPush(ctx context.Context, since time.Time) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.user_id, m.vehicle_id, v.rivian_vehicle_id, m.key, m.achieved_at, m.detail
		  FROM milestones m
		  JOIN vehicles v ON v.id = m.vehicle_id
		  JOIN users u ON u.id = m.user_id AND NOT u.disabled
		 WHERE m.status = 'achieved' AND m.dismissed_at IS NULL AND m.achieved_at > $1::timestamptz
		   AND NOT EXISTS (
		       SELECT 1 FROM engagement_sends e
		        WHERE e.user_id = m.user_id AND e.kind = 'milestone'
		          AND e.key = 'milestone:' || m.key || ':' || m.vehicle_id::text)
		 ORDER BY m.achieved_at`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r := Row{Status: StatusAchieved}
		var detail []byte
		if err := rows.Scan(&r.User, &r.Vehicle, &r.RivianID, &r.Key, &r.AchievedAt, &detail); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(detail, &r.Detail)
		r.Kind = kindOfKey(r.Key)
		out = append(out, r)
	}
	return out, rows.Err()
}

// List returns the account's milestone cards: achieved since the cutoff and not
// dismissed, newest first.
func (s *PGStore) List(ctx context.Context, uid uuid.UUID, since time.Time) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.vehicle_id, v.rivian_vehicle_id, m.key, m.status, m.achieved_at, m.detail, m.dismissed_at
		  FROM milestones m JOIN vehicles v ON v.id = m.vehicle_id
		 WHERE m.user_id = $1 AND m.status = 'achieved' AND m.dismissed_at IS NULL AND m.achieved_at > $2::timestamptz
		 ORDER BY m.achieved_at DESC`, uid, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r := Row{User: uid}
		var detail []byte
		var dismissed sql.NullTime
		if err := rows.Scan(&r.Vehicle, &r.RivianID, &r.Key, &r.Status, &r.AchievedAt, &detail, &dismissed); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(detail, &r.Detail)
		r.Kind = kindOfKey(r.Key)
		if dismissed.Valid {
			t := dismissed.Time
			r.DismissedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Dismiss hides a card; false when there is no such card for the account.
func (s *PGStore) Dismiss(ctx context.Context, uid uuid.UUID, rivianID, key string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE milestones m SET dismissed_at = COALESCE(m.dismissed_at, $4::timestamptz)
		  FROM vehicles v
		 WHERE m.user_id = $1 AND v.id = m.vehicle_id AND v.user_id = $1 AND v.rivian_vehicle_id = $2
		   AND m.key = $3 AND m.status = 'achieved'`, uid, rivianID, key, now.UTC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func kindOfKey(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i]
		}
	}
	return key
}
