package plugreminder

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// HomeFunc reads the account's saved home place.
type HomeFunc func(ctx context.Context, uid uuid.UUID) (lat, lon float64, ok bool, err error)

type pgSource struct {
	db   *sql.DB
	home HomeFunc
	skip uuid.UUID // the demo account: never evaluated
}

// NewSource reads from Postgres. skip is excluded from the candidates (the demo
// user); home reads the settings.
func NewSource(db *sql.DB, home HomeFunc, skip uuid.UUID) Source {
	return &pgSource{db: db, home: home, skip: skip}
}

func (s *pgSource) Candidates(ctx context.Context) ([]Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.user_id, p.timezone, p.plug_threshold_pct,
		       EXTRACT(HOUR FROM p.plug_remind_at)::int * 60 + EXTRACT(MINUTE FROM p.plug_remind_at)::int
		  FROM engagement_prefs p
		  JOIN users u ON u.id = p.user_id AND NOT u.disabled
		 WHERE p.timezone IS NOT NULL AND p.timezone <> '' AND p.user_id <> $1
		   AND EXISTS (SELECT 1 FROM push_subscriptions s WHERE s.user_id = p.user_id AND s.on_plug_in_reminder)`, s.skip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.User, &c.Timezone, &c.ThresholdPct, &c.RemindAtMin); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *pgSource) Home(ctx context.Context, uid uuid.UUID) (float64, float64, bool, error) {
	return s.home(ctx, uid)
}

// pluggedSQL is true for a row where the cable is connected: a connected
// charger status, or any charging_* session state.
const pluggedSQL = `(vs.charger_status LIKE 'chrgr\_sts\_connected%' OR vs.charging_state LIKE 'charging\_%')`

func (s *pgSource) Latest(ctx context.Context, uid uuid.UUID, since time.Time) ([]Snap, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT ON (vs.vehicle_id) v.rivian_vehicle_id, vs.at, COALESCE(vs.battery_level_pct, 0),
		       COALESCE(vs.lat, 0), COALESCE(vs.lon, 0), COALESCE(vs.shift_state, ''), COALESCE(vs.speed_mph, 0),
		       `+pluggedSQL+`
		  FROM vehicle_state vs JOIN vehicles v ON v.id = vs.vehicle_id
		 WHERE vs.user_id = $1 AND v.user_id = $1 AND vs.at > $2::timestamptz
		   AND v.rivian_vehicle_id NOT LIKE 'electrafi-%'
		 ORDER BY vs.vehicle_id, vs.at DESC`, uid, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snap
	for rows.Next() {
		var sn Snap
		if err := rows.Scan(&sn.Vehicle, &sn.At, &sn.SoCPct, &sn.Lat, &sn.Lon, &sn.Gear, &sn.SpeedMph, &sn.Plugged); err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

func (s *pgSource) RecentlyPlugged(ctx context.Context, uid uuid.UUID, homeLat, homeLon float64, since time.Time) (bool, error) {
	var plugged bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM vehicle_state vs WHERE vs.user_id = $1 AND vs.at > $2::timestamptz AND `+pluggedSQL+`)`,
		uid, since.UTC()).Scan(&plugged)
	if err != nil || plugged {
		return plugged, err
	}
	// A charge that started at home in the window (a short one, between samples).
	var started bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM charges c
		   WHERE c.user_id = $1 AND c.started_at > $2::timestamptz
		     AND c.lat IS NOT NULL AND c.lon IS NOT NULL
		     AND abs(c.lat - $3) < 0.002 AND abs(c.lon - $4) < 0.002)`, uid, since.UTC(), homeLat, homeLon).Scan(&started)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return started, err
}
