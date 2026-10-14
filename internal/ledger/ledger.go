// Package ledger builds the server-side money statement (the Money tab
// and, eventually, every cost/energy total in the SPA) from recorded
// charges and drives.
//
// Everything here is read-only and pure: Build takes rows already
// loaded from Postgres (see Load) and returns the response. Three
// rules keep the numbers honest:
//
//   - Charges are the ledger. "Spend" is what was paid for energy;
//     driving cost is spend ÷ miles, never a sum of per-drive guesses.
//   - Overlapping duplicates (two recorders on one vehicle, or an
//     import overlapping live data) are resolved at query time and
//     excluded from every total. No rows are modified.
//   - Unknown stays unknown: unpriced sessions, missing energy and
//     inferred provenance are counted and surfaced, not papered over.
//
// The wire contract lives in the Money tab's ledger-contract.md (v1).
package ledger

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/charges"
)

// Version is the response contract version. Additive changes keep it;
// breaking changes bump it.
const Version = 1

// FastMinKW is the session peak at or above which a charge is DC fast
// charging. Same threshold as analytics.FastMinKW (the Charges page's
// "Fast" bucket) so the two surfaces agree.
const FastMinKW = 50.0

// HomeRadiusKM is how close to the saved home location a session must
// start to count as "home". Matches the ~200 m DBSCAN epsilon the
// Charges page uses, with a little GPS slack.
const HomeRadiusKM = 0.25

// homeRateTolerance is how close a persisted $/kWh must be to the
// configured home rate to be attributed to it. price_per_kwh is
// numeric(12,6), so anything stamped from the setting matches exactly;
// the slack only absorbs float conversion.
const homeRateTolerance = 1e-4

// padding is how far beyond the month the loader reaches so dedupe and
// plug-in grouping see sessions that straddle the boundary.
const padding = 72 * time.Hour

// Cost sources. v1 infers them (there is no persisted provenance yet).
const (
	SourceRivian   = "rivian"
	SourceHomeRate = "home_rate"
	SourceUnknown  = "unknown"
	SourceMixed    = "mixed"
)

// Flags on receipts and sessions.
const (
	FlagFragmentsFolded    = "fragments_folded"
	FlagHomeRateOnDCFC     = "home_rate_on_dcfc"
	FlagHomeRateAway       = "home_rate_away"
	FlagUnpriced           = "unpriced"
	FlagNullEnergy         = "null_energy"
	FlagOpenSession        = "open_session"
	FlagAbandoned          = "abandoned"
	FlagContinuesNextMonth = "continues_next_month"
	FlagContinuedFromPrev  = "continued_from_previous_month"
	FlagCurrencyMismatch   = "currency_mismatch"
)

// Charge is the projection of a charges row the ledger needs. Pointer
// fields are nullable columns: nil means "not recorded", which the
// ledger keeps distinct from zero.
type Charge struct {
	ID          string // charges.external_id, the id /api/charges exposes
	VehicleID   string // Rivian vehicle id
	StartedAt   time.Time
	EndedAt     time.Time
	StartSoC    *float64
	EndSoC      *float64
	EnergyKWh   *float64
	PeakKW      *float64
	Cost        *float64
	Currency    string
	PricePerKWh *float64
	FinalState  string
	Source      string
	Lat, Lon    *float64
	CreatedAt   time.Time
}

// Drive is the projection of a drives row the ledger needs.
type Drive struct {
	ID         string
	VehicleID  string
	StartedAt  time.Time
	EndedAt    time.Time
	DistanceMi *float64
	EnergyKWh  *float64
	Source     string
	CreatedAt  time.Time
}

// LatLon is a WGS84 point.
type LatLon struct{ Lat, Lon float64 }

// Config is the per-user context the statement is priced in.
type Config struct {
	// Currency is the user's home currency (ISO-4217). Sessions in
	// another currency are excluded from spend and flagged.
	Currency string
	// HomeRate is the user's configured home $/kWh; 0 = not set.
	HomeRate float64
	// Home is the saved home location; nil = not set.
	Home *LatLon
}

// Period is one statement month in a location.
type Period struct {
	Month string // YYYY-MM
	Loc   *time.Location
	Start time.Time // inclusive, UTC instant
	End   time.Time // exclusive, UTC instant
}

