// Package stretched is the repair for charge rows the stale-charge janitor stretched past their real end (#1619,
// #1630): it refreshed an open row's ended_at on a charger state that stuck after an unplug, so the row ran on
// through the vehicle's later drives. The repair caps ended_at at the last frame the vehicle read plugged in.
//
// Like internal/repair/blips it FINDS its candidates by one predicate, and an apply must present the count and
// the sha256 of the sorted id list of an approved dry run; anything else aborts the transaction. It changes
// ended_at only, never later and never before started_at, in one transaction, after the old row (to_jsonb) went
// to charges_capped_backup and the durable CSV sink. It refuses open rows, rows written in the last hour and rows a user
// touched. A row whose plugged frames continue after a drive may hold a second, real charge; those are FLAGGED
// (reported, not changed): the rule is Flagger, and can be swapped.
package stretched

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/repair/blips"
	"github.com/apohor/rivolt/internal/repair/phantom"
	"github.com/apohor/rivolt/internal/rivian"
)

const (
	// MinStretch: a row is stretched when ended_at is this far past the last plugged frame.
	MinStretch = time.Hour
	// QuietFor: a row written more recently than this is left alone (the recorder or the janitor may still be on it).
	QuietFor = time.Hour
)

// Row is a candidate: a live charge with at least one same-vehicle drive starting inside it, and the evidence.
type Row struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	VehicleID  uuid.UUID
	StartedAt  time.Time
	EndedAt    time.Time
	UpdatedAt  time.Time
	FinalState string
	Source     string
	CostSource string
	// FirstDrive is the start of the first drive inside the row.
	FirstDrive time.Time
	// LastPlugged is the last frame before the first drive where the vehicle read plugged in and stood still (zero: none).
	LastPlugged time.Time
	// PluggedAfterDrive: an active charging frame (power, standing still) exists after FirstDrive.
	PluggedAfterDrive bool
	// What a person or another table says about the row.
	HasRestamp bool
	HasReceipt bool
	Edited     bool

	// The split's evidence (loaded for rows with plugged frames after a drive).
	RivianVehicleID        string
	PackKWh                float64
	EnergyKWh              float64
	StartSoC, EndSoC       float64
	PricePerKWh            float64
	Cost                   float64 // the stored cost (0: none)
	LastPluggedBeforeDrive time.Time
	Frames                 []Frame
	Locs                   []Loc
	// Taken is the set of external ids already used by the vehicle's charges (a new row must not collide).
	Taken map[string]bool
}

// Loc is a frame's position, for placing a new row.
type Loc struct {
	At       time.Time
	Lat, Lon float64
}

// NewRow is a charge the split creates from a parked segment after a drive.
type NewRow struct {
	ExternalID       string
	Start, End       time.Time
	SoCStart, SoCEnd float64
	KWh              float64
	MaxPowerKW       float64
	AvgPowerKW       float64 // kWh over the active hours
	ActiveSeconds    float64
	FinalState       string // the original's for the last segment, charging_complete for an earlier one
	Lat, Lon         float64
	// Priced by Options.Price when it is set (zero values: left unpriced, "Not set").
	Cost       float64
	PricePerKW float64
	Currency   string
	CostSource string
	Network    string
	Place      string
}

// Split is the plan for a row that holds absorbed charges: the original keeps what was before the first drive
// (energy conserved: stored minus the new rows), and each parked segment after a drive becomes a row.
type Split struct {
	OrigEndSoC float64
	OrigKWh    float64
	OrigCost   *float64 // set when the original is auto-priced and re-priced by energy share
	// The original's figures recomputed from the frames inside its own window: the stored ones may belong to an
	// absorbed (DC) session.
	OrigMaxKW, OrigAvgKW, OrigActiveSec float64
	// charging_complete when the SoC rose over the original's window, else abandoned.
	OrigFinalState string
	// EmptyOriginal: nothing in the original's span before the first drive was a charge (no plugged frame, no SoC gain). It
	// is kept as a zero-energy abandoned row ended at the first drive; its remainder is still stored minus the new rows.
	EmptyOriginal bool
	// ClampedKWh is the excess the new rows were scaled down by (0: not clamped).
	ClampedKWh float64
	News       []NewRow
}

// Options select the optional behaviour of a plan.
type Options struct {
	// Flag replaces DefaultFlagger.
	Flag Flagger
	// Split turns rows with plugged frames after a drive into splits instead of flags.
	Split bool
	// CapBySoC (mode A) caps a row with no plugged frame and no charging after its first drive at the last pre-drive frame
	// where the SoC rose (else at the first drive): a real charge on a vehicle that reports no charger status or power.
	CapBySoC bool
	// Price, when set, prices and places each new row of a split (see PriceFn). It runs in the plan, so the digest covers its
	// result and the apply recomputes it: a changed rate or home location aborts the apply as drift.
	Price PriceFn
}

