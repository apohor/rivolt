package r2

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Vehicle is the vehicle being repaired.
type Vehicle struct {
	ID       uuid.UUID // vehicles.id
	UserID   uuid.UUID
	RivianID string
	Model    string
	PackKWh  float64
}

// LoadVehicle resolves a Rivian vehicle id.
func LoadVehicle(ctx context.Context, d *sql.DB, rivianID string) (Vehicle, error) {
	v := Vehicle{RivianID: rivianID}
	var pack sql.NullFloat64
	var model sql.NullString
	err := d.QueryRowContext(ctx,
		`SELECT id, user_id, model, pack_kwh FROM vehicles WHERE rivian_vehicle_id = $1`, rivianID).
		Scan(&v.ID, &v.UserID, &model, &pack)
	if err != nil {
		return v, fmt.Errorf("vehicle %s: %w", rivianID, err)
	}
	v.Model, v.PackKWh = strings.TrimSpace(model.String), pack.Float64
	return v, nil
}

// LoadRows reads the vehicle's samples in [since, until), oldest first.
func LoadRows(ctx context.Context, d *sql.DB, v Vehicle, since, until time.Time) ([]Row, error) {
	rs, err := d.QueryContext(ctx, `
		SELECT at, COALESCE(power_state,''), COALESCE(charging_state,''),
		       COALESCE(odometer_mi,0), COALESCE(battery_level_pct,0), COALESCE(speed_mph,0),
		       COALESCE(lat,0), COALESCE(lon,0), COALESCE(charger_power_kw,0)
		FROM vehicle_state
		WHERE vehicle_id = $1 AND at >= $2::timestamptz AND at < $3::timestamptz
		ORDER BY at`, v.ID, since, until)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		if err := rs.Scan(&r.At, &r.Power, &r.ChargingState, &r.OdoMi, &r.SoC, &r.SpeedMph, &r.Lat, &r.Lon, &r.KW); err != nil {
			return nil, err
		}
		r.Power = strings.ToLower(strings.TrimSpace(r.Power))
		out = append(out, r)
	}
	return out, rs.Err()
}

// Existing is a stored session in the window.
type Existing struct {
	ID         uuid.UUID
	ExternalID string
	Start, End time.Time
	Miles      float64
	Source     string
	UserEdited bool // notes/tags (drives) or a user price (charges)
}

// LoadDrives returns the vehicle's drives overlapping [since, until).
func LoadDrives(ctx context.Context, d *sql.DB, v Vehicle, since, until time.Time) ([]Existing, error) {
	return loadExisting(ctx, d, `
		SELECT id, external_id, started_at, ended_at, COALESCE(distance_mi,0), source,
		       (COALESCE(notes,'') <> '' OR cardinality(tags) > 0)
		FROM drives WHERE vehicle_id = $1 AND started_at < $3::timestamptz AND ended_at > $2::timestamptz
		ORDER BY started_at`, v, since, until)
}

// LoadCharges returns the vehicle's charges overlapping [since, until).
func LoadCharges(ctx context.Context, d *sql.DB, v Vehicle, since, until time.Time) ([]Existing, error) {
	return loadExisting(ctx, d, `
		SELECT id, external_id, started_at, ended_at, 0, source, (cost_source = 'user')
		FROM charges WHERE vehicle_id = $1 AND started_at < $3::timestamptz AND ended_at > $2::timestamptz
		ORDER BY started_at`, v, since, until)
}

func loadExisting(ctx context.Context, d *sql.DB, q string, v Vehicle, since, until time.Time) ([]Existing, error) {
	rs, err := d.QueryContext(ctx, q, v.ID, since, until)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Existing
	for rs.Next() {
		var e Existing
		var edited sql.NullBool
		if err := rs.Scan(&e.ID, &e.ExternalID, &e.Start, &e.End, &e.Miles, &e.Source, &edited); err != nil {
			return nil, err
		}
		e.UserEdited = edited.Bool
		out = append(out, e)
	}
	return out, rs.Err()
}

