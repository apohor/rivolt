// Package chargeenergy repairs the energy of AC home charges the recorder under-recorded. Every pod handover mid-charge
// restarted a charge's energy integral at zero (fixed in the recorder; see internal/rivian livestate.go), so charges since
// about Oct 15 carry only the energy since their last handover, and a cost computed from it.
//
// The repair only ever raises a row: it estimates the energy from the SoC gain, per vehicle where the vehicle has enough
// clean charges of its own (its median kWh per SoC %), else the pack's gain times the charging-loss factor, and replaces
// the energy (and the cost, from the row's own price) only on a row far under that estimate. The old figures go in the
// row's metadata; the row is marked energy_estimated; a JSON backup is written before any change.
//
// Nothing here runs by itself: a human audits the dry-run, supplies the id list, and approves one user at a time.
package chargeenergy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

const (
	// LowRatio: a row is a candidate when its energy per SoC % is under this share of its estimate. Rows between 0.8 and
	// 0.9 of the estimate are ordinary variance, and are left alone.
	LowRatio = 0.8
	// MinSoC is the smallest SoC gain a row needs to be judged (under it a rounding step moves the estimate too much).
	MinSoC = 5.0
	// MinCleanCharges is how many clean charges a vehicle needs for its own median to be used.
	MinCleanCharges = 3
	// ACPeakKW: a charge whose peak stayed under this is AC (a fast charge takes Rivian's own total, which survives).
	ACPeakKW = 22.0
	// CleanBand bounds the kWh per SoC % of a clean charge, as a share of the pack's expected figure, so a reset row
	// before the cutoff cannot pull a median down.
	CleanLow, CleanHigh = 0.8, 1.4
	// CleanWindow is how far before the cutoff clean charges are taken from.
	CleanWindow = 90 * 24 * time.Hour
)

// Row is one charge with what the rule needs.
type Row struct {
	ID               uuid.UUID
	ExternalID       string
	UserID           uuid.UUID
	VehicleID        uuid.UUID
	Model            string
	PackKWh          float64 // the vehicle's pack: its stored value, else the model/trim lookup
	StartedAt        time.Time
	EndedAt          time.Time
	StartSoC, EndSoC float64
	Energy           float64
	Cost             *float64
	Price            *float64
	Currency         string
	CostSource       string
	NetworkState     string
	PlaceOverride    string
	LocationOverride string
	MaxPowerKW       float64
	Source           string
	Metadata         json.RawMessage
}

// PerPct is the energy the wall delivers per SoC %, the repair's estimate for a vehicle, with how it was made.
type PerPct struct {
	KWh    float64
	Method string // "own median of N clean charges" | "pack x loss factor"
}

// PackPerPct is the pack-based estimate: the pack's energy per SoC % times the charging losses.
func PackPerPct(packKWh float64) float64 { return packKWh / 100 * rivian.ChargeWallToPackFactor }

// Estimate is the vehicle's estimate: its own median kWh per SoC % over its clean charges when it has at least
// MinCleanCharges of them, else the pack-based figure.
func Estimate(packKWh float64, clean []float64) PerPct {
	if len(clean) >= MinCleanCharges {
		c := append([]float64(nil), clean...)
		sort.Float64s(c)
		med := c[len(c)/2]
		if len(c)%2 == 0 {
			med = (c[len(c)/2-1] + c[len(c)/2]) / 2
		}
		return PerPct{KWh: med, Method: fmt.Sprintf("own median of %d clean charges", len(c))}
	}
	return PerPct{KWh: PackPerPct(packKWh), Method: "pack x loss factor"}
}

// IsClean says whether a charge before the cutoff is a trustworthy sample for its vehicle's median: AC, a real SoC gain,
// and kWh per SoC % within the band around what its pack should give.
func IsClean(r Row) bool {
	if r.Source != "live" || r.MaxPowerKW <= 0 || r.MaxPowerKW >= ACPeakKW || r.PackKWh <= 0 {
		return false
	}
	d := r.EndSoC - r.StartSoC
	if !socKnown(r) || d < MinSoC || r.Energy <= 0 {
		return false
	}
	per := r.Energy / d
	exp := PackPerPct(r.PackKWh)
	return per >= CleanLow*exp && per <= CleanHigh*exp
}