// PriceFn fills Cost, PricePerKWh, Currency, CostSource, Network and Place on a charge built from a new row (it reads
// StartedAt, EndedAt, Lat, Lon, MaxPowerKW, ActiveSeconds and EnergyAddedKWh), for the user. It must not call Rivian, and
// must leave a row it cannot price as it is.
type PriceFn func(userID uuid.UUID, c *charges.Charge)

// Verdict is the rule's answer for one row.
type Verdict struct {
	CapTo  time.Time // set when the row is capped
	BySoC  bool      // capped by the SoC rule (mode A), not by a plugged frame
	SoCOff bool      // with BySoC: the stored kWh disagrees with the pre-drive SoC gain
	Split  *Split    // set when the row is split (CapTo is the original's new end)
	Reason string    // why a row is skipped or flagged ("" when capped)
	Flag   bool      // skipped because it may hold an absorbed charge
}

// Flagger decides whether a row may hold a second, real charge that the stretch swallowed, returning the
// reason ("" for no). The default looks at plugged frames after a drive; the rule is meant to be replaced by
// something that can also see a gradual SoC rise (DefaultFlagger is only the conservative start).
type Flagger func(Row) string

// DefaultFlagger flags a row with active charging frames after its first drive.
func DefaultFlagger(r Row) string {
	if r.PluggedAfterDrive {
		return "flagged: charging frames after a drive (possible absorbed charge)"
	}
	return ""
}

// Classify applies the rule at time now.
func Classify(r Row, now time.Time, opt Options) Verdict {
	flag := opt.Flag
	if flag == nil {
		flag = DefaultFlagger
	}
	skip := func(s string) Verdict { return Verdict{Reason: s} }
	switch {
	case r.Source != "live":
		return skip("not a live row")
	case charges.IsOpenLiveFinalState(r.FinalState):
		return skip("open: final_state " + r.FinalState)
	case now.Sub(r.UpdatedAt) < QuietFor:
		return skip("written in the last hour")
	case strings.EqualFold(r.CostSource, "user"):
		return skip("carries a user-entered price")
	case r.HasRestamp:
		return skip("carries a place correction")
	case r.HasReceipt:
		return skip("a receipt points at it")
	case r.Edited:
		return skip("carries a user override, note or tag")
	case r.FirstDrive.IsZero():
		return skip("no drive inside")
	case r.LastPlugged.IsZero() && !(opt.Split && flag(r) != ""):
		// With --split a row whose only charging came after its first drive goes on to the split (an empty original).
		// With --cap-by-soc a row with no charging after the drive either is judged by its SoC before the drive.
		if opt.CapBySoC && !r.PluggedAfterDrive {
			return capBySoC(r)
		}
		return skip("no plugged frame inside")
	}
	if why := flag(r); why != "" {
		if opt.Split {
			if sp, cut, reason := trySplit(r, opt); reason == "" {
				return Verdict{CapTo: cut, Split: sp}
			} else {
				return Verdict{Reason: reason, Flag: true}
			}
		}
		return Verdict{Reason: why, Flag: true}
	}
	switch {
	case r.EndedAt.Sub(r.LastPlugged) <= MinStretch:
		return skip("not stretched")
	case r.LastPlugged.Before(r.StartedAt) || !r.LastPlugged.Before(r.EndedAt):
		return skip("cap outside the row")
	}
	return Verdict{CapTo: r.LastPlugged}
}

// Queryer is *sql.DB or *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// stationary excludes frames taken while the vehicle moved: inside a same-vehicle drive, or above the recorder's
// drive-close speed (2 mph). A charger status that sticks at "connected" through a drive must not read as plugged.
const stationary = `(COALESCE(s.speed_mph,0) <= 2
    AND NOT EXISTS (SELECT 1 FROM drives dd WHERE dd.vehicle_id = s.vehicle_id AND s.at BETWEEN dd.started_at AND dd.ended_at))`

// pluggedFrame is chargerPlugged for a stored vehicle_state frame, judged as if no charge were open (the recorder's
// rule in internal/rivian, minus the open-charge allowance): a connected status, or charging with power behind it,
// while the vehicle stands still.
const pluggedFrame = `(` + stationary + ` AND (s.charger_status LIKE 'chrgr_sts_connected%'
    OR (s.charging_state IN ('charging_active','charging_connecting') AND COALESCE(s.charger_power_kw,0) > 0)))`

// activeFrame is a frame that shows charging going on: charging_active with power, while standing still. Frames
// like this after a drive are the evidence of an absorbed charge; a status that merely stays "connected" is not.
const activeFrame = `(` + stationary + ` AND s.charging_state = 'charging_active' AND COALESCE(s.charger_power_kw,0) > 0)`