// NextLiveDriveAfter returns the start of the vehicle's first live drive
// starting at or after t (ok=false when none). The operator checks it
// against when the fixed recorder reached prod: drives recorded after
// that must stay out of the window.
func NextLiveDriveAfter(ctx context.Context, d *sql.DB, v Vehicle, t time.Time) (time.Time, bool, error) {
	var at sql.NullTime
	err := d.QueryRowContext(ctx,
		`SELECT min(started_at) FROM drives WHERE vehicle_id = $1 AND source = 'live' AND started_at >= $2::timestamptz`, v.ID, t).Scan(&at)
	if err != nil {
		return time.Time{}, false, err
	}
	return at.Time, at.Valid, nil
}

// Plan is what an apply does.
type Plan struct {
	Vehicle       Vehicle
	Since, Until  time.Time
	DeleteDrives  []Existing // live drives in the window, replaced
	InsertDrives  []Trip
	InsertCharges []Charge
	Skipped       []string // why something was left alone
}

// MakePlan refuses any vehicle but an R2: the segmentation rests on an
// R2's power state; an R1's recorded gear is the better record.
//
// MakePlan replaces every live drive inside [since, until) with the
// re-segmented trips and adds the recovered charges that don't overlap
// a stored charge. It refuses (error) when a drive in the window isn't
// live, sticks out of the window, or carries the user's notes/tags:
// those are a person's call, not a repair's.
func MakePlan(v Vehicle, since, until time.Time, trips []Trip, chs []Charge, drives, stored []Existing) (Plan, error) {
	p := Plan{Vehicle: v, Since: since, Until: until}
	if !strings.EqualFold(v.Model, "R2") {
		return p, fmt.Errorf("vehicle %s is a %q, not an R2: refusing", v.RivianID, v.Model)
	}
	for _, d := range drives {
		switch {
		case d.Source != "live":
			return p, fmt.Errorf("drive %s (%s) isn't a live drive; narrow the window", d.ExternalID, d.Source)
		case d.Start.Before(since) || d.End.After(until):
			return p, fmt.Errorf("drive %s (%s → %s) crosses the window; widen it to cover the whole drive",
				d.ExternalID, d.Start.Format(time.RFC3339), d.End.Format(time.RFC3339))
		case d.UserEdited:
			return p, fmt.Errorf("drive %s has the user's notes or tags", d.ExternalID)
		}
		p.DeleteDrives = append(p.DeleteDrives, d)
	}
	p.InsertDrives = trips
	for _, c := range chs {
		if o := overlapping(c.Start, c.End, stored); o != nil {
			p.Skipped = append(p.Skipped, fmt.Sprintf("charge %s–%s overlaps stored charge %s", c.Start.Format(time.RFC3339), c.End.Format(time.RFC3339), o.ExternalID))
			continue
		}
		p.InsertCharges = append(p.InsertCharges, c)
	}
	return p, nil
}

func overlapping(s, e time.Time, xs []Existing) *Existing {
	for i := range xs {
		if xs[i].Start.Before(e) && xs[i].End.After(s) {
			return &xs[i]
		}
	}
	return nil
}

// BackupRow is one row saved before the apply (Kind: drive,
// drive_recaps, drive_efficiency, drive_weather, drive_weather_series)
// or one row the apply inserted (inserted_drive, inserted_charge;
// Row holds {"external_id": …}).
type BackupRow struct {
	Kind string
	Row  json.RawMessage
}

// dependents are the per-drive tables keyed (user_id, drive_id =
// drives.external_id), without a foreign key.
var dependents = []string{"drive_recaps", "drive_efficiency", "drive_weather", "drive_weather_series"}

