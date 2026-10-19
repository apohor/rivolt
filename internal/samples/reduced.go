package samples

import (
	"context"
	"time"
)

// ReducedMonths lists, oldest first, the UTC months in [from, to] whose raw
// telemetry for one vehicle (Rivian vehicle id) is shown from reduced samples:
// the month has been thinned (a sample_tiers row) and the user's vehicle-month is
// not currently held at full detail (archive_months.rethin_after in the future,
// set by an admin restore). One small query on those two tables; it reads no
// vehicle_state rows. An id the user doesn't own, or an empty one, yields none.
func (s *Store) ReducedMonths(ctx context.Context, rivianVehicleID string, from, to time.Time) ([]time.Time, error) {
	if rivianVehicleID == "" {
		return nil, nil
	}
	if to.Before(from) {
		from, to = to, from
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.month
		  FROM sample_tiers t
		  JOIN vehicles v ON v.user_id = $1 AND v.rivian_vehicle_id = $2
		 WHERE t.month BETWEEN date_trunc('month', $3::timestamptz AT TIME ZONE 'UTC')::date
		                   AND date_trunc('month', $4::timestamptz AT TIME ZONE 'UTC')::date
		   AND NOT EXISTS (SELECT 1 FROM archive_months a
		                    WHERE a.user_id = $1 AND a.vehicle_id = v.id AND a.month = t.month
		                      AND a.rethin_after > now())
		 ORDER BY t.month`, s.userID, rivianVehicleID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var m time.Time
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, monthOf(m))
	}
	return out, rows.Err()
}