// Select loads every live charge with a same-vehicle drive starting strictly inside it, with the evidence. With
// forUpdate the charge rows are locked, for the apply transaction. With frames the rows that have plugged frames
// after a drive also carry their vehicle_state frames and the vehicle's taken external ids, for the split.
func Select(ctx context.Context, q Queryer, forUpdate, frames bool) ([]Row, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF c"
	}
	rows, err := q.QueryContext(ctx, `
		WITH cand AS (
		  SELECT c.id, c.user_id, c.vehicle_id, c.started_at, c.ended_at, c.updated_at,
		         COALESCE(c.final_state,'') AS final_state, c.source, COALESCE(c.cost_source,'') AS cost_source,
		         COALESCE(c.energy_added_kwh,0) AS kwh, COALESCE(c.start_soc_pct,0) AS soc0, COALESCE(c.end_soc_pct,0) AS soc1,
		         COALESCE(c.price_per_kwh,0)::float8 AS ppk, COALESCE(c.cost,0)::float8 AS cost0,
		         (SELECT min(d.started_at) FROM drives d
		           WHERE d.vehicle_id = c.vehicle_id AND d.started_at > c.started_at AND d.started_at < c.ended_at) AS first_drive,
		         EXISTS (SELECT 1 FROM charge_restamps r WHERE r.charge_id = c.id) AS restamp,
		         EXISTS (SELECT 1 FROM rivian_receipts rr WHERE rr.charge_id = c.id) AS receipt,
		         (c.place_override IS NOT NULL OR c.location_override IS NOT NULL OR COALESCE(c.notes,'') <> '' OR cardinality(c.tags) > 0) AS edited
		    FROM charges c
		   WHERE c.source = 'live'
		   ORDER BY c.vehicle_id, c.started_at, c.id`+lock+`
		)
		SELECT cand.id, cand.user_id, cand.vehicle_id, cand.started_at, cand.ended_at, cand.updated_at,
		       cand.final_state, cand.source, cand.cost_source, cand.first_drive,
		       (SELECT max(s.at) FROM vehicle_state s
		         WHERE s.vehicle_id = cand.vehicle_id AND s.at BETWEEN cand.started_at AND cand.first_drive AND `+pluggedFrame+`),
		       EXISTS (SELECT 1 FROM vehicle_state s
		         WHERE s.vehicle_id = cand.vehicle_id AND s.at > cand.first_drive AND s.at <= cand.ended_at AND `+activeFrame+`),
		       cand.restamp, cand.receipt, cand.edited,
		       cand.kwh, cand.soc0, cand.soc1, cand.ppk, cand.cost0,
		       COALESCE(v.pack_kwh,0), v.rivian_vehicle_id
		  FROM cand JOIN vehicles v ON v.id = cand.vehicle_id
		 WHERE cand.first_drive IS NOT NULL
		 ORDER BY cand.vehicle_id, cand.started_at, cand.id`)
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		var r Row
		var drive, pluggedBefore sql.NullTime
		if err := rows.Scan(&r.ID, &r.UserID, &r.VehicleID, &r.StartedAt, &r.EndedAt, &r.UpdatedAt, &r.FinalState, &r.Source, &r.CostSource,
			&drive, &pluggedBefore, &r.PluggedAfterDrive, &r.HasRestamp, &r.HasReceipt, &r.Edited,
			&r.EnergyKWh, &r.StartSoC, &r.EndSoC, &r.PricePerKWh, &r.Cost, &r.PackKWh, &r.RivianVehicleID); err != nil {
			rows.Close()
			return nil, err
		}
		r.FirstDrive, r.LastPlugged, r.LastPluggedBeforeDrive = drive.Time, pluggedBefore.Time, pluggedBefore.Time
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if frames {
		taken := map[uuid.UUID]map[string]bool{}
		for i := range out {
			r := &out[i]
			// Frames are read for the rows the split or the SoC cap may need: charging after the first drive, or no plugged
			// frame before it.
			if !r.PluggedAfterDrive && !r.LastPluggedBeforeDrive.IsZero() {
				continue
			}
			if err := loadFrames(ctx, q, r); err != nil {
				return nil, err
			}
			if taken[r.VehicleID] == nil {
				t, err := takenIDs(ctx, q, r.VehicleID)
				if err != nil {
					return nil, err
				}
				taken[r.VehicleID] = t
			}
			r.Taken = taken[r.VehicleID]
		}
	}
	return out, nil
}

