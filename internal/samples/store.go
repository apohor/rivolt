// Package samples persists raw vehicle_state polling snapshots.
package samples

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/db"
)

// Sample is a single polling snapshot. VehicleID is the Rivian gateway
// string id ("01-000000000"); the store resolves it to the internal
// vehicles.id UUID on write.
type Sample struct {
	VehicleID       string
	At              time.Time
	BatteryLevelPct float64
	RangeMi         float64
	OdometerMi      float64
	Lat, Lon        float64
	// LocationFixAt is the timestamp Rivian's GNSS module reported on
	// the (Lat, Lon) fix itself, NOT the wall-clock time the recorder
	// observed it. Differs from At when the modem loses sky view and
	// keeps replaying the last known fix — see migration 0013 for
	// the field rationale and the Fort Stockton incident that
	// motivated it. Pointer (rather than time.Time + omitempty)
	// because Go's encoding/json doesn't honor omitempty on struct
	// types: a zero time.Time would serialize as "0001-01-01T00:00:00Z"
	// and the frontend would compute a 2000-year fix age. nil
	// renders as "fix age unknown" upstream.
	LocationFixAt *time.Time `json:",omitempty"`
	SpeedMph      float64
	ShiftState    string
	DriveMode     string `json:"drive_mode,omitempty"` // "everyday", "sport", "all-terrain", etc.; nullable in DB for legacy samples
	ChargingState string
	// PowerState is the car's power/sleep state ("sleep" | "ready" |
	// "standby" | "go"). Empty on legacy rows / imports (NULL in DB).
	// Feeds the sleep/activity aggregation.
	PowerState     string `json:"power_state,omitempty"`
	ChargerPowerKW float64
	// ChargerStatus is Rivian's chargerStatus, the plug indicator
	// (migration 0053); "" when not reported.
	ChargerStatus  string `json:"charger_status,omitempty"`
	ChargeLimitPct float64
	InsideTempC    float64
	OutsideTempC   float64
	DriveNumber    int64
	ChargeNumber   int64
	Source         string // "live" | "electrafi_import"
	// AltitudeM is the elevation above sea level (meters) at
	// (Lat, Lon), looked up by the live recorder against the
	// Mapzen Terrarium DEM at sample-write time. Pointer because
	// it's nullable: NULL on legacy rows written before migration
	// 0019, on samples without a GPS fix, on ElectraFi imports,
	// and on cold-cache misses where the tile fetch was kicked
	// off async (the next sample on that tile will hit the cache).
	AltitudeM *float64 `json:"altitude_m,omitempty"`
	// TirePressureMinBar is the minimum of the four corner pressures
	// at sample time, in bar. Pointer because nullable: NULL on
	// legacy rows pre-migration 0024, ElectraFi imports, and live
	// rows where Rivian reported zero on all four corners (sensor
	// outage / vehicle parked w/ no recent reading). The efficiency
	// analyzer pulls the median across a drive's samples; min-of-4
	// keeps the worst tire (the one that actually hurts efficiency)
	// dominant without bloating the schema with all four corners.
	TirePressureMinBar *float64 `json:"tire_pressure_min_bar,omitempty"`
	// PackTempAvg/Max/MinC are high-voltage battery cell temperatures
	// (°C), merged in from the Parallax energy.high_voltage.battery_state
	// topic. Pointers because nullable: NULL on legacy rows pre-migration
	// 0036, imports, and live rows recorded before the vehicle pushed a
	// battery_state frame. Avg/max/min kept separately so the spread
	// (thermal imbalance) is queryable.
	PackTempAvgC *float64 `json:"pack_temp_avg_c,omitempty"`
	PackTempMaxC *float64 `json:"pack_temp_max_c,omitempty"`
	PackTempMinC *float64 `json:"pack_temp_min_c,omitempty"`
}

// Store wraps the vehicle_state table, scoped to one user.
type Store struct {
	db       *sql.DB
	userID   uuid.UUID
	vehicles *db.VehicleResolver
}

// OpenStore binds a pooled connection to a user + vehicle resolver.
func OpenStore(d *sql.DB, userID uuid.UUID, v *db.VehicleResolver) (*Store, error) {
	if d == nil {
		return nil, fmt.Errorf("samples: db is nil")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("samples: userID is zero")
	}
	if v == nil {
		return nil, fmt.Errorf("samples: vehicle resolver is nil")
	}
	return &Store{db: d, userID: userID, vehicles: v}, nil
}