// ParsePeriod parses YYYY-MM in loc.
func ParsePeriod(month string, loc *time.Location) (Period, error) {
	if loc == nil {
		loc = time.UTC
	}
	t, err := time.ParseInLocation("2006-01", strings.TrimSpace(month), loc)
	if err != nil {
		return Period{}, fmt.Errorf("month must be YYYY-MM: %w", err)
	}
	return periodFrom(t, loc), nil
}

func periodFrom(startLocal time.Time, loc *time.Location) Period {
	start := time.Date(startLocal.Year(), startLocal.Month(), 1, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 1, 0)
	return Period{Month: start.Format("2006-01"), Loc: loc, Start: start.UTC(), End: end.UTC()}
}

// LastMonths returns the n months ending with the one containing now,
// oldest first.
func LastMonths(now time.Time, n int, loc *time.Location) []Period {
	if loc == nil {
		loc = time.UTC
	}
	cur := now.In(loc)
	first := time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, loc)
	out := make([]Period, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, periodFrom(first.AddDate(0, -i, 0), loc))
	}
	return out
}

// LoadWindow is the [from, to) range Load must cover for the periods,
// padded so boundary-straddling sessions are seen.
func LoadWindow(ps ...Period) (time.Time, time.Time) {
	var from, to time.Time
	for i, p := range ps {
		if i == 0 || p.Start.Before(from) {
			from = p.Start
		}
		if i == 0 || p.End.After(to) {
			to = p.End
		}
	}
	return from.Add(-padding), to.Add(padding)
}

// ---- response types -------------------------------------------------

// Monthly is the GET /api/ledger/monthly response.
type Monthly struct {
	Version   int     `json:"version"`
	Period    string  `json:"period"`
	TZ        string  `json:"tz"`
	TZSource  string  `json:"tz_source"`
	Range     Range   `json:"range"`
	Currency  string  `json:"currency"`
	VehicleID *string `json:"vehicle_id"`

	IsCurrentMonth  bool       `json:"is_current_month"`
	FirstActivityAt *time.Time `json:"first_activity_at"`
	LastActivityAt  *time.Time `json:"last_activity_at"`

	Spend       Spend       `json:"spend"`
	Energy      Energy      `json:"energy"`
	Driving     Driving     `json:"driving"`
	Receipts    []Receipt   `json:"receipts"`
	DataQuality DataQuality `json:"data_quality"`
}

// MonthSummary is a Monthly without receipts, for
// GET /api/ledger/summary. The outer Receipts field shadows the
// embedded one in JSON and is always empty, so the key is omitted.
type MonthSummary struct {
	Monthly
	Receipts []Receipt `json:"receipts,omitempty"`
}

// Range is a [start, end) UTC interval.
type Range struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Spend is money paid for energy.
type Spend struct {
	Total                    float64            `json:"total"`
	BySource                 map[string]float64 `json:"by_source"`
	PricedSessions           int                `json:"priced_sessions"`
	UnpricedSessions         int                `json:"unpriced_sessions"`
	UnpricedKWh              float64            `json:"unpriced_kwh"`
	UnpricedHomeRateEstimate *float64           `json:"unpriced_home_rate_estimate"`
}

// Energy is energy added by charging.
type Energy struct {
	Sessions           int                 `json:"sessions"`
	KWhAdded           float64             `json:"kwh_added"`
	NullEnergySessions int                 `json:"null_energy_sessions"`
	ByKind             map[string]KindLine `json:"by_kind"`
}

// KindLine is one by_kind bucket.
type KindLine struct {
	Sessions int      `json:"sessions"`
	KWh      float64  `json:"kwh"`
	Cost     float64  `json:"cost"`
	PeakKW   *float64 `json:"peak_kw"`
}

// Driving summarises drives and the per-mile allocation of spend.
type Driving struct {
	Drives           int      `json:"drives"`
	Miles            float64  `json:"miles"`
	MilesWithEnergy  float64  `json:"miles_with_energy"`
	KWhUsedKnown     float64  `json:"kwh_used_known"`
	Coverage         *float64 `json:"coverage"`
	CostPerMile      *float64 `json:"cost_per_mile"`
	CostPerMileBasis string   `json:"cost_per_mile_basis"`
}

