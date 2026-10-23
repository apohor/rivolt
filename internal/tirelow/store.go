package tirelow

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// LatestFunc reads a vehicle's newest state from the monitor this pod runs (nil state: not this pod's).
type LatestFunc func(uid uuid.UUID, rivianID string) (*rivian.State, time.Time)

type pgSource struct {
	db     *sql.DB
	latest LatestFunc
	skip   uuid.UUID // the demo account: never evaluated
}

// NewSource reads candidates and records markers in Postgres, and states from latest. skip is excluded (the demo user).
func NewSource(db *sql.DB, latest LatestFunc, skip uuid.UUID) Source {
	return &pgSource{db: db, latest: latest, skip: skip}
}

func (s *pgSource) Vehicles(ctx context.Context) ([]Vehicle, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.user_id, v.id, v.rivian_vehicle_id, COALESCE(v.model, '')
		  FROM vehicles v
		  JOIN users u ON u.id = v.user_id AND NOT u.disabled
		 WHERE v.user_id <> $1
		   AND v.rivian_vehicle_id <> ''
		   AND (EXISTS (SELECT 1 FROM push_subscriptions p WHERE p.user_id = v.user_id AND p.on_tire_low)
		     OR EXISTS (SELECT 1 FROM push_apns_devices d WHERE d.user_id = v.user_id AND d.on_tire_low))`, s.skip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Vehicle
	for rows.Next() {
		var v Vehicle
		if err := rows.Scan(&v.UserID, &v.VehicleID, &v.RivianID, &v.Model); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *pgSource) Latest(uid uuid.UUID, rivianID string) (*rivian.State, time.Time) {
	return s.latest(uid, rivianID)
}

func (s *pgSource) Claim(ctx context.Context, uid, vehicleID uuid.UUID, newest time.Time, wheels []string) (bool, error) {
	// One alert per (vehicle, newest-wheel time), and none while a recent alert already named every one of these wheels
	// (a pod change restarts the in-memory clocks). A wheel that no recent alert named gets its own.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO tire_low_alerts (user_id, vehicle_id, episode_started_at, wheels)
		SELECT $1, $2, $3, $4::text
		 WHERE NOT EXISTS (SELECT 1 FROM tire_low_alerts
		                    WHERE vehicle_id = $2 AND sent_at > now() - make_interval(secs => $5)
		                      AND string_to_array(wheels, ',') @> string_to_array($4::text, ','))
		ON CONFLICT (vehicle_id, episode_started_at) DO NOTHING`,
		uid, vehicleID, newest.UTC(), strings.Join(wheels, ","), Cooldown.Seconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *pgSource) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tire_low_alerts WHERE sent_at < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