// Close is a no-op; the pool is managed by main.
func (s *Store) Close() error { return nil }

// Reset deletes every raw sample (vehicle_state row) for this
// store's user. See drives.Store.Reset.
func (s *Store) Reset(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM vehicle_state WHERE user_id = $1`, s.userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// InsertBatch inserts many samples in a single transaction.
// Duplicate (vehicle_id, at) tuples are ignored so re-imports are
// idempotent. All samples must belong to the same user.
// nullStr maps an empty string to a SQL NULL so a nullable text column
// stays distinguishable from "" (legacy rows read back as NULL, not "").
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) InsertBatch(ctx context.Context, batch []Sample) error {
	if len(batch) == 0 {
		return nil
	}
	uuids, err := s.prepareBatch(ctx, batch)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO vehicle_state (`+sampleColumns+`
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
		ON CONFLICT (vehicle_id, at) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	touched := map[vehicleMonth]bool{}
	for _, v := range batch {
		res, err := stmt.ExecContext(ctx, s.sampleArgs(uuids[v.VehicleID], v)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			touched[vehicleMonth{uuids[v.VehicleID], monthOf(v.At)}] = true
		}
	}
	if err := s.markRearchive(ctx, tx, touched); err != nil {
		return err
	}
	return tx.Commit()
}

// vehicleMonth is a (vehicle, month) a write added rows to.
type vehicleMonth struct {
	vehicle uuid.UUID
	month   time.Time
}

// markRearchive flags the archived months a write added rows to, in the
// write's transaction (N19 A4): the archive job re-archives them, merging
// with the object if the month was thinned. A month without an archive row
// (the current one, for live writes) is a no-op.
func (s *Store) markRearchive(ctx context.Context, tx *sql.Tx, touched map[vehicleMonth]bool) error {
	// A month that ended less than ArchiveAfterDays ago can't have an
	// archive row (the job's own cutoff), so the live recorder's writes skip
	// the UPDATE altogether (backend on #628).
	archivable := nowFn().UTC().AddDate(0, 0, -archive.ArchiveAfterDays)
	for vm := range touched {
		if vm.month.AddDate(0, 1, 0).After(archivable) {
			continue
		}
		if err := archive.MarkRearchive(ctx, tx, s.userID, vm.vehicle, vm.month); err != nil {
			return fmt.Errorf("mark %s for re-archiving: %w", vm.month.Format("2006-01"), err)
		}
	}
	return nil
}

// bulkInsertRows bounds one multi-row INSERT: 500 rows × one param per
// sampleColumns column (27) stays far below Postgres's 65,535
// bind-parameter limit (store_params_test.go).
const bulkInsertRows = 500

// InsertBulk is InsertBatch for large batches: each chunk of up to
// bulkInsertRows samples is one multi-row INSERT (one round trip)
// instead of one EXEC per row. Same columns, same ON CONFLICT DO
// NOTHING, same partition and vehicle handling. Chunks commit
// independently; a failure returns the error and leaves earlier
// chunks written, which is safe because inserts are idempotent.
// Used by replay catch-up; the live recorder keeps InsertBatch.
func (s *Store) InsertBulk(ctx context.Context, batch []Sample) error {
	if len(batch) == 0 {
		return nil
	}
	uuids, err := s.prepareBatch(ctx, batch)
	if err != nil {
		return err
	}
	const nCols = 27
	for start := 0; start < len(batch); start += bulkInsertRows {
		chunk := batch[start:min(start+bulkInsertRows, len(batch))]
		var q strings.Builder
		q.WriteString("INSERT INTO vehicle_state (" + sampleColumns + ") VALUES ")
		args := make([]any, 0, len(chunk)*nCols)
		for i, v := range chunk {
			if i > 0 {
				q.WriteString(",")
			}
			q.WriteString("(")
			for c := 0; c < nCols; c++ {
				if c > 0 {
					q.WriteString(",")
				}
				q.WriteString("$" + strconv.Itoa(i*nCols+c+1))
			}
			q.WriteString(")")
			args = append(args, s.sampleArgs(uuids[v.VehicleID], v)...)
		}
		q.WriteString(" ON CONFLICT (vehicle_id, at) DO NOTHING")
		// One transaction per chunk, so the re-archive flag commits with its
		// rows.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, q.String(), args...)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			touched := map[vehicleMonth]bool{}
			for _, v := range chunk {
				touched[vehicleMonth{uuids[v.VehicleID], monthOf(v.At)}] = true
			}
			if err := s.markRearchive(ctx, tx, touched); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// sampleColumns is the vehicle_state column list both insert paths
// write, in sampleArgs order.
const sampleColumns = `
			user_id, vehicle_id, at,
			battery_level_pct, range_mi, odometer_mi,
			lat, lon, location_fix_at,
			speed_mph, shift_state, drive_mode, charging_state,
			charger_power_kw, charge_limit_pct,
			inside_temp_c, outside_temp_c,
			drive_number, charge_number, source,
			altitude_m, tire_pressure_min_bar,
			pack_temp_avg_c, pack_temp_max_c, pack_temp_min_c,
			power_state, charger_status`

// prepareBatch ensures every month the batch touches has a partition
// and resolves each distinct Rivian id to its vehicles.id.
func (s *Store) prepareBatch(ctx context.Context, batch []Sample) (map[string]uuid.UUID, error) {
	// vehicle_state is RANGE-partitioned by month (migration 0007).
	// The partition janitor only keeps current+future months hot;
	// historical writes (ElectraFi imports, backfills) hit months
	// the janitor never created, which surfaces as
	//   "no partition of relation \"vehicle_state\" found for row".
	// Ensure every distinct month present in the batch before the
	// INSERT runs. The helper is idempotent and bounded — at most
	// (months spanned by batch) calls, typically 1-2 per flush.
	months, err := checkBatch(batch, nowFn())
	if err != nil {
		return nil, err
	}
	for m := range months {
		if _, err := s.db.ExecContext(ctx,
			`SELECT rivolt_ensure_vehicle_state_partition($1)`, m,
		); err != nil {
			return nil, fmt.Errorf("ensure partition for %s: %w", m.Format("2006-01"), err)
		}
	}
	// Resolve every distinct Rivian id outside the tx so a new
	// vehicle row is committed independently of the sample write.
	uuids := make(map[string]uuid.UUID, len(batch))
	for _, v := range batch {
		if _, ok := uuids[v.VehicleID]; ok {
			continue
		}
		id, err := s.vehicles.Resolve(ctx, v.VehicleID)
		if err != nil {
			return nil, err
		}
		uuids[v.VehicleID] = id
	}
	return uuids, nil
}

// sampleArgs returns one sample's bind values in sampleColumns order.
func (s *Store) sampleArgs(vehicleID uuid.UUID, v Sample) []any {
	// Pass NULL for a missing LocationFixAt so legacy/imported rows
	// stay distinguishable from "fix observed at the unix epoch".
	var fixAt any
	if v.LocationFixAt != nil && !v.LocationFixAt.IsZero() {
		fixAt = v.LocationFixAt.UTC()
	}
	var alt any
	if v.AltitudeM != nil {
		alt = *v.AltitudeM
	}
	var tirePsi any
	if v.TirePressureMinBar != nil {
		tirePsi = *v.TirePressureMinBar
	}
	ptr := func(p *float64) any {
		if p != nil {
			return *p
		}
		return nil
	}
	return []any{
		s.userID, vehicleID, v.At.UTC(),
		v.BatteryLevelPct, v.RangeMi, v.OdometerMi,
		v.Lat, v.Lon, fixAt,
		v.SpeedMph, v.ShiftState, v.DriveMode, v.ChargingState,
		v.ChargerPowerKW, v.ChargeLimitPct,
		v.InsideTempC, v.OutsideTempC,
		v.DriveNumber, v.ChargeNumber, v.Source,
		alt, tirePsi,
		ptr(v.PackTempAvgC), ptr(v.PackTempMaxC), ptr(v.PackTempMinC),
		nullStr(v.PowerState), nullStr(v.ChargerStatus),
	}
}

// Count returns the total number of samples for this user.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vehicle_state WHERE user_id = $1`,
		s.userID).Scan(&n)
	return n, err
}