// loadFrames reads the vehicle_state frames inside the row (ascending), and their positions.
func loadFrames(ctx context.Context, q Queryer, r *Row) error {
	rs, err := q.QueryContext(ctx, `
		SELECT s.at, COALESCE(s.speed_mph,0), COALESCE(s.charger_power_kw,0), COALESCE(s.battery_level_pct,0),
		       COALESCE(s.shift_state,''), COALESCE(s.charging_state,''), COALESCE(s.lat,0), COALESCE(s.lon,0)
		  FROM vehicle_state s
		 WHERE s.vehicle_id = $1 AND s.at BETWEEN $2 AND $3
		 ORDER BY s.at`, r.VehicleID, r.StartedAt, r.EndedAt)
	if err != nil {
		return err
	}
	defer rs.Close()
	for rs.Next() {
		var f Frame
		var lat, lon float64
		if err := rs.Scan(&f.At, &f.SpeedMph, &f.PowerKW, &f.SoC, &f.Gear, &f.ChargingState, &lat, &lon); err != nil {
			return err
		}
		r.Frames = append(r.Frames, f)
		if lat != 0 || lon != 0 {
			r.Locs = append(r.Locs, Loc{At: f.At, Lat: lat, Lon: lon})
		}
	}
	return rs.Err()
}

