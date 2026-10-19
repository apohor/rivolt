package samples

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/archive/s3"
)

// ArchiveRows is what the backup reads the telemetry archive through
// (archive.Reader).
type ArchiveRows interface {
	Objects(ctx context.Context, uid uuid.UUID) ([]archive.ObjectRef, error)
	EachRow(ctx context.Context, uid, vehicleID uuid.UUID, month time.Time, fn func(archive.Row) error) error
}

// EachAllArchived is EachAll with full detail for thinned months (N19): a
// month thinning has reduced is read from the user's archive objects, plus
// any of the month's rows in Postgres an object lacks. A thinned month with a
// vehicle that has no readable object falls back to the reduced rows in
// Postgres and is returned in reduced ("2026-05"), for the backup to say so.
// Months go oldest first; inside a thinned month, vehicle by vehicle, each by
// at. With src nil it is EachAll.
//
// An archive object that fails after some of its rows were passed to fn
// (corrupt, truncated) is an error: the caller must fail the whole backup,
// not end it as if complete.
func (s *Store) EachAllArchived(ctx context.Context, src ArchiveRows, fn func(Sample) error) (reduced []string, err error) {
	if src == nil {
		return nil, s.EachAll(ctx, fn)
	}
	thinned := map[time.Time]bool{}
	if err := eachTime(ctx, s.db, `SELECT month FROM sample_tiers`, nil, func(m time.Time) { thinned[monthOf(m)] = true }); err != nil {
		return nil, err
	}
	objs, err := src.Objects(ctx, s.userID)
	if err != nil {
		return nil, err
	}
	byMonth := map[time.Time][]archive.ObjectRef{}
	for _, o := range objs {
		byMonth[monthOf(o.Month)] = append(byMonth[monthOf(o.Month)], o)
	}
	rivianID := map[uuid.UUID]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT id, rivian_vehicle_id FROM vehicles WHERE user_id = $1`, s.userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		var rid string
		if err := rows.Scan(&id, &rid); err != nil {
			rows.Close()
			return nil, err
		}
		rivianID[id] = rid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Every month that can hold the user's samples: the partitions, oldest
	// first, plus any month only the archive still has.
	months := map[time.Time]bool{}
	if err := eachName(ctx, s.db, `
		SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		 WHERE i.inhparent = 'vehicle_state'::regclass`, func(name string) {
		if m, ok := partitionMonth(name); ok {
			months[m] = true
		}
	}); err != nil {
		return nil, err
	}
	for m := range byMonth {
		months[m] = true
	}
	ordered := make([]time.Time, 0, len(months))
	for m := range months {
		ordered = append(ordered, m)
	}
	sortTimes(ordered)

	for _, m := range ordered {
		from, to := m, m.AddDate(0, 1, 0)
		if !thinned[m] {
			if err := s.eachRange(ctx, from, to, uuid.Nil, nil, fn); err != nil {
				return reduced, err
			}
			continue
		}
		covered := map[uuid.UUID]bool{}
		for _, o := range byMonth[m] {
			rid, ok := rivianID[o.VehicleID]
			if !ok {
				continue // the vehicle is gone, and its samples with it
			}
			seen := map[int64]bool{}
			passed := 0
			err := src.EachRow(ctx, s.userID, o.VehicleID, m, func(r archive.Row) error {
				v, err := sampleFromArchive(r, rid)
				if err != nil {
					return err
				}
				seen[v.At.UnixMicro()] = true
				passed++
				return fn(v)
			})
			if errors.Is(err, s3.ErrNotFound) && passed == 0 {
				continue // falls back to Postgres below
			}
			if err != nil {
				return reduced, fmt.Errorf("archive %s: %w", archive.MonthString(m), err)
			}
			covered[o.VehicleID] = true
			// Rows that reached the month after it was archived.
			if err := s.eachRange(ctx, from, to, o.VehicleID, seen, fn); err != nil {
				return reduced, err
			}
		}
		// Vehicles of a thinned month without a readable object: what
		// Postgres has, which is the reduced copy.
		var short bool
		if err := s.eachRange(ctx, from, to, uuid.Nil, nil, func(v Sample) error {
			short = true
			return fn(v)
		}, covered); err != nil {
			return reduced, err
		}
		if short {
			reduced = append(reduced, archive.MonthString(m))
		}
	}
	return reduced, nil
}

// eachRange streams the user's samples in [from, to) by at, as EachAll does.
// With vehicle set, only that vehicle's, skipping the ats in seen. With skip
// set (variadic, at most one), every vehicle but those.
func (s *Store) eachRange(ctx context.Context, from, to time.Time, vehicle uuid.UUID, seen map[int64]bool, fn func(Sample) error, skip ...map[uuid.UUID]bool) error {
	q := `
		SELECT vs.vehicle_id, v.rivian_vehicle_id, vs.at,
		       COALESCE(vs.battery_level_pct,0), COALESCE(vs.range_mi,0), COALESCE(vs.odometer_mi,0),
		       COALESCE(vs.lat,0), COALESCE(vs.lon,0), vs.location_fix_at,
		       COALESCE(vs.speed_mph,0),
		       COALESCE(vs.shift_state,''), COALESCE(vs.drive_mode,''), COALESCE(vs.charging_state,''),
		       COALESCE(vs.charger_power_kw,0), COALESCE(vs.charge_limit_pct,0),
		       COALESCE(vs.inside_temp_c,0), COALESCE(vs.outside_temp_c,0),
		       COALESCE(vs.drive_number,0), COALESCE(vs.charge_number,0), vs.source,
		       vs.altitude_m, vs.tire_pressure_min_bar, vs.pack_temp_avg_c, vs.pack_temp_max_c, vs.pack_temp_min_c
		FROM vehicle_state vs
		JOIN vehicles v ON v.id = vs.vehicle_id
		WHERE vs.user_id = $1 AND vs.at >= $2 AND vs.at < $3`
	args := []any{s.userID, from, to}
	if vehicle != uuid.Nil {
		q += ` AND vs.vehicle_id = $4`
		args = append(args, vehicle)
	}
	q += ` ORDER BY vs.vehicle_id, vs.at`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var vid uuid.UUID
		var v Sample
		var fixAt sql.NullTime
		var alt, tirePsi, pAvg, pMax, pMin sql.NullFloat64
		if err := rows.Scan(&vid, &v.VehicleID, &v.At,
			&v.BatteryLevelPct, &v.RangeMi, &v.OdometerMi,
			&v.Lat, &v.Lon, &fixAt,
			&v.SpeedMph, &v.ShiftState, &v.DriveMode, &v.ChargingState,
			&v.ChargerPowerKW, &v.ChargeLimitPct,
			&v.InsideTempC, &v.OutsideTempC,
			&v.DriveNumber, &v.ChargeNumber, &v.Source,
			&alt, &tirePsi, &pAvg, &pMax, &pMin,
		); err != nil {
			return err
		}
		if len(skip) > 0 && skip[0][vid] {
			continue
		}
		v.At = v.At.UTC()
		if seen != nil && seen[v.At.UnixMicro()] {
			continue
		}
		if fixAt.Valid {
			t := fixAt.Time.UTC()
			v.LocationFixAt = &t
		}
		v.AltitudeM, v.TirePressureMinBar = nullPtr(alt), nullPtr(tirePsi)
		v.PackTempAvgC, v.PackTempMaxC, v.PackTempMinC = nullPtr(pAvg), nullPtr(pMax), nullPtr(pMin)
		if err := fn(v); err != nil {
			return err
		}
	}
	return rows.Err()
}

func nullPtr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	f := n.Float64
	return &f
}

// sampleFromArchive maps an archived row to a Sample exactly as EachAll maps
// a database row: NULL numbers and strings become 0 and "", the nullable
// readings stay nil.
func sampleFromArchive(r archive.Row, rivianID string) (Sample, error) {
	var v Sample
	var err error
	str := func(col string) string { s, _ := r.Get(col); return s }
	num := func(col string) float64 {
		s := str(col)
		if s == "" || err != nil {
			return 0
		}
		f, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			err = fmt.Errorf("archive row: %s=%q: %w", col, s, perr)
		}
		return f
	}
	opt := func(col string) *float64 {
		if str(col) == "" {
			return nil
		}
		f := num(col)
		return &f
	}
	ts := func(col string) (time.Time, bool) {
		s := str(col)
		if s == "" {
			return time.Time{}, false
		}
		t, perr := time.Parse(archive.TimeLayout, s)
		if perr != nil && err == nil {
			err = fmt.Errorf("archive row: %s=%q: %w", col, s, perr)
		}
		return t.UTC(), true
	}
	v.VehicleID = rivianID
	at, ok := ts("at")
	if !ok && err == nil {
		err = errors.New("archive row: no at")
	}
	v.At = at
	if t, ok := ts("location_fix_at"); ok {
		v.LocationFixAt = &t
	}
	v.BatteryLevelPct, v.RangeMi, v.OdometerMi = num("battery_level_pct"), num("range_mi"), num("odometer_mi")
	v.Lat, v.Lon, v.SpeedMph = num("lat"), num("lon"), num("speed_mph")
	v.ShiftState, v.DriveMode, v.ChargingState = str("shift_state"), str("drive_mode"), str("charging_state")
	v.ChargerPowerKW, v.ChargeLimitPct = num("charger_power_kw"), num("charge_limit_pct")
	v.InsideTempC, v.OutsideTempC = num("inside_temp_c"), num("outside_temp_c")
	v.DriveNumber, v.ChargeNumber = int64(num("drive_number")), int64(num("charge_number"))
	v.Source = str("source")
	v.AltitudeM, v.TirePressureMinBar = opt("altitude_m"), opt("tire_pressure_min_bar")
	v.PackTempAvgC, v.PackTempMaxC, v.PackTempMinC = opt("pack_temp_avg_c"), opt("pack_temp_max_c"), opt("pack_temp_min_c")
	return v, err
}

func monthOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func sortTimes(ts []time.Time) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j].Before(ts[j-1]); j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

func eachTime(ctx context.Context, db *sql.DB, q string, args []any, fn func(time.Time)) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return err
		}
		fn(t)
	}
	return rows.Err()
}

func eachName(ctx context.Context, db *sql.DB, q string, fn func(string)) error {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		fn(n)
	}
	return rows.Err()
}