// ListSince returns samples newer than since, up to limit, oldest first.
//
// Limit semantics:
//   - <= 0 falls back to a reasonable default (1000).
//   - Anything above the hard cap is clamped DOWN to the cap, not
//     reset to the default. The previous "if limit > 10000 { limit
//     = 1000 }" branch was a foot-gun: the UI asks for 20k samples
//     so a 90-minute drive (~1.7k rows at the WS cadence) returns
//     fully, but the store silently truncated to 1k → the route
//     polyline ended halfway through the trip and the user reported
//     "no GPS coordinates for the end of the route".
func (s *Store) ListSince(ctx context.Context, since time.Time, limit int) ([]Sample, error) {
	return s.ListBetween(ctx, since, time.Time{}, limit)
}

// ListBetween returns samples in (since, until], oldest first. Zero
// `until` means "no upper bound" (back-compat with ListSince). The
// upper bound matters for the drive detail page: without it, opening
// any drive ages back pulls every sample from the drive's start
// through now, which can be days of charging+parked rows.
func (s *Store) ListBetween(ctx context.Context, since, until time.Time, limit int) ([]Sample, error) {
	return s.ListBetweenVehicle(ctx, "", since, until, limit)
}

// ListBetweenVehicle is ListBetween scoped to one vehicle (Rivian vehicle
// id). An empty vehicleID means every vehicle of the user; an id the user
// doesn't own matches nothing, so the result is empty, never another
// user's rows.
func (s *Store) ListBetweenVehicle(ctx context.Context, vehicleID string, since, until time.Time, limit int) ([]Sample, error) {
	const maxLimit = 50000
	if limit <= 0 {
		limit = 1000
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	var (
		query string
		args  []any
	)
	if until.IsZero() {
		query = `
		SELECT v.rivian_vehicle_id, vs.at,
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
		WHERE vs.user_id = $1 AND vs.at > $2 AND ($4::text = '' OR v.rivian_vehicle_id = $4::text)
		ORDER BY vs.at ASC
		LIMIT $3`
		args = []any{s.userID, since.UTC(), limit, vehicleID}
	} else {
		query = `
		SELECT v.rivian_vehicle_id, vs.at,
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
		WHERE vs.user_id = $1 AND vs.at > $2 AND vs.at <= $3 AND ($5::text = '' OR v.rivian_vehicle_id = $5::text)
		ORDER BY vs.at ASC
		LIMIT $4`
		args = []any{s.userID, since.UTC(), until.UTC(), limit, vehicleID}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSamples(rows)
}

// scanSamples reads rows in the column order shared by ListBetween and
// ListBetweenDownsampled.
func scanSamples(rows *sql.Rows) ([]Sample, error) {
	var out []Sample
	for rows.Next() {
		var v Sample
		var fixAt sql.NullTime
		var alt sql.NullFloat64
		var tirePsi sql.NullFloat64
		var pAvg, pMax, pMin sql.NullFloat64
		if err := rows.Scan(&v.VehicleID, &v.At,
			&v.BatteryLevelPct, &v.RangeMi, &v.OdometerMi,
			&v.Lat, &v.Lon, &fixAt,
			&v.SpeedMph, &v.ShiftState, &v.DriveMode, &v.ChargingState,
			&v.ChargerPowerKW, &v.ChargeLimitPct,
			&v.InsideTempC, &v.OutsideTempC,
			&v.DriveNumber, &v.ChargeNumber, &v.Source,
			&alt, &tirePsi, &pAvg, &pMax, &pMin,
		); err != nil {
			return nil, err
		}
		v.At = v.At.UTC()
		if fixAt.Valid {
			t := fixAt.Time.UTC()
			v.LocationFixAt = &t
		}
		if alt.Valid {
			f := alt.Float64
			v.AltitudeM = &f
		}
		if tirePsi.Valid {
			f := tirePsi.Float64
			v.TirePressureMinBar = &f
		}
		if pAvg.Valid {
			f := pAvg.Float64
			v.PackTempAvgC = &f
		}
		if pMax.Valid {
			f := pMax.Float64
			v.PackTempMaxC = &f
		}
		if pMin.Valid {
			f := pMin.Float64
			v.PackTempMinC = &f
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DayActivity is one calendar day's (UTC) power-state breakdown, in
// hours, derived from the persisted power_state column (migration 0038).
// IdleAwakeH is the interesting one: the car awake while parked and not
// actively charging — i.e. staying awake for no obvious reason, the
// vampire-drain signal. Driving and active charging are excluded (both
// are expected awake time). Only intervals with a recorded power_state
// contribute, so the series fills forward from the deploy.
type DayActivity struct {
	Day        time.Time `json:"day"`
	AsleepH    float64   `json:"asleep_h"`
	IdleAwakeH float64   `json:"idle_awake_h"`
	// Sleeps counts the times the car fell asleep that day;
	// FailedSleeps the ones that ended within a minute. A healthy car
	// sleeps a few times a day for hours; a high failed share means
	// something keeps waking it, which is usually what drives
	// IdleAwakeH up.
	Sleeps       int `json:"sleeps"`
	FailedSleeps int `json:"failed_sleeps"`
}

// SleepActivity attributes each inter-sample interval to the vehicle's
// state at its start and sums per UTC day over [since, until). Intervals
// are capped at 20 min so a data outage never counts as one giant block —
// steady-state cadence stays well under that even while the car sleeps
// (the Rivian cloud replays keep the sample stream dense). vehicleID
// scopes to one car; empty aggregates all of the user's vehicles.
//
// idle_awake = awake (power_state<>'sleep') AND parked (gear not D/R/N)
// AND not actively charging (charging_state not charging_active /
// charging_connecting). An unknown (NULL) gear or charging state counts
// as parked and not charging, as in internal/drain. That isolates unexplained awake time from
// normal driving / charging.
//
// Charging is judged by charging_state alone, not charger_power_kw:
// until the recorder fix alongside this, the stored power kept the last
// session's value on every parked sample (see dropIdleChargerPower in
// internal/rivian), which made every awake hour look like charging and
// flattened idle-awake to zero. charging_state was always right, so
// keying on it corrects the history as well as new rows.
func (s *Store) SleepActivity(ctx context.Context, vehicleID string, since, until time.Time) ([]DayActivity, error) {
	const capSec = 1200 // 20 min
	args := []any{s.userID, since.UTC(), until.UTC(), capSec}
	vfilter := ""
	if vehicleID != "" {
		vfilter = " AND vs.vehicle_id = (SELECT id FROM vehicles WHERE rivian_vehicle_id = $5)"
		args = append(args, vehicleID)
	}
	query := `
	WITH ivl AS (
		SELECT vs.at, vs.power_state, vs.shift_state, vs.charging_state,
		       LEAST(
		         EXTRACT(EPOCH FROM (LEAD(vs.at) OVER (PARTITION BY vs.vehicle_id ORDER BY vs.at) - vs.at)),
		         $4
		       ) AS dur_s
		FROM vehicle_state vs
		WHERE vs.user_id = $1 AND vs.at >= $2 AND vs.at < $3 AND vs.power_state IS NOT NULL` + vfilter + `
	)
	SELECT date_trunc('day', at) AS day,
	       COALESCE(SUM(dur_s) FILTER (WHERE power_state = 'sleep'), 0)/3600.0 AS asleep_h,
	       COALESCE(SUM(dur_s) FILTER (
	         WHERE power_state <> 'sleep'
	           AND COALESCE(shift_state, '') NOT IN ('D','R','N')
	           AND COALESCE(charging_state, '') NOT IN ('charging_active','charging_connecting')
	       ), 0)/3600.0 AS idle_awake_h
	FROM ivl
	WHERE dur_s IS NOT NULL
	GROUP BY 1 ORDER BY 1`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayActivity
	byDay := map[time.Time]int{}
	for rows.Next() {
		var d DayActivity
		if err := rows.Scan(&d.Day, &d.AsleepH, &d.IdleAwakeH); err != nil {
			return nil, err
		}
		byDay[d.Day.UTC()] = len(out)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.countSleeps(ctx, vehicleID, since, until, byDay, out); err != nil {
		return nil, err
	}
	return out, nil
}

// countSleeps fills Sleeps / FailedSleeps on the SleepActivity days.
// A sleep starts at the first 'sleep' sample after a non-sleep one and
// ends at the next non-sleep sample; failed means it lasted under 60 s
// (the threshold the monitor's sleep gate logs with). A sleep still in
// progress at the end of the window isn't counted.
func (s *Store) countSleeps(ctx context.Context, vehicleID string, since, until time.Time, byDay map[time.Time]int, out []DayActivity) error {
	args := []any{s.userID, since.UTC(), until.UTC()}
	vfilter := ""
	if vehicleID != "" {
		vfilter = " AND vs.vehicle_id = (SELECT id FROM vehicles WHERE rivian_vehicle_id = $4)"
		args = append(args, vehicleID)
	}
	query := `
	WITH s AS (
		SELECT vs.vehicle_id, vs.at, vs.power_state,
		       LAG(vs.power_state) OVER (PARTITION BY vs.vehicle_id ORDER BY vs.at) AS prev_ps
		FROM vehicle_state vs
		WHERE vs.user_id = $1 AND vs.at >= $2 AND vs.at < $3 AND vs.power_state IS NOT NULL` + vfilter + `
	), changes AS (
		SELECT vehicle_id, at, power_state,
		       LEAD(at) OVER (PARTITION BY vehicle_id ORDER BY at) AS next_change_at
		FROM s WHERE power_state IS DISTINCT FROM prev_ps
	)
	SELECT date_trunc('day', at) AS day,
	       COUNT(*) AS sleeps,
	       COUNT(*) FILTER (WHERE next_change_at - at < interval '60 seconds') AS failed
	FROM changes
	WHERE power_state = 'sleep' AND next_change_at IS NOT NULL
	GROUP BY 1`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var sleeps, failed int
		if err := rows.Scan(&day, &sleeps, &failed); err != nil {
			return err
		}
		if i, ok := byDay[day.UTC()]; ok {
			out[i].Sleeps, out[i].FailedSleeps = sleeps, failed
		}
	}
	return rows.Err()
}

// ListAll returns every sample for this user, oldest first. Used by
// the Settings → Backup endpoint. Unbounded — a year of 60 s polls
// is on the order of 500k rows, ~100 MB JSON; acceptable for a
// full backup but NOT suitable for UI consumption.
func (s *Store) EachAll(ctx context.Context, fn func(Sample) error) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.rivian_vehicle_id, vs.at,
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
		WHERE vs.user_id = $1
		ORDER BY vs.at ASC`, s.userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v Sample
		var fixAt sql.NullTime
		var alt sql.NullFloat64
		var tirePsi sql.NullFloat64
		var pAvg, pMax, pMin sql.NullFloat64
		if err := rows.Scan(&v.VehicleID, &v.At,
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
		v.At = v.At.UTC()
		if fixAt.Valid {
			t := fixAt.Time.UTC()
			v.LocationFixAt = &t
		}
		if alt.Valid {
			f := alt.Float64
			v.AltitudeM = &f
		}
		if tirePsi.Valid {
			f := tirePsi.Float64
			v.TirePressureMinBar = &f
		}
		if pAvg.Valid {
			f := pAvg.Float64
			v.PackTempAvgC = &f
		}
		if pMax.Valid {
			f := pMax.Float64
			v.PackTempMaxC = &f
		}
		if pMin.Valid {
			f := pMin.Float64
			v.PackTempMinC = &f
		}
		if err := fn(v); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ListAll returns every row; use EachAll for large result sets.
func (s *Store) ListAll(ctx context.Context) ([]Sample, error) {
	var out []Sample
	err := s.EachAll(ctx, func(v Sample) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

// Downsampling: a window with more rows than the cap used to return the
// OLDEST cap rows, silently cutting the end of long charge curves. It
// now returns an evenly bucketed series spanning the whole window, and
// keeps the extremes of each bucket so peaks survive.
//
// Per time bucket the kept rows are: first, last, max and min
// charger_power_kw, max and min battery_level_pct, max speed_mph (at most
// 7 distinct rows; the bucket count is limit/7, so the result never
// exceeds limit).
const downsampleKeepPerBucket = 7

// Downsampled describes the result of ListBetweenDownsampled.
type Downsampled struct {
	Samples []Sample
	// Reduced is true when the window held more than limit rows and the
	// series is a bucketed reduction of it.
	Reduced bool
	// Total is how many rows the window holds. Only known for a window that was
	// counted (bounded, within MaxDownsampleSpan); otherwise it is len(Samples).
	Total int
	// Truncated is true when the window was NOT bucketed (unbounded, or wider than
	// MaxDownsampleSpan) and the result reached the cap: the rows are the OLDEST
	// `limit` and the tail may be missing. It can be a false alarm when the window
	// holds exactly `limit` rows.
	Truncated bool
}

// MaxDownsampleSpan bounds the window that is bucketed. The count and the
// bucketing scan every row in the window, so a caller-controlled span
// (anonymous demo callers included) must stay bounded; wider windows,
// and windows with no upper bound, keep the old cheap behaviour (the
// oldest `limit` rows, LIMIT stops the scan early).
const MaxDownsampleSpan = 7 * 24 * time.Hour

// ListBetweenDownsampled is ListBetween with the cap handled by
// bucketing instead of truncation. At or under the cap it returns every
// row exactly as ListBetween does. Without an upper bound, or over
// MaxDownsampleSpan, it IS ListBetween (never flagged Reduced).
func (s *Store) ListBetweenDownsampled(ctx context.Context, since, until time.Time, limit int) (Downsampled, error) {
	return s.ListBetweenDownsampledVehicle(ctx, "", since, until, limit)
}

// ListBetweenDownsampledVehicle is ListBetweenDownsampled scoped to one
// vehicle (Rivian vehicle id); empty means every vehicle of the user. The
// total and the bucketing count only that vehicle's rows.
func (s *Store) ListBetweenDownsampledVehicle(ctx context.Context, vehicleID string, since, until time.Time, limit int) (Downsampled, error) {
	const maxLimit = 50000
	if limit <= 0 {
		limit = 1000
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if until.IsZero() || until.Sub(since) > MaxDownsampleSpan {
		out, err := s.ListBetweenVehicle(ctx, vehicleID, since, until, limit)
		return Downsampled{Samples: out, Total: len(out), Truncated: len(out) >= limit}, err
	}
	upper := until.UTC()
	var total, vehicles int
	var first, last sql.NullTime
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*), count(DISTINCT vs.vehicle_id), min(vs.at), max(vs.at)
		  FROM vehicle_state vs
		  JOIN vehicles v ON v.id = vs.vehicle_id
		 WHERE vs.user_id = $1 AND vs.at > $2 AND vs.at <= $3
		   AND ($4::text = '' OR v.rivian_vehicle_id = $4::text)`,
		s.userID, since.UTC(), upper, vehicleID).Scan(&total, &vehicles, &first, &last); err != nil {
		return Downsampled{}, err
	}
	if total <= limit || !first.Valid {
		out, err := s.ListBetweenVehicle(ctx, vehicleID, since, until, limit)
		return Downsampled{Samples: out, Total: total}, err
	}
	if vehicles < 1 {
		vehicles = 1
	}
	// Buckets are per vehicle, so one truck's extremes never crowd out
	// another's; the bucket count shrinks to keep the cap.
	buckets := limit / (downsampleKeepPerBucket * vehicles)
	if buckets < 1 {
		buckets = 1
	}
	spanSec := last.Time.Sub(first.Time).Seconds()
	width := spanSec / float64(buckets)
	if width < 0.001 {
		width = 0.001
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH w AS (
		    SELECT vs.*, v.rivian_vehicle_id AS rvid,
		           LEAST(floor(EXTRACT(EPOCH FROM (vs.at - $4::timestamptz)) / $5::float8)::int, $6::int - 1) AS bk
		      FROM vehicle_state vs
		      JOIN vehicles v ON v.id = vs.vehicle_id
		     WHERE vs.user_id = $1 AND vs.at > $2 AND vs.at <= $3
		       AND ($8::text = '' OR v.rivian_vehicle_id = $8::text)
		), r AS (
		    SELECT w.*,
		           row_number() OVER (PARTITION BY vehicle_id, bk ORDER BY at ASC)  AS r_first,
		           row_number() OVER (PARTITION BY vehicle_id, bk ORDER BY at DESC) AS r_last,
		           row_number() OVER (PARTITION BY vehicle_id, bk ORDER BY charger_power_kw DESC NULLS LAST, at) AS r_pmax,
		           row_number() OVER (PARTITION BY vehicle_id, bk ORDER BY charger_power_kw ASC NULLS LAST, at)  AS r_pmin,
		           row_number() OVER (PARTITION BY vehicle_id, bk ORDER BY battery_level_pct DESC NULLS LAST, at) AS r_bmax,
		           row_number() OVER (PARTITION BY vehicle_id, bk ORDER BY battery_level_pct ASC NULLS LAST, at)  AS r_bmin,
		           row_number() OVER (PARTITION BY vehicle_id, bk ORDER BY speed_mph DESC NULLS LAST, at) AS r_smax
		      FROM w
		)
		SELECT rvid, at,
		       COALESCE(battery_level_pct,0), COALESCE(range_mi,0), COALESCE(odometer_mi,0),
		       COALESCE(lat,0), COALESCE(lon,0), location_fix_at,
		       COALESCE(speed_mph,0),
		       COALESCE(shift_state,''), COALESCE(drive_mode,''), COALESCE(charging_state,''),
		       COALESCE(charger_power_kw,0), COALESCE(charge_limit_pct,0),
		       COALESCE(inside_temp_c,0), COALESCE(outside_temp_c,0),
		       COALESCE(drive_number,0), COALESCE(charge_number,0), source,
		       altitude_m, tire_pressure_min_bar, pack_temp_avg_c, pack_temp_max_c, pack_temp_min_c
		  FROM r
		 WHERE r_first = 1 OR r_last = 1 OR r_pmax = 1 OR r_pmin = 1 OR r_bmax = 1 OR r_bmin = 1 OR r_smax = 1
		 ORDER BY at ASC
		 LIMIT $7`,
		s.userID, since.UTC(), upper, first.Time.UTC(), width, buckets, limit, vehicleID)
	if err != nil {
		return Downsampled{}, err
	}
	defer rows.Close()
	out, err := scanSamples(rows)
	return Downsampled{Samples: out, Reduced: true, Total: total}, err
}

// DriveCondition is a recorded drive with the conditions it was driven
// in, from the drive's own samples: the median non-zero outside
// temperature (zero is the live merge's "no reading" sentinel, as in the
// drive recap) and the drive mode with the most samples.
type DriveCondition struct {
	ID           string
	VehicleID    string
	StartedAt    time.Time
	EndedAt      time.Time
	CreatedAt    time.Time
	Source       string
	DistanceMi   float64
	EnergyKWh    float64
	AvgSpeedMph  float64
	OutsideTempC *float64
	DriveMode    string
}

// DriveConditions lists the user's drives that started in [since, until)
// (optionally one vehicle, by Rivian id) with their conditions. Drives
// under a minute are left out, as in the drive list. Each drive reads
// only its own window of vehicle_state (primary key vehicle_id, at).
func (s *Store) DriveConditions(ctx context.Context, vehicleID string, since, until time.Time) ([]DriveCondition, error) {
	args := []any{s.userID, since.UTC(), until.UTC()}
	vfilter := ""
	if vehicleID != "" {
		vfilter = " AND v.rivian_vehicle_id = $4"
		args = append(args, vehicleID)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.external_id, v.rivian_vehicle_id, d.started_at, d.ended_at, d.created_at, d.source,
		       COALESCE(d.distance_mi,0), COALESCE(d.energy_used_kwh,0), COALESCE(d.avg_speed_mph,0),
		       c.temp_c, COALESCE(c.mode,'')
		FROM drives d
		JOIN vehicles v ON v.id = d.vehicle_id
		LEFT JOIN LATERAL (
			SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY vs.outside_temp_c)
			         FILTER (WHERE vs.outside_temp_c IS NOT NULL AND vs.outside_temp_c <> 0) AS temp_c,
			       mode() WITHIN GROUP (ORDER BY vs.drive_mode)
			         FILTER (WHERE COALESCE(vs.drive_mode,'') <> '') AS mode
			FROM vehicle_state vs
			WHERE vs.vehicle_id = d.vehicle_id AND vs.user_id = d.user_id
			  AND vs.at >= d.started_at AND vs.at <= d.ended_at
		) c ON true
		WHERE d.user_id = $1 AND d.started_at >= $2 AND d.started_at < $3
		  AND d.ended_at - d.started_at >= interval '1 minute'`+vfilter+`
		ORDER BY d.started_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DriveCondition
	for rows.Next() {
		var d DriveCondition
		var temp sql.NullFloat64
		if err := rows.Scan(&d.ID, &d.VehicleID, &d.StartedAt, &d.EndedAt, &d.CreatedAt, &d.Source,
			&d.DistanceMi, &d.EnergyKWh, &d.AvgSpeedMph, &temp, &d.DriveMode); err != nil {
			return nil, err
		}
		if temp.Valid {
			t := temp.Float64
			d.OutsideTempC = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