// Apply runs p in one transaction. sink receives the backup (every
// replaced row as to_jsonb, then markers for every inserted row) and
// must persist it durably; it is called before anything is deleted, and
// again for the insert markers before the commit. encode turns a trip's
// path into the route polyline.
func Apply(ctx context.Context, d *sql.DB, p Plan, encode func([][2]float64) string, sink func([]BackupRow) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	v := p.Vehicle
	ext := make([]string, len(p.DeleteDrives))
	for i, x := range p.DeleteDrives {
		ext[i] = x.ExternalID
	}
	extArr := "{" + strings.Join(quoteAll(ext), ",") + "}"

	var backup []BackupRow
	collect := func(kind, q string, args ...any) error {
		rs, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("backup %s: %w", kind, err)
		}
		defer rs.Close()
		for rs.Next() {
			var raw []byte
			if err := rs.Scan(&raw); err != nil {
				return err
			}
			backup = append(backup, BackupRow{Kind: kind, Row: raw})
		}
		return rs.Err()
	}
	if err := collect("drive", `SELECT to_jsonb(d.*) FROM drives d WHERE d.vehicle_id = $1 AND d.external_id = ANY($2::text[])`, v.ID, extArr); err != nil {
		return err
	}
	if n := countKind(backup, "drive"); n != len(p.DeleteDrives) {
		return fmt.Errorf("backed up %d drives, planned %d: the data changed since the dry run", n, len(p.DeleteDrives))
	}
	for _, t := range dependents {
		if err := collect(t, `SELECT to_jsonb(x.*) FROM `+t+` x WHERE x.user_id = $1 AND x.drive_id = ANY($2::text[])`, v.UserID, extArr); err != nil {
			return err
		}
	}
	if err := sink(backup); err != nil {
		return fmt.Errorf("backup: %w", err)
	}

	for _, t := range dependents {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE user_id = $1 AND drive_id = ANY($2::text[])`, v.UserID, extArr); err != nil {
			return fmt.Errorf("delete %s: %w", t, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM drives WHERE vehicle_id = $1 AND source = 'live' AND external_id = ANY($2::text[])`, v.ID, extArr)
	if err != nil {
		return fmt.Errorf("delete drives: %w", err)
	}
	if n, _ := res.RowsAffected(); int(n) != len(p.DeleteDrives) {
		return fmt.Errorf("deleted %d drives, planned %d", n, len(p.DeleteDrives))
	}

	var inserted []BackupRow
	for _, t := range p.InsertDrives {
		id := ExternalID(v.RivianID, "d", t.Start)
		var energy any
		if e := t.EnergyKWh(v.PackKWh); e > 0 {
			energy = e
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at,
			       start_soc_pct, end_soc_pct, start_odometer_mi, end_odometer_mi, distance_mi,
			       start_lat, start_lon, end_lat, end_lon, max_speed_mph, avg_speed_mph,
			       energy_used_kwh, source, route_polyline, metadata)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'live',NULLIF($18,''),
			        jsonb_build_object('repair', 'r2-history'))`,
			v.UserID, v.ID, id, t.Start.UTC(), t.End.UTC(),
			nz(t.StartSoC), nz(t.EndSoC), nz(t.StartOdoMi), nz(t.EndOdoMi), nz(t.DistanceMi()),
			nz(t.StartLat), nz(t.StartLon), nz(t.EndLat), nz(t.EndLon), nz(t.MaxSpeedMph), nz(t.AvgSpeedMph()),
			energy, encode(t.Path)); err != nil {
			return fmt.Errorf("insert drive %s: %w", id, err)
		}
		inserted = append(inserted, marker("inserted_drive", id))
	}
	for _, c := range p.InsertCharges {
		id := ExternalID(v.RivianID, "c", c.Start)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at,
			       start_soc_pct, end_soc_pct, energy_added_kwh, max_power_kw, avg_power_kw,
			       final_state, lat, lon, source, active_seconds,
			       cost, price_per_kwh, currency, cost_source, place)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'charging_complete',$11,$12,'live',$13,
			        $14,$15,NULLIF($16,''),NULLIF($17,''),NULLIF($18,''))`,
			v.UserID, v.ID, id, c.Start.UTC(), c.End.UTC(),
			nz(c.StartSoC), nz(c.EndSoC), c.EnergyKWh, nz(c.PeakKW), nz(avgKW(c)),
			nz(c.Lat), nz(c.Lon), nz(c.ActiveSeconds),
			nz(c.Cost), nz(c.PricePerKWh), c.Currency, c.CostSource, c.Place); err != nil {
			return fmt.Errorf("insert charge %s: %w", id, err)
		}
		inserted = append(inserted, marker("inserted_charge", id))
	}
	if err := sink(inserted); err != nil {
		return fmt.Errorf("backup inserted ids: %w", err)
	}
	return tx.Commit()
}