// socKnown says the row's SoC range is usable: a recorded start (0 is an unknown one, which would make the gain the whole end
// SoC), a rising SoC, and an end no higher than 100.
func socKnown(r Row) bool { return r.StartSoC > 0 && r.EndSoC > r.StartSoC && r.EndSoC <= 100 }

// capSlack is the recorder's own margin on its plausibility cap (energy cannot exceed peak power times duration).
const capSlack = 1.05

// Verdict is the rule's answer for one row.
type Verdict struct {
	Repair    bool
	Reason    string // why not, when !Repair
	NewEnergy float64
	NewCost   *float64
	Method    string
}

// Classify applies the rule. User-corrected rows and rows with a billed or confirmed price are refused first; only a
// row far under its estimate is repaired, and only upwards.
func Classify(r Row, est PerPct) Verdict {
	skip := func(s string) Verdict { return Verdict{Reason: s} }
	d := r.EndSoC - r.StartSoC
	switch {
	case r.Source != "live":
		return skip("not a live row")
	case r.MaxPowerKW <= 0 || r.MaxPowerKW >= ACPeakKW:
		return skip("not an AC charge")
	case strings.EqualFold(r.CostSource, "user"):
		return skip("carries a user-entered price")
	case strings.EqualFold(r.CostSource, "rivian"):
		return skip("priced by Rivian's own bill")
	case r.CostSource != "" && !strings.EqualFold(r.CostSource, "home_rate"):
		return skip("priced by another source")
	case r.NetworkState == "confirmed":
		return skip("carries a confirmed network")
	case r.PlaceOverride != "" || r.LocationOverride != "":
		return skip("carries a user correction (place or location)")
	case strings.Contains(string(r.Metadata), "energy_estimated"):
		return skip("already estimated")
	case !socKnown(r):
		return skip("SoC range unknown or implausible")
	case d < MinSoC:
		return skip("SoC gain under the minimum")
	case est.KWh <= 0:
		return skip("no estimate")
	}
	want := d * est.KWh
	if r.Energy >= LowRatio*want {
		return skip("energy is within the estimate's range")
	}
	if want <= r.Energy {
		return skip("the estimate is not higher")
	}
	hours := r.EndedAt.Sub(r.StartedAt).Hours()
	if hours <= 0 {
		return skip("no duration to check the estimate against")
	}
	if want > r.MaxPowerKW*hours*capSlack {
		return skip("estimate exceeds peak power x duration")
	}
	v := Verdict{Repair: true, NewEnergy: round(want, 3), Method: est.Method}
	if r.Cost != nil && r.Price != nil && *r.Price > 0 && strings.EqualFold(r.CostSource, "home_rate") {
		c := round(v.NewEnergy**r.Price, 4)
		v.NewCost = &c
	}
	return v
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

// Planned is a row with its estimate and verdict.
type Planned struct {
	Row     Row
	Est     PerPct
	Verdict Verdict
}

// Plan classifies every row against its vehicle's estimate. rows are the charges since the cutoff; clean holds each
// vehicle's clean kWh-per-SoC-% samples from before it.
func Plan(rows []Row, clean map[uuid.UUID][]float64) []Planned {
	out := make([]Planned, 0, len(rows))
	for _, r := range rows {
		est := Estimate(r.PackKWh, clean[r.VehicleID])
		out = append(out, Planned{Row: r, Est: est, Verdict: Classify(r, est)})
	}
	return out
}

// ModelSummary is the dry-run's per-model count. Nothing here names a user or a vehicle.
type ModelSummary struct {
	Model      string
	Considered int
	Repair     int
	Accounts   int
	OwnMedian  int // repairs estimated from the vehicle's own median
	PackBased  int // repairs estimated from the pack and the loss factor
	KWhBefore  float64
	KWhAfter   float64
}

// Summarise counts planned rows per model.
func Summarise(p []Planned) []ModelSummary {
	by := map[string]*ModelSummary{}
	users := map[string]map[uuid.UUID]bool{}
	for _, x := range p {
		m := x.Row.Model
		if m == "" {
			m = "unknown"
		}
		s := by[m]
		if s == nil {
			s = &ModelSummary{Model: m}
			by[m] = s
			users[m] = map[uuid.UUID]bool{}
		}
		s.Considered++
		if !x.Verdict.Repair {
			continue
		}
		s.Repair++
		users[m][x.Row.UserID] = true
		if strings.HasPrefix(x.Est.Method, "own") {
			s.OwnMedian++
		} else {
			s.PackBased++
		}
		s.KWhBefore += x.Row.Energy
		s.KWhAfter += x.Verdict.NewEnergy
	}
	var out []ModelSummary
	for m, s := range by {
		s.Accounts = len(users[m])
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

const rowSelect = `
	SELECT c.id, c.external_id, c.user_id, c.vehicle_id, COALESCE(v.model,''),
	       COALESCE(v.pack_kwh, 0), COALESCE(v.trim,''), COALESCE(v.model_year, 0),
	       c.started_at, c.ended_at, COALESCE(c.start_soc_pct,0), COALESCE(c.end_soc_pct,0), COALESCE(c.energy_added_kwh,0),
	       c.cost, c.price_per_kwh, COALESCE(c.currency,''), COALESCE(c.cost_source,''), COALESCE(c.network_state,''),
	       COALESCE(c.place_override,''), COALESCE(c.location_override,''), COALESCE(c.max_power_kw,0), c.source, c.metadata
	  FROM charges c JOIN vehicles v ON v.id = c.vehicle_id
	 WHERE c.ended_at IS NOT NULL AND c.source = 'live' AND c.end_soc_pct - c.start_soc_pct >= $2`

func scanRows(rows *sql.Rows) ([]Row, error) {
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var trim string
		var year int
		var cost, price sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.ExternalID, &r.UserID, &r.VehicleID, &r.Model, &r.PackKWh, &trim, &year,
			&r.StartedAt, &r.EndedAt, &r.StartSoC, &r.EndSoC, &r.Energy, &cost, &price, &r.Currency, &r.CostSource, &r.NetworkState,
			&r.PlaceOverride, &r.LocationOverride, &r.MaxPowerKW, &r.Source, &r.Metadata); err != nil {
			return nil, err
		}
		if cost.Valid {
			r.Cost = &cost.Float64
		}
		if price.Valid {
			r.Price = &price.Float64
		}
		if r.PackKWh <= 0 {
			r.PackKWh = rivian.InferPackKWh(r.Model, trim, year)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Load reads the charges since the cutoff and each vehicle's clean samples from before it.
func Load(ctx context.Context, d *sql.DB, since time.Time) ([]Row, map[uuid.UUID][]float64, error) {
	rs, err := d.QueryContext(ctx, rowSelect+` AND c.started_at >= $1 ORDER BY c.started_at`, since, MinSoC)
	if err != nil {
		return nil, nil, err
	}
	rows, err := scanRows(rs)
	if err != nil {
		return nil, nil, err
	}
	cs, err := d.QueryContext(ctx, rowSelect+` AND c.started_at < $1 AND c.started_at >= $1 - make_interval(secs => $3)`,
		since, MinSoC, CleanWindow.Seconds())
	if err != nil {
		return nil, nil, err
	}
	before, err := scanRows(cs)
	if err != nil {
		return nil, nil, err
	}
	clean := map[uuid.UUID][]float64{}
	for _, r := range before {
		if IsClean(r) {
			clean[r.VehicleID] = append(clean[r.VehicleID], r.Energy/(r.EndSoC-r.StartSoC))
		}
	}
	return rows, clean, nil
}

// BackupRow is what the backup keeps of a row before it changes: enough to put it back exactly.
type BackupRow struct {
	ID        uuid.UUID       `json:"id"`
	UserID    uuid.UUID       `json:"user_id"`
	Energy    float64         `json:"energy_added_kwh"`
	Cost      *float64        `json:"cost"`
	Metadata  json.RawMessage `json:"metadata"`
	NewEnergy float64         `json:"new_energy_added_kwh"`
	NewCost   *float64        `json:"new_cost"`
	Method    string          `json:"method"`
}

// WriteBackup writes the rows as one JSON object per line, 0600, refusing to overwrite, and syncs it.
func WriteBackup(path string, rows []BackupRow) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ReadBackup reads a backup written by WriteBackup.
func ReadBackup(path string) ([]BackupRow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []BackupRow
	for n, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r BackupRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// Apply repairs one user's rows from an audited id list. It reads the rows again inside a transaction (locking them),
// re-runs the rule against a fresh estimate, writes the backup of what it will change before changing anything, and
// updates the energy (and cost), the metadata note and the estimated mark. It reports how many rows it changed and how
// many it skipped because the rule no longer held. Rows of other users are never touched.
func Apply(ctx context.Context, d *sql.DB, user uuid.UUID, ids []uuid.UUID, since time.Time, backupPath string) (changed, skipped int, err error) {
	rows, clean, err := Load(ctx, d, since)
	if err != nil {
		return 0, 0, err
	}
	want := map[uuid.UUID]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var plan []Planned
	for _, p := range Plan(rows, clean) {
		if want[p.Row.ID] && p.Row.UserID == user {
			plan = append(plan, p)
		}
	}
	skipped = len(want) - len(plan)
	var todo []Planned
	var backup []BackupRow
	for _, p := range plan {
		if !p.Verdict.Repair {
			skipped++
			continue
		}
		todo = append(todo, p)
		backup = append(backup, BackupRow{ID: p.Row.ID, UserID: p.Row.UserID, Energy: p.Row.Energy, Cost: p.Row.Cost, Metadata: p.Row.Metadata,
			NewEnergy: p.Verdict.NewEnergy, NewCost: p.Verdict.NewCost, Method: p.Verdict.Method})
	}
	if len(todo) == 0 {
		return 0, skipped, nil
	}
	if err := WriteBackup(backupPath, backup); err != nil {
		return 0, skipped, fmt.Errorf("backup: %w", err)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, skipped, err
	}
	defer func() { _ = tx.Rollback() }()
	at := time.Now().UTC().Format(time.RFC3339)
	for _, p := range todo {
		var newCost any
		if p.Verdict.NewCost != nil {
			newCost = *p.Verdict.NewCost
		}
		var oldCost any
		if p.Row.Cost != nil {
			oldCost = *p.Row.Cost
		}
		// The guard re-checks that the row is exactly what the plan saw (it may have been edited since).
		res, err := tx.ExecContext(ctx, `
			UPDATE charges SET
			       energy_added_kwh = $2,
			       cost = COALESCE($3::double precision, cost),
			       metadata = metadata || jsonb_build_object('energy_estimated', true,
			           'energy_repair', jsonb_build_object('from_kwh', $4::double precision, 'from_cost', $5::double precision,
			                                              'method', $6::text, 'at', $7::text)),
			       updated_at = NOW()
			 WHERE id = $1 AND user_id = $8 AND energy_added_kwh = $4::double precision AND cost IS NOT DISTINCT FROM $5::double precision`,
			p.Row.ID, p.Verdict.NewEnergy, newCost, p.Row.Energy, oldCost, p.Verdict.Method, at, user)
		if err != nil {
			return 0, skipped, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			changed++
		} else {
			skipped++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, skipped, err
	}
	return changed, skipped, nil
}

// Restore puts backed-up rows back as they were, but only where the row still holds the repaired figures (so a later edit
// by the user is not overwritten). It reports how many it restored.
func Restore(ctx context.Context, d *sql.DB, rows []BackupRow) (restored int, err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range rows {
		var cost, newCost any
		if r.Cost != nil {
			cost = *r.Cost
		}
		if r.NewCost != nil {
			newCost = *r.NewCost
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE charges SET energy_added_kwh = $2, cost = $3::double precision, metadata = $4::jsonb, updated_at = NOW()
			 WHERE id = $1 AND user_id = $5 AND energy_added_kwh = $6::double precision
			   AND (cost IS NOT DISTINCT FROM $7::double precision OR $7::double precision IS NULL)`,
			r.ID, r.Energy, cost, string(r.Metadata), r.UserID, r.NewEnergy, newCost)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			restored++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return restored, nil
}