// takenIDs is the set of external ids the vehicle's charges already use.
func takenIDs(ctx context.Context, q Queryer, vehicleID uuid.UUID) (map[string]bool, error) {
	rs, err := q.QueryContext(ctx, `SELECT external_id FROM charges WHERE vehicle_id = $1`, vehicleID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	m := map[string]bool{}
	for rs.Next() {
		var e string
		if err := rs.Scan(&e); err != nil {
			return nil, err
		}
		m[e] = true
	}
	return m, rs.Err()
}

// Plan is the classified candidates.
type Plan struct {
	Cap     []Row
	CapTo   map[uuid.UUID]time.Time
	BySoC   map[uuid.UUID]bool   // capped by the SoC rule (mode A)
	SoCOff  map[uuid.UUID]bool   // mode A rows whose stored kWh disagrees with the pre-drive SoC gain
	Splits  map[uuid.UUID]*Split // the capped rows that also create new rows
	Skipped map[string]int       // reason -> count (flagged reasons included)
	Flagged []Row
}

// MakePlan classifies rows at time now.
func MakePlan(rows []Row, now time.Time, opt Options) Plan {
	p := Plan{CapTo: map[uuid.UUID]time.Time{}, BySoC: map[uuid.UUID]bool{}, SoCOff: map[uuid.UUID]bool{}, Splits: map[uuid.UUID]*Split{}, Skipped: map[string]int{}}
	for _, r := range rows {
		v := Classify(r, now, opt)
		switch {
		case !v.CapTo.IsZero():
			p.Cap = append(p.Cap, r)
			p.CapTo[r.ID] = v.CapTo
			if v.BySoC {
				p.BySoC[r.ID] = true
				p.SoCOff[r.ID] = v.SoCOff
			}
			if v.Split != nil {
				p.Splits[r.ID] = v.Split
			}
		case v.Flag:
			p.Flagged = append(p.Flagged, r)
			p.Skipped[v.Reason]++
		default:
			p.Skipped[v.Reason]++
		}
	}
	return p
}

// IDs returns the ids to cap, sorted.
func (p Plan) IDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(p.Cap))
	for i, r := range p.Cap {
		ids[i] = r.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

// FlaggedIDs returns the flagged ids, sorted.
func (p Plan) FlaggedIDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(p.Flagged))
	for i, r := range p.Flagged {
		ids[i] = r.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

// Digest is the sha256 over the whole plan: for each capped row (sorted by id) its new ended_at and, for a split,
// the original's new end SoC, energy and cost and each new row's external id, start, end and kWh. A moved cap point, a
// different segment or a different energy figure changes it.
func (p Plan) Digest() string {
	ids := p.IDs()
	lines := make([]string, len(ids))
	for i, id := range ids {
		line := id.String() + " " + p.CapTo[id].UTC().Format(time.RFC3339Nano)
		if p.BySoC[id] {
			line += " mode=soc"
		}
		if sp := p.Splits[id]; sp != nil {
			cost := "-"
			if sp.OrigCost != nil {
				cost = fmt.Sprintf("%.4f", *sp.OrigCost)
			}
			line += fmt.Sprintf(" split soc=%.2f kwh=%.4f cost=%s state=%s max=%.3f avg=%.3f active=%.0f empty=%t clamped=%.4f", sp.OrigEndSoC, sp.OrigKWh, cost, sp.OrigFinalState, sp.OrigMaxKW, sp.OrigAvgKW, sp.OrigActiveSec, sp.EmptyOriginal, sp.ClampedKWh)
			for _, n := range sp.News {
				line += fmt.Sprintf(" | %s %s %s kwh=%.4f soc=%.2f-%.2f state=%s max=%.3f avg=%.3f active=%.0f cost=%.4f ppk=%.6f %s src=%s net=%s place=%s",
					n.ExternalID, n.Start.UTC().Format(time.RFC3339Nano), n.End.UTC().Format(time.RFC3339Nano), n.KWh, n.SoCStart, n.SoCEnd, n.FinalState, n.MaxPowerKW, n.AvgPowerKW, n.ActiveSeconds,
					n.Cost, n.PricePerKW, n.Currency, n.CostSource, n.Network, n.Place)
			}
		}
		lines[i] = line
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

// PricedBySource counts the new rows by the cost source the pricing step gave them ("unpriced" for those it left alone: a
// fast charge, or AC away from home).
func (p Plan) PricedBySource() map[string]int {
	m := map[string]int{}
	for _, sp := range p.Splits {
		for _, n := range sp.News {
			src := n.CostSource
			if src == "" {
				src = "unpriced"
			}
			m[src]++
		}
	}
	return m
}

// NewRowKinds counts the new rows by charge type, by peak power: DC fast (the recorder's DCFCMinKW) or L2/AC. The
// repair prices neither (the dry run reports it so the follow-up pricing step is sized).
func (p Plan) NewRowKinds() (l2, dc int) {
	for _, sp := range p.Splits {
		for _, n := range sp.News {
			if n.MaxPowerKW >= rivian.DCFCMinKW {
				dc++
			} else {
				l2++
			}
		}
	}
	return
}

// SoCModeTotals counts the rows capped by the SoC rule, and of those the ones whose stored kWh disagrees with the
// pre-drive SoC gain by more than 25% (capped all the same; their energy is not fixed by this repair).
func (p Plan) SoCModeTotals() (rows, disagree int) {
	for id := range p.BySoC {
		rows++
		if p.SoCOff[id] {
			disagree++
		}
	}
	return
}

// ClampTotals counts the splits whose new rows were scaled down to the stored energy, and the kWh clamped away.
func (p Plan) ClampTotals() (rows int, kwh float64) {
	for _, sp := range p.Splits {
		if sp.ClampedKWh > 0 {
			rows++
			kwh += sp.ClampedKWh
		}
	}
	return
}

// EmptyOriginals counts the splits whose original holds no charge before the first drive.
func (p Plan) EmptyOriginals() int {
	n := 0
	for _, sp := range p.Splits {
		if sp.EmptyOriginal {
			n++
		}
	}
	return n
}

// NewRowTypes counts the new rows as AC at home, AC away from home, AC with no place (no saved home) and DC fast.
func (p Plan) NewRowTypes() (acHome, acAway, acOther, dc int) {
	for _, sp := range p.Splits {
		for _, n := range sp.News {
			switch {
			case rivian.IsDCFCSession(n.MaxPowerKW, n.KWh, n.ActiveSeconds, n.End.Sub(n.Start)):
				dc++
			case n.Place == "home":
				acHome++
			case n.Place == "away":
				acAway++
			default:
				acOther++
			}
		}
	}
	return
}

// CostDelta is the change in cost the split makes: the originals' re-pricing by energy share (negative when they give
// energy away) and the cost of the new rows.
func (p Plan) CostDelta() (originals, newRows float64) {
	byID := map[uuid.UUID]Row{}
	for _, r := range p.Cap {
		byID[r.ID] = r
	}
	for id, sp := range p.Splits {
		if sp.OrigCost != nil {
			originals += *sp.OrigCost - byID[id].Cost
		}
		for _, n := range sp.News {
			newRows += n.Cost
		}
	}
	return
}

// SplitsByVehicle counts the split rows per vehicle id.
func (p Plan) SplitsByVehicle() map[uuid.UUID]int {
	m := map[uuid.UUID]int{}
	for _, r := range p.Cap {
		if p.Splits[r.ID] != nil {
			m[r.VehicleID]++
		}
	}
	return m
}

// IdleHoursInNewRows is the time the new rows span beyond their active charging (a segment that held a charge, a
// long idle and another charge becomes one row; its energy is right, its duration includes the idle).
func (p Plan) IdleHoursInNewRows() float64 {
	var h float64
	for _, sp := range p.Splits {
		for _, n := range sp.News {
			if d := n.End.Sub(n.Start).Seconds() - n.ActiveSeconds; d > 0 {
				h += d / 3600
			}
		}
	}
	return h
}

// SplitTotals is the number of rows split, the rows they create, and the energy re-attributed to the new rows.
func (p Plan) SplitTotals() (rows, created int, kwh float64) {
	for _, sp := range p.Splits {
		rows++
		created += len(sp.News)
		for _, n := range sp.News {
			kwh += n.KWh
		}
	}
	return
}

// ByVehicle counts the rows to cap per vehicle id.
func (p Plan) ByVehicle() map[uuid.UUID]int {
	m := map[uuid.UUID]int{}
	for _, r := range p.Cap {
		m[r.VehicleID]++
	}
	return m
}

// HoursRemoved is the total time the cap takes off the rows.
func (p Plan) HoursRemoved() float64 {
	var h float64
	for _, r := range p.Cap {
		h += r.EndedAt.Sub(p.CapTo[r.ID]).Hours()
	}
	return h
}

// Repriceable counts capped rows whose price came from the home rate or a network estimate: their cost may
// change with the window (time of use). The repair does not re-price; `rivolt reprice --ids` does, on the list
// the dry run writes.
func (p Plan) Repriceable() int {
	n := 0
	for _, r := range p.Cap {
		if r.CostSource == "home_rate" || r.CostSource == "network_estimate" {
			n++
		}
	}
	return n
}

// ErrDrift: the rows no longer match the approved dry run (nothing was changed).
var ErrDrift = errors.New("stretched: the rows to cap differ from the approved dry run; nothing changed")

// ErrOpenCharge: a truck is charging now; the repair does not run.
var ErrOpenCharge = errors.New("stretched: a live charge was written in the last 15 minutes; run in a quiet hour")

// Apply caps (and splits) in ONE transaction. It locks and re-selects the candidates, re-applies the rule at the
// current time, and aborts unless the count AND the digest equal the approved dry run's. The old rows (to_jsonb) go
// to sink (the durable CSV; an error aborts before anything is changed) and to charges_capped_backup. Each update
// asserts the cap is earlier than the old ended_at and not before started_at, and touches exactly one row; a split
// asserts the energy is conserved. New rows are plain inserts (no hooks, pushes or events fire) and are listed in the
// backup row so the undo deletes exactly them.
func Apply(ctx context.Context, d *sql.DB, batchID uuid.UUID, wantCount int, wantDigest, reason string, allowOpen bool, opt Options, sink func([]phantom.BackupRow) error) (int, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if !allowOpen {
		if n, err := blips.OpenCharges(ctx, tx); err != nil {
			return 0, err
		} else if n > 0 {
			return 0, fmt.Errorf("%w (%d open)", ErrOpenCharge, n)
		}
	}
	rows, err := Select(ctx, tx, true, opt.Split || opt.CapBySoC)
	if err != nil {
		return 0, err
	}
	plan := MakePlan(rows, time.Now(), opt)
	ids := plan.IDs()
	if len(ids) != wantCount || plan.Digest() != wantDigest {
		return 0, fmt.Errorf("%w: found %d (sha256 %s), approved %d (sha256 %s)", ErrDrift, len(ids), plan.Digest(), wantCount, wantDigest)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	var backup []phantom.BackupRow
	for _, r := range plan.Cap {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT to_jsonb(c.*) FROM charges c WHERE c.id = $1`, r.ID).Scan(&raw); err != nil {
			return 0, err
		}
		backup = append(backup, phantom.BackupRow{Kind: "charge_cap", ChargeID: r.ID, UserID: r.UserID, Row: json.RawMessage(raw)})
	}
	if sink != nil {
		if err := sink(backup); err != nil {
			return 0, fmt.Errorf("backup sink: %w", err)
		}
	}
	for _, b := range backup {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO charges_capped_backup (batch_id, charge_id, user_id, row, capped_to, reason)
			VALUES ($1,$2,$3,$4::jsonb,$5,$6)`, batchID, b.ChargeID, b.UserID, string(b.Row), plan.CapTo[b.ChargeID], reason); err != nil {
			return 0, err
		}
	}
	mark := fmt.Sprintf(`{"stretched_repair":{"batch":%q}}`, batchID.String())
	n := 0
	for _, r := range plan.Cap {
		to := plan.CapTo[r.ID]
		sp := plan.Splits[r.ID]
		// The assertions are in the statement: never later than the old end, never before the start.
		var res sql.Result
		if sp == nil {
			res, err = tx.ExecContext(ctx, `
				UPDATE charges SET ended_at = $2, metadata = metadata || $4::jsonb, updated_at = now()
				 WHERE id = $1 AND source = 'live' AND ended_at = $3 AND $2 < ended_at AND $2 >= started_at`, r.ID, to, r.EndedAt, mark)
		} else {
			// Energy is conserved: what the original keeps plus what the new rows take is what it stored.
			var moved float64
			for _, nr := range sp.News {
				moved += nr.KWh
			}
			if diff := sp.OrigKWh + moved - r.EnergyKWh; diff > 1e-6 || diff < -1e-6 || sp.OrigKWh < 0 {
				return 0, fmt.Errorf("stretched: row %s energy not conserved (keeps %.4f + moves %.4f vs stored %.4f); rolling back", r.ID, sp.OrigKWh, moved, r.EnergyKWh)
			}
			var cost any // nil keeps the stored cost
			if sp.OrigCost != nil {
				cost = *sp.OrigCost
			}
			res, err = tx.ExecContext(ctx, `
				UPDATE charges SET ended_at = $2, end_soc_pct = $5, energy_added_kwh = $6, cost = COALESCE($7::numeric, cost),
				       final_state = $8, max_power_kw = NULLIF($9::float8, 0), avg_power_kw = NULLIF($10::float8, 0), active_seconds = NULLIF($11::float8, 0),
				       metadata = metadata || $4::jsonb, updated_at = now()
				 WHERE id = $1 AND source = 'live' AND ended_at = $3 AND $2 < ended_at AND $2 >= started_at`,
				r.ID, to, r.EndedAt, mark, sp.OrigEndSoC, sp.OrigKWh, cost, sp.OrigFinalState, sp.OrigMaxKW, sp.OrigAvgKW, sp.OrigActiveSec)
		}
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k != 1 {
			return 0, fmt.Errorf("stretched: row %s changed %d rows; rolling back", r.ID, k)
		}
		if sp != nil {
			created := make([]string, 0, len(sp.News))
			for _, nr := range sp.News {
				var id uuid.UUID
				// Priced and placed by Options.Price in the plan (never a copied home_rate); unpriced where it declined.
				if err := tx.QueryRowContext(ctx, `
					INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, start_soc_pct, end_soc_pct,
					                     energy_added_kwh, max_power_kw, avg_power_kw, final_state, lat, lon, source, active_seconds, metadata,
					                     cost, price_per_kwh, currency, cost_source, network, place)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9::float8,0),NULLIF($10::float8,0),$11,NULLIF($12::float8,0),NULLIF($13::float8,0),'live',NULLIF($14::float8,0),$15::jsonb,
					        NULLIF($16::numeric,0), NULLIF($17::numeric,0), NULLIF($18,''), NULLIF($19,''), NULLIF($20,''), NULLIF($21,''))
					RETURNING id`,
					r.UserID, r.VehicleID, nr.ExternalID, nr.Start, nr.End, nr.SoCStart, nr.SoCEnd,
					nr.KWh, nr.MaxPowerKW, nr.AvgPowerKW, nr.FinalState, nr.Lat, nr.Lon, nr.ActiveSeconds, mark,
					nr.Cost, nr.PricePerKW, nr.Currency, nr.CostSource, nr.Network, nr.Place).Scan(&id); err != nil {
					return 0, fmt.Errorf("stretched: creating %s: %w", nr.ExternalID, err)
				}
				created = append(created, id.String())
			}
			if _, err := tx.ExecContext(ctx, `UPDATE charges_capped_backup SET created_ids = $3::uuid[] WHERE batch_id = $1 AND charge_id = $2`,
				batchID, r.ID, "{"+strings.Join(created, ",")+"}"); err != nil {
				return 0, err
			}
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// Blocker is a reason a batch cannot be restored cleanly. Nothing is restored while any exists.
type Blocker struct {
	ChargeID uuid.UUID
	Reason   string
}

// restoreGroup is one original row of a batch and the rows its split created.
type restoreGroup struct {
	charge  uuid.UUID
	created []string
}

// lockRestore locks, in the caller's transaction, every original and created row of the batch (FOR UPDATE, ordered by
// id) and returns the groups with the reasons any of them cannot be restored: the original's ended_at is no longer the
// capped value, or a person has built on the original or on a created row since the repair (a user price, place or
// location override, note, tag, place correction or receipt). The repair refused such rows beforehand, so any of
// these on the original is a later edit.
func lockRestore(ctx context.Context, tx *sql.Tx, batchID uuid.UUID) ([]restoreGroup, []Blocker, error) {
	rs, err := tx.QueryContext(ctx, `SELECT charge_id, COALESCE(array_to_string(created_ids, ','), '') FROM charges_capped_backup WHERE batch_id = $1 AND restored_at IS NULL ORDER BY charge_id`, batchID)
	if err != nil {
		return nil, nil, err
	}
	var groups []restoreGroup
	for rs.Next() {
		var g restoreGroup
		var created string
		if err := rs.Scan(&g.charge, &created); err != nil {
			rs.Close()
			return nil, nil, err
		}
		if created != "" {
			g.created = strings.Split(created, ",")
		}
		groups = append(groups, g)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, nil, err
	}
	var blockers []Blocker
	for _, g := range groups {
		ids := append([]string{g.charge.String()}, g.created...)
		arr := "{" + strings.Join(ids, ",") + "}"
		// Lock first, then judge: no concurrent edit can slip between the check and the writes.
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM charges WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`, arr); err != nil {
			return nil, nil, err
		}
		var intact int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM charges c JOIN charges_capped_backup b ON b.charge_id = c.id
			 WHERE b.batch_id = $1 AND b.charge_id = $2 AND c.ended_at = b.capped_to`, batchID, g.charge).Scan(&intact); err != nil {
			return nil, nil, err
		}
		if intact != 1 {
			blockers = append(blockers, Blocker{g.charge, "the original's ended_at changed since the repair (or the row is gone)"})
		}
		var touched int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM charges x
			 WHERE x.id = ANY($1::uuid[])
			   AND (COALESCE(x.cost_source,'') = 'user' OR x.place_override IS NOT NULL OR x.location_override IS NOT NULL
			        OR COALESCE(x.notes,'') <> '' OR cardinality(x.tags) > 0
			        OR EXISTS (SELECT 1 FROM charge_restamps r WHERE r.charge_id = x.id)
			        OR EXISTS (SELECT 1 FROM rivian_receipts rr WHERE rr.charge_id = x.id))`, arr).Scan(&touched); err != nil {
			return nil, nil, err
		}
		if touched > 0 {
			blockers = append(blockers, Blocker{g.charge, "a person has built on the original or a created row since the repair"})
		}
	}
	return groups, blockers, nil
}

// RestoreBlockers lists the reasons the batch cannot be restored, for the restore dry run. It writes nothing (the
// locks are released by the rollback).
func RestoreBlockers(ctx context.Context, d *sql.DB, batchID uuid.UUID) ([]Blocker, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, blockers, err := lockRestore(ctx, tx, batchID)
	return blockers, err
}

// ErrRestoreBlocked: some group of the batch cannot be restored; nothing was changed.
var ErrRestoreBlocked = errors.New("stretched: the batch cannot be restored cleanly; nothing changed")

// Restore undoes a batch from charges_capped_backup in ONE transaction, all or nothing: every original and created
// row is locked first; if any group is blocked (see lockRestore) the transaction rolls back with ErrRestoreBlocked
// and nothing changes, and any statement that does not behave (a delete or an update that touches an unexpected
// number of rows) rolls everything back too. Then the created rows are deleted (the original's old end overlaps them,
// charges_live_no_overlap) and the original's changed columns go back: ended_at, end_soc_pct, energy_added_kwh,
// cost, final_state and the power figures, and metadata with only the repair's own key removed, so keys added since
// survive. updated_at is touched by the table's trigger on every update, so it reads the restore time (the old value
// stays in the backup row). A batch with nothing left to restore is refused.
func Restore(ctx context.Context, d *sql.DB, batchID uuid.UUID) (int, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	groups, blockers, err := lockRestore(ctx, tx, batchID)
	if err != nil {
		return 0, err
	}
	if len(groups) == 0 {
		return 0, fmt.Errorf("stretched: batch %s has nothing to restore (unknown or already restored)", batchID)
	}
	if len(blockers) > 0 {
		return 0, fmt.Errorf("%w: %d blocker(s), first: %s (%s)", ErrRestoreBlocked, len(blockers), blockers[0].ChargeID, blockers[0].Reason)
	}
	restored := 0
	for _, g := range groups {
		if len(g.created) > 0 {
			res, err := tx.ExecContext(ctx, `DELETE FROM charges WHERE id = ANY($1::uuid[])`, "{"+strings.Join(g.created, ",")+"}")
			if err != nil {
				return 0, err
			}
			if k, _ := res.RowsAffected(); int(k) != len(g.created) {
				return 0, fmt.Errorf("stretched: deleted %d of %d created rows for %s; rolling back", k, len(g.created), g.charge)
			}
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE charges c
			   SET ended_at = (b.row->>'ended_at')::timestamptz,
			       end_soc_pct = (b.row->>'end_soc_pct')::double precision,
			       energy_added_kwh = (b.row->>'energy_added_kwh')::double precision,
			       cost = (b.row->>'cost')::numeric,
			       final_state = b.row->>'final_state',
			       max_power_kw = (b.row->>'max_power_kw')::double precision,
			       avg_power_kw = (b.row->>'avg_power_kw')::double precision,
			       active_seconds = (b.row->>'active_seconds')::double precision,
			       metadata = c.metadata - 'stretched_repair'
			  FROM charges_capped_backup b
			 WHERE b.batch_id = $1 AND b.charge_id = $2 AND c.id = b.charge_id AND c.ended_at = b.capped_to`, batchID, g.charge)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k != 1 {
			return 0, fmt.Errorf("stretched: restoring %s changed %d rows; rolling back", g.charge, k)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE charges_capped_backup SET restored_at = now() WHERE batch_id = $1 AND charge_id = $2`, batchID, g.charge); err != nil {
			return 0, err
		}
		restored++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return restored, nil
}