// Restore undoes an Apply from its backup: the inserted rows go, the
// replaced drives and their dependents come back. One transaction.
func Restore(ctx context.Context, d *sql.DB, v Vehicle, rows []BackupRow) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// A repaired row the user has since made their own (notes, tags, a
	// price, a confirmed network) isn't removed silently.
	var edited []string
	for _, r := range rows {
		var m struct {
			ExternalID string `json:"external_id"`
		}
		if r.Kind != "inserted_drive" && r.Kind != "inserted_charge" {
			continue
		}
		if err := json.Unmarshal(r.Row, &m); err != nil || m.ExternalID == "" {
			return fmt.Errorf("bad marker %s", r.Row)
		}
		q := `SELECT count(*) FROM drives WHERE vehicle_id = $1 AND external_id = $2 AND (COALESCE(notes,'') <> '' OR cardinality(tags) > 0)`
		if r.Kind == "inserted_charge" {
			q = `SELECT count(*) FROM charges WHERE vehicle_id = $1 AND external_id = $2 AND cost_source = 'user'`
		}
		var n int
		if err := tx.QueryRowContext(ctx, q, v.ID, m.ExternalID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			edited = append(edited, m.ExternalID)
		}
	}
	if len(edited) > 0 {
		return fmt.Errorf("refusing: the user has edited repaired rows since (%s); restore them by hand", strings.Join(edited, ", "))
	}
	for _, r := range rows {
		switch r.Kind {
		case "inserted_drive", "inserted_charge":
			var m struct {
				ExternalID string `json:"external_id"`
			}
			if err := json.Unmarshal(r.Row, &m); err != nil || m.ExternalID == "" {
				return fmt.Errorf("bad marker %s", r.Row)
			}
			table := "drives"
			if r.Kind == "inserted_charge" {
				table = "charges"
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE vehicle_id = $1 AND external_id = $2`, v.ID, m.ExternalID); err != nil {
				return err
			}
		}
	}
	for _, r := range rows {
		table := r.Kind
		switch r.Kind {
		case "drive":
			table = "drives"
		case "drive_recaps", "drive_efficiency", "drive_weather", "drive_weather_series":
		default:
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` SELECT * FROM jsonb_populate_record(NULL::`+table+`, $1::jsonb)`, string(r.Row)); err != nil {
			return fmt.Errorf("restore %s: %w", table, err)
		}
	}
	return tx.Commit()
}

func marker(kind, externalID string) BackupRow {
	b, _ := json.Marshal(map[string]string{"external_id": externalID})
	return BackupRow{Kind: kind, Row: b}
}

func countKind(rs []BackupRow, kind string) int {
	n := 0
	for _, r := range rs {
		if r.Kind == kind {
			n++
		}
	}
	return n
}

func avgKW(c Charge) float64 {
	if h := c.End.Sub(c.Start).Hours(); h > 0 {
		return c.EnergyKWh / h
	}
	return 0
}

func nz(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = `"` + strings.ReplaceAll(strings.ReplaceAll(x, `\`, `\\`), `"`, `\"`) + `"`
	}
	return out
}