// Receipt is one plug-in: consecutive charges with no drive between.
type Receipt struct {
	ReceiptID        string           `json:"receipt_id"`
	VehicleID        string           `json:"vehicle_id"`
	Kind             string           `json:"kind"`
	Place            string           `json:"place"`
	KMFromHome       *float64         `json:"km_from_home"`
	StartedAt        time.Time        `json:"started_at"`
	EndedAt          time.Time        `json:"ended_at"`
	LocalDate        string           `json:"local_date"`
	SpansDays        int              `json:"spans_days"`
	SoCStart         *float64         `json:"soc_start"`
	SoCEnd           *float64         `json:"soc_end"`
	KWh              float64          `json:"kwh"`
	PeakKW           *float64         `json:"peak_kw"`
	Cost             *float64         `json:"cost"`
	CostSource       string           `json:"cost_source"`
	Rate             *float64         `json:"rate"`
	SessionCount     int              `json:"session_count"`
	UnpricedSessions int              `json:"unpriced_sessions"`
	Flags            []string         `json:"flags"`
	Sessions         []ReceiptSession `json:"sessions"`
}

// ReceiptSession is one charge inside a receipt.
type ReceiptSession struct {
	ChargeID    string    `json:"charge_id"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	SoCStart    *float64  `json:"soc_start"`
	SoCEnd      *float64  `json:"soc_end"`
	KWh         *float64  `json:"kwh"`
	PeakKW      *float64  `json:"peak_kw"`
	Cost        *float64  `json:"cost"`
	PricePerKWh *float64  `json:"price_per_kwh"`
	CostSource  string    `json:"cost_source"`
	FinalState  string    `json:"final_state"`
	Source      string    `json:"source"`
	Flags       []string  `json:"flags"`
}

// DataQuality carries the counts behind the Money tab's quality notes.
type DataQuality struct {
	CostSourceInferred       bool         `json:"cost_source_inferred"`
	SupersededExcluded       int          `json:"superseded_excluded"`
	Superseded               []Superseded `json:"superseded,omitempty"`
	DrivesSupersededExcluded int          `json:"drives_superseded_excluded"`
	OpenSessions             int          `json:"open_sessions"`
	HomeRateOnDCFC           int          `json:"home_rate_on_dcfc"`
	HomeRateAway             int          `json:"home_rate_away"`
	CurrencyMismatchExcluded int          `json:"currency_mismatch_excluded"`
}

// Superseded names a duplicate excluded in favour of its winner.
type Superseded struct {
	ChargeID     string `json:"charge_id"`
	SupersededBy string `json:"superseded_by"`
}

// ---- dedupe ---------------------------------------------------------

type interval struct {
	vehicle    string
	start, end time.Time
}

func (a interval) overlaps(b interval) bool {
	return a.vehicle == b.vehicle && a.start.Before(b.end) && b.start.Before(a.end)
}

// EffectiveCharges resolves overlapping duplicates per vehicle. Sessions
// are accepted in priority order (live over imports, then has energy,
// has a cost, longer, created earlier, id); a session overlapping one
// already accepted is superseded by it. Output is sorted by start.
func EffectiveCharges(cs []Charge) ([]Charge, []Superseded) {
	order := append([]Charge(nil), cs...)
	sort.SliceStable(order, func(i, j int) bool { return chargeBeats(order[i], order[j]) })
	var kept []Charge
	var keptIv []interval
	var lost []Superseded
	for _, c := range order {
		iv := interval{c.VehicleID, c.StartedAt, c.EndedAt}
		winner := ""
		for k, other := range keptIv {
			if iv.overlaps(other) {
				winner = kept[k].ID
				break
			}
		}
		if winner != "" {
			lost = append(lost, Superseded{ChargeID: c.ID, SupersededBy: winner})
			continue
		}
		kept = append(kept, c)
		keptIv = append(keptIv, iv)
	}
	sortCharges(kept)
	sort.Slice(lost, func(i, j int) bool { return lost[i].ChargeID < lost[j].ChargeID })
	return kept, lost
}

func chargeBeats(a, b Charge) bool {
	if x, y := a.Source == "live", b.Source == "live"; x != y {
		return x
	}
	if x, y := positive(a.EnergyKWh), positive(b.EnergyKWh); x != y {
		return x
	}
	if x, y := positive(a.Cost), positive(b.Cost); x != y {
		return x
	}
	if da, db := a.EndedAt.Sub(a.StartedAt), b.EndedAt.Sub(b.StartedAt); da != db {
		return da > db
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// EffectiveDrives is EffectiveCharges for drives. Returns the kept
// drives sorted by start and, for each superseded drive id, its winner.
func EffectiveDrives(ds []Drive) ([]Drive, map[string]string) {
	order := append([]Drive(nil), ds...)
	sort.SliceStable(order, func(i, j int) bool { return driveBeats(order[i], order[j]) })
	var kept []Drive
	var keptIv []interval
	lost := map[string]string{}
	for _, d := range order {
		iv := interval{d.VehicleID, d.StartedAt, d.EndedAt}
		winner := ""
		for k, other := range keptIv {
			if iv.overlaps(other) {
				winner = kept[k].ID
				break
			}
		}
		if winner != "" {
			lost[d.ID] = winner
			continue
		}
		kept = append(kept, d)
		keptIv = append(keptIv, iv)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if !kept[i].StartedAt.Equal(kept[j].StartedAt) {
			return kept[i].StartedAt.Before(kept[j].StartedAt)
		}
		return kept[i].ID < kept[j].ID
	})
	return kept, lost
}

func driveBeats(a, b Drive) bool {
	if x, y := a.Source == "live", b.Source == "live"; x != y {
		return x
	}
	if x, y := positive(a.EnergyKWh), positive(b.EnergyKWh); x != y {
		return x
	}
	if da, db := a.EndedAt.Sub(a.StartedAt), b.EndedAt.Sub(b.StartedAt); da != db {
		return da > db
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// ---- classification -------------------------------------------------

// CostSource infers where a session's persisted cost came from. With no
// provenance column yet: no cost → unknown; a rate equal to the user's
// current home rate → home_rate; anything else priced → rivian (which
// in practice also covers manual edits and older home rates).
func CostSource(c Charge, homeRate float64) string {
	if !positive(c.Cost) {
		return SourceUnknown
	}
	if homeRate > 0 && c.PricePerKWh != nil && math.Abs(*c.PricePerKWh-homeRate) <= homeRateTolerance {
		return SourceHomeRate
	}
	return SourceRivian
}

func isDCFC(c Charge) bool { return c.PeakKW != nil && *c.PeakKW >= FastMinKW }

func kindOf(c Charge) string {
	if isDCFC(c) {
		return "dcfc"
	}
	return "ac"
}

// place returns home/away/unknown and the distance in km (nil when
// unknown).
func place(c Charge, home *LatLon) (string, *float64) {
	if home == nil || c.Lat == nil || c.Lon == nil || (*c.Lat == 0 && *c.Lon == 0) {
		return "unknown", nil
	}
	km := haversineKM(home.Lat, home.Lon, *c.Lat, *c.Lon)
	km = math.Round(km*100) / 100
	if km <= HomeRadiusKM {
		return "home", &km
	}
	return "away", &km
}

func isOpen(finalState string) bool {
	return charges.IsOpenLiveFinalState(strings.ToLower(strings.TrimSpace(finalState)))
}

func currencyMatches(c Charge, cfg Config) bool {
	return c.Currency == "" || cfg.Currency == "" || strings.EqualFold(c.Currency, cfg.Currency)
}

// sessionView is a charge with everything Build derives for it.
type sessionView struct {
	c       Charge
	source  string
	place   string
	kmHome  *float64
	flags   []string
	spendOK bool // counts toward spend (priced, same currency)
}

func viewOf(c Charge, cfg Config) sessionView {
	v := sessionView{c: c, source: CostSource(c, cfg.HomeRate)}
	v.place, v.kmHome = place(c, cfg.Home)
	if !currencyMatches(c, cfg) {
		v.flags = append(v.flags, FlagCurrencyMismatch)
	}
	if v.source == SourceUnknown {
		v.flags = append(v.flags, FlagUnpriced)
	}
	if !positive(c.EnergyKWh) {
		v.flags = append(v.flags, FlagNullEnergy)
	}
	if isOpen(c.FinalState) {
		v.flags = append(v.flags, FlagOpenSession)
	}
	if strings.EqualFold(c.FinalState, "abandoned") {
		v.flags = append(v.flags, FlagAbandoned)
	}
	if v.source == SourceHomeRate && isDCFC(c) {
		v.flags = append(v.flags, FlagHomeRateOnDCFC)
	}
	if v.source == SourceHomeRate && v.place == "away" {
		v.flags = append(v.flags, FlagHomeRateAway)
	}
	v.spendOK = v.source != SourceUnknown && currencyMatches(c, cfg)
	sort.Strings(v.flags)
	return v
}

// ---- build ----------------------------------------------------------

// Input is everything Build needs for one or more months: rows loaded
// over LoadWindow(periods...).
type Input struct {
	Charges []Charge
	Drives  []Drive
	Config  Config
	Now     time.Time
}

// Build produces the statement for p. withReceipts=false yields the
// month summary shape (no receipts, no superseded list).
func Build(in Input, p Period, withReceipts bool) Monthly {
	effCharges, lost := EffectiveCharges(in.Charges)
	drives, lostDrives := EffectiveDrives(in.Drives)

	out := Monthly{
		Version:        Version,
		Period:         p.Month,
		TZ:             p.Loc.String(),
		Range:          Range{Start: p.Start, End: p.End},
		Currency:       in.Config.Currency,
		IsCurrentMonth: !in.Now.Before(p.Start) && in.Now.Before(p.End),
		Spend: Spend{
			BySource: map[string]float64{SourceRivian: 0, SourceHomeRate: 0},
		},
		Energy: Energy{
			ByKind: map[string]KindLine{"ac": {}, "dcfc": {}},
		},
		Driving:     Driving{CostPerMileBasis: "spend_over_miles"},
		DataQuality: DataQuality{CostSourceInferred: true},
	}
	inMonth := func(t time.Time) bool { return !t.Before(p.Start) && t.Before(p.End) }
	noteActivity := func(t time.Time) {
		t = t.UTC()
		if out.FirstActivityAt == nil || t.Before(*out.FirstActivityAt) {
			tt := t
			out.FirstActivityAt = &tt
		}
		if out.LastActivityAt == nil || t.After(*out.LastActivityAt) {
			tt := t
			out.LastActivityAt = &tt
		}
	}

	// Charges: totals.
	var unpricedEstimate float64
	for _, c := range effCharges {
		if !inMonth(c.StartedAt) {
			continue
		}
		noteActivity(c.StartedAt)
		v := viewOf(c, in.Config)
		kwh := val(c.EnergyKWh)

		out.Energy.Sessions++
		out.Energy.KWhAdded += kwh
		if !positive(c.EnergyKWh) {
			out.Energy.NullEnergySessions++
		}
		kl := out.Energy.ByKind[kindOf(c)]
		kl.Sessions++
		kl.KWh += kwh
		if c.PeakKW != nil && (kl.PeakKW == nil || *c.PeakKW > *kl.PeakKW) {
			pk := *c.PeakKW
			kl.PeakKW = &pk
		}

		switch {
		case hasFlag(v.flags, FlagCurrencyMismatch):
			out.DataQuality.CurrencyMismatchExcluded++
		case v.spendOK:
			cost := *c.Cost
			out.Spend.Total += cost
			out.Spend.BySource[v.source] += cost
			out.Spend.PricedSessions++
			kl.Cost += cost
		default:
			out.Spend.UnpricedSessions++
			out.Spend.UnpricedKWh += kwh
			unpricedEstimate += kwh * in.Config.HomeRate
		}
		out.Energy.ByKind[kindOf(c)] = kl

		if hasFlag(v.flags, FlagOpenSession) {
			out.DataQuality.OpenSessions++
		}
		if hasFlag(v.flags, FlagHomeRateOnDCFC) {
			out.DataQuality.HomeRateOnDCFC++
		}
		if hasFlag(v.flags, FlagHomeRateAway) {
			out.DataQuality.HomeRateAway++
		}
	}
	if in.Config.HomeRate > 0 {
		est := round4(unpricedEstimate)
		out.Spend.UnpricedHomeRateEstimate = &est
	}
	for _, s := range lost {
		if c, ok := findCharge(in.Charges, s.ChargeID); ok && inMonth(c.StartedAt) {
			out.DataQuality.SupersededExcluded++
			if withReceipts {
				out.DataQuality.Superseded = append(out.DataQuality.Superseded, s)
			}
		}
	}

	// Drives.
	for _, d := range drives {
		if !inMonth(d.StartedAt) {
			continue
		}
		noteActivity(d.StartedAt)
		out.Driving.Drives++
		mi := val(d.DistanceMi)
		out.Driving.Miles += mi
		if positive(d.EnergyKWh) {
			out.Driving.MilesWithEnergy += mi
			out.Driving.KWhUsedKnown += *d.EnergyKWh
		}
	}
	for id := range lostDrives {
		for _, d := range in.Drives {
			if d.ID == id && inMonth(d.StartedAt) {
				out.DataQuality.DrivesSupersededExcluded++
				break
			}
		}
	}
	if out.Driving.Miles > 0 {
		cov := round4(out.Driving.MilesWithEnergy / out.Driving.Miles)
		out.Driving.Coverage = &cov
		if out.Spend.Total > 0 {
			cpm := round4(out.Spend.Total / out.Driving.Miles)
			out.Driving.CostPerMile = &cpm
		}
	}

	if withReceipts {
		out.Receipts = buildReceipts(effCharges, drives, in.Config, p)
		if out.Receipts == nil {
			out.Receipts = []Receipt{}
		}
	}
	roundMonthly(&out)
	return out
}

// buildReceipts groups the effective charges (over the padded window)
// into plug-ins and returns the receipts for p, newest first.
func buildReceipts(effCharges []Charge, drives []Drive, cfg Config, p Period) []Receipt {
	byVehicle := map[string][]Charge{}
	for _, c := range effCharges {
		byVehicle[c.VehicleID] = append(byVehicle[c.VehicleID], c)
	}
	drivesByVehicle := map[string][]Drive{}
	for _, d := range drives {
		drivesByVehicle[d.VehicleID] = append(drivesByVehicle[d.VehicleID], d)
	}
	var out []Receipt
	vehicles := make([]string, 0, len(byVehicle))
	for v := range byVehicle {
		vehicles = append(vehicles, v)
	}
	sort.Strings(vehicles)
	for _, v := range vehicles {
		cs := byVehicle[v]
		sortCharges(cs)
		var group []Charge
		flush := func() {
			if r, ok := receiptFor(group, cfg, p); ok {
				out = append(out, r)
			}
			group = nil
		}
		for _, c := range cs {
			if len(group) > 0 && !samePlugIn(group[len(group)-1], c, drivesByVehicle[v]) {
				flush()
			}
			group = append(group, c)
		}
		flush()
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ReceiptID < out[j].ReceiptID
	})
	return out
}

// samePlugIn reports whether next continues prev's plug-in: no drive
// started between them and, when both are located, they're within
// HomeRadiusKM of each other.
func samePlugIn(prev, next Charge, drives []Drive) bool {
	for _, d := range drives {
		if d.StartedAt.After(prev.StartedAt) && d.StartedAt.Before(next.StartedAt) {
			return false
		}
	}
	if prev.Lat != nil && prev.Lon != nil && next.Lat != nil && next.Lon != nil &&
		!(*prev.Lat == 0 && *prev.Lon == 0) && !(*next.Lat == 0 && *next.Lon == 0) {
		if haversineKM(*prev.Lat, *prev.Lon, *next.Lat, *next.Lon) > HomeRadiusKM {
			return false
		}
	}
	return true
}

func receiptFor(group []Charge, cfg Config, p Period) (Receipt, bool) {
	var in []Charge
	before, after := false, false
	for _, c := range group {
		switch {
		case c.StartedAt.Before(p.Start):
			before = true
		case !c.StartedAt.Before(p.End):
			after = true
		default:
			in = append(in, c)
		}
	}
	if len(in) == 0 {
		return Receipt{}, false
	}
	first, last := in[0], in[len(in)-1]
	r := Receipt{
		ReceiptID:    "rcpt_" + first.ID,
		VehicleID:    first.VehicleID,
		Kind:         "ac",
		StartedAt:    first.StartedAt.UTC(),
		SoCStart:     first.StartSoC,
		SoCEnd:       last.EndSoC,
		SessionCount: len(in),
	}
	r.Place, r.KMFromHome = place(first, cfg.Home)

	flagSet := map[string]bool{}
	sources := map[string]bool{}
	var cost, pricedKWh float64
	priced := false
	end := first.EndedAt
	for _, c := range in {
		v := viewOf(c, cfg)
		if c.EndedAt.After(end) {
			end = c.EndedAt
		}
		if isDCFC(c) {
			r.Kind = "dcfc"
		}
		if c.PeakKW != nil && (r.PeakKW == nil || *c.PeakKW > *r.PeakKW) {
			pk := *c.PeakKW
			r.PeakKW = &pk
		}
		r.KWh += val(c.EnergyKWh)
		sources[v.source] = true
		if v.spendOK {
			priced = true
			cost += *c.Cost
			pricedKWh += val(c.EnergyKWh)
		} else {
			r.UnpricedSessions++
		}
		for _, f := range v.flags {
			flagSet[f] = true
		}
		r.Sessions = append(r.Sessions, ReceiptSession{
			ChargeID:    c.ID,
			StartedAt:   c.StartedAt.UTC(),
			EndedAt:     c.EndedAt.UTC(),
			SoCStart:    c.StartSoC,
			SoCEnd:      c.EndSoC,
			KWh:         c.EnergyKWh,
			PeakKW:      c.PeakKW,
			Cost:        c.Cost,
			PricePerKWh: c.PricePerKWh,
			CostSource:  v.source,
			FinalState:  c.FinalState,
			Source:      c.Source,
			Flags:       nonNil(v.flags),
		})
	}
	r.EndedAt = end.UTC()
	if priced {
		c := round4(cost)
		r.Cost = &c
		if pricedKWh > 0 {
			rate := round4(cost / pricedKWh)
			r.Rate = &rate
		}
	}
	if len(sources) == 1 {
		for s := range sources {
			r.CostSource = s
		}
	} else {
		r.CostSource = SourceMixed
	}
	if len(in) > 1 {
		flagSet[FlagFragmentsFolded] = true
	}
	if before {
		flagSet[FlagContinuedFromPrev] = true
	}
	if after {
		flagSet[FlagContinuesNextMonth] = true
	}
	for f := range flagSet {
		r.Flags = append(r.Flags, f)
	}
	sort.Strings(r.Flags)
	r.Flags = nonNil(r.Flags)

	startLocal := r.StartedAt.In(p.Loc)
	endLocal := r.EndedAt.In(p.Loc)
	r.LocalDate = startLocal.Format("2006-01-02")
	r.SpansDays = daysBetween(startLocal, endLocal) + 1
	r.KWh = round4(r.KWh)
	return r, true
}

// ---- helpers --------------------------------------------------------

func sortCharges(cs []Charge) {
	sort.SliceStable(cs, func(i, j int) bool {
		if !cs[i].StartedAt.Equal(cs[j].StartedAt) {
			return cs[i].StartedAt.Before(cs[j].StartedAt)
		}
		return cs[i].ID < cs[j].ID
	})
}

func findCharge(cs []Charge, id string) (Charge, bool) {
	for _, c := range cs {
		if c.ID == id {
			return c, true
		}
	}
	return Charge{}, false
}

func daysBetween(a, b time.Time) int {
	ad := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	bd := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	n := int(bd.Sub(ad).Hours() / 24)
	if n < 0 {
		return 0
	}
	return n
}

func positive(p *float64) bool { return p != nil && *p > 0 }

func val(p *float64) float64 {
	if p == nil || *p < 0 || math.IsNaN(*p) {
		return 0
	}
	return *p
}

func hasFlag(fs []string, f string) bool {
	for _, x := range fs {
		if x == f {
			return true
		}
	}
	return false
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func round4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

func roundMonthly(m *Monthly) {
	m.Spend.Total = round4(m.Spend.Total)
	for k, v := range m.Spend.BySource {
		m.Spend.BySource[k] = round4(v)
	}
	m.Spend.UnpricedKWh = round4(m.Spend.UnpricedKWh)
	m.Energy.KWhAdded = round4(m.Energy.KWhAdded)
	for k, v := range m.Energy.ByKind {
		v.KWh = round4(v.KWh)
		v.Cost = round4(v.Cost)
		m.Energy.ByKind[k] = v
	}
	m.Driving.Miles = round4(m.Driving.Miles)
	m.Driving.MilesWithEnergy = round4(m.Driving.MilesWithEnergy)
	m.Driving.KWhUsedKnown = round4(m.Driving.KWhUsedKnown)
}

func haversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}
