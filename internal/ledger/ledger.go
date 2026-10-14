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
	"github.com/apohor/rivolt/internal/rivian"
)

// Version is the response contract version. Additive changes keep it;
// breaking changes bump it.
const Version = 1

// FastMinKW is the session peak at or above which a charge is DC fast
// charging. Same threshold as analytics.FastMinKW (the Charges page's
// "Fast" bucket) so the two surfaces agree.
const FastMinKW = 50.0

// MinMilesForCostPerMile is the driven distance below which
// driving.cost_per_mile is null: with only a few miles, one charge
// dominates and the ratio is meaningless (D2 F6).
const MinMilesForCostPerMile = 100.0

// HomeRadiusKM is how close to the saved home location a session must
// start to count as "home". Matches the ~200 m DBSCAN epsilon the
// Charges page uses, with a little GPS slack.
const HomeRadiusKM = rivian.HomeRadiusM / 1000

// homeRateTolerance is how close a legacy row's $/kWh must be to the
// configured home rate to be attributed to it (rows without a persisted
// cost_source).
const homeRateTolerance = 0.0005

// padding is how far beyond the month the loader reaches so dedupe and
// plug-in grouping see sessions that straddle the boundary.
const padding = 72 * time.Hour

// Cost sources. Rows closed by the pricing-aware recorder persist one
// of rivian / home_rate / network_estimate / user in charges.cost_source
// (migration 0040). Legacy rows (NULL) are derived at query time, which
// adds home_rate_misapplied: the old recorder's home rate stamped on a
// DC fast session, shown as "estimate pending" and kept out of spend
// until `rivolt reprice` backfills it.
const (
	SourceRivian             = "rivian"
	SourceHomeRate           = "home_rate"
	SourceNetworkEstimate    = "network_estimate"
	SourceUser               = "user"
	SourceHomeRateMisapplied = "home_rate_misapplied"
	SourceUnknown            = "unknown"
	SourceMixed              = "mixed"
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
	FlagEstimatePending    = "estimate_pending"
	// FlagFree marks a session the user priced at $0 (cost_source=user,
	// cost 0): priced, counted in spend at zero, shown as "Free".
	FlagFree = "free"
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
	// ActiveSeconds is charges.active_seconds (time actually drawing
	// power); nil when not recorded. Feeds the shared DCFC rule.
	ActiveSeconds *float64
	// Place is charges.place ("home" / "away") recorded with the row
	// (migration 0044); "" when not recorded.
	Place string
	// CostSource is charges.cost_source; "" when NULL (legacy row) or
	// the column doesn't exist yet.
	CostSource string
	// Network is charges.network (e.g. "Electrify America") for
	// network-estimated sessions; "" when unknown.
	Network string
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
	EstimatedShare           *float64           `json:"estimated_share"`
	PricedSessions           int                `json:"priced_sessions"`
	UnpricedSessions         int                `json:"unpriced_sessions"`
	UnpricedKWh              float64            `json:"unpriced_kwh"`
	UnpricedHomeRateEstimate *float64           `json:"unpriced_home_rate_estimate"`
	EstimatePendingSessions  int                `json:"estimate_pending_sessions"`
	EstimatePendingKWh       float64            `json:"estimate_pending_kwh"`
}

// Energy is energy added by charging.
type Energy struct {
	Sessions           int                 `json:"sessions"`
	KWhAdded           float64             `json:"kwh_added"`
	NullEnergySessions int                 `json:"null_energy_sessions"`
	ByKind             map[string]KindLine `json:"by_kind"`
	ByPlace            map[string]KindLine `json:"by_place"`
	// ByPlaceKind is the place × kind cross-tab of the same per-session
	// classification: summed over places it equals ByKind, over kinds
	// ByPlace. All six cells are always present.
	ByPlaceKind map[string]map[string]KindLine `json:"by_place_kind"`
}

// KindLine is one by_kind / by_place bucket. Sessions are classified
// one by one (session peak for kind, session location for place), so
// each map's lines sum to the Energy totals.
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
	Network          *string          `json:"network"`
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
	// CostSourceDerived is true when cost_source was derived at query
	// time (legacy row) rather than persisted by the recorder.
	CostSourceDerived bool     `json:"cost_source_derived"`
	Network           *string  `json:"network"`
	Kind              string   `json:"kind"`
	Place             string   `json:"place"`
	FinalState        string   `json:"final_state"`
	Source            string   `json:"source"`
	Flags             []string `json:"flags"`
}

// DataQuality carries the counts behind the Money tab's quality notes.
type DataQuality struct {
	// CostSourceInferred is true when any session in the month had its
	// cost_source derived (legacy NULL); CostSourceDerivedSessions counts
	// them.
	CostSourceInferred        bool         `json:"cost_source_inferred"`
	CostSourceDerivedSessions int          `json:"cost_source_derived_sessions"`
	SupersededExcluded        int          `json:"superseded_excluded"`
	Superseded                []Superseded `json:"superseded,omitempty"`
	DrivesSupersededExcluded  int          `json:"drives_superseded_excluded"`
	OpenSessions              int          `json:"open_sessions"`
	HomeRateOnDCFC            int          `json:"home_rate_on_dcfc"`
	HomeRateAway              int          `json:"home_rate_away"`
	CurrencyMismatchExcluded  int          `json:"currency_mismatch_excluded"`
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

// CostSource reports where a session's cost came from and whether the
// answer was derived at query time rather than read from the row.
//
//   - A persisted rivian / network_estimate / user source wins (a row
//     with no cost is still unknown).
//   - Legacy rows (NULL source) with no cost are unknown.
//   - A persisted home_rate wins, except AC away from home → unknown
//     (owner decision 2026-10-13: away AC is unknown until priced).
//   - A legacy rate within homeRateTolerance of the user's home rate:
//     DC fast charging → home_rate_misapplied (estimate pending); AC
//     away from home → unknown, needs a price; otherwise → home_rate.
//   - Any other legacy price → rivian (billed, or a manual edit / an
//     older home rate).
func CostSource(c Charge, cfg Config) (string, bool) {
	persisted := strings.ToLower(strings.TrimSpace(c.CostSource))
	switch persisted {
	case SourceRivian, SourceNetworkEstimate, SourceUser:
		if c.Cost == nil {
			return SourceUnknown, false
		}
		return persisted, false
	case SourceHomeRate:
		// Persisted wins, except AC away from home (rows the recorder
		// stamped before it learned to leave those unpriced).
		if c.Cost == nil {
			return SourceUnknown, false
		}
		if pl, _ := place(c, cfg.Home); pl == "away" && !isDCFC(c) {
			return SourceUnknown, true
		}
		return SourceHomeRate, false
	}
	if !positive(c.Cost) {
		return SourceUnknown, true
	}
	if homeRateStamped(c, cfg) {
		if isDCFC(c) {
			return SourceHomeRateMisapplied, true
		}
		if pl, _ := place(c, cfg.Home); pl == "away" {
			return SourceUnknown, true
		}
		return SourceHomeRate, true
	}
	return SourceRivian, true
}

// homeRateStamped reports whether the row carries the home rate: a
// persisted home_rate source, or a legacy row priced within
// homeRateTolerance of the user's current home rate.
func homeRateStamped(c Charge, cfg Config) bool {
	switch strings.ToLower(strings.TrimSpace(c.CostSource)) {
	case SourceHomeRate:
		return c.Cost != nil
	case SourceRivian, SourceNetworkEstimate, SourceUser:
		return false
	}
	// Legacy (NULL) or unrecognised source: judge by the rate.
	return positive(c.Cost) && cfg.HomeRate > 0 && c.PricePerKWh != nil &&
		math.Abs(*c.PricePerKWh-cfg.HomeRate) <= homeRateTolerance
}

// isPriced reports whether a cost source counts toward spend.
func isPriced(source string) bool {
	switch source {
	case SourceRivian, SourceHomeRate, SourceNetworkEstimate, SourceUser:
		return true
	}
	return false
}

func networkOf(c Charge) *string {
	if n := strings.TrimSpace(c.Network); n != "" {
		return &n
	}
	return nil
}

// isDCFC is the recorder's rule (rivian.IsDCFCSession): peak ≥ 50 kW AND
// energy over active (else wall) time ≥ 20 kW, so one glitched peak
// sample on a long home session doesn't make it a fast charge.
func isDCFC(c Charge) bool {
	return rivian.IsDCFCSession(val(c.PeakKW), val(c.EnergyKWh), val(c.ActiveSeconds), c.EndedAt.Sub(c.StartedAt))
}

func kindOf(c Charge) string {
	if isDCFC(c) {
		return "dcfc"
	}
	return "ac"
}

// place returns home/away/unknown and the distance in km (nil when
// unknown).
//
// A place recorded on the row (migration 0044) wins, so moving home
// doesn't reclassify past charges. km_from_home is then reported only
// when the current home agrees with it (otherwise it would describe a
// different home than the one the charge was classified against).
// Rows without one (legacy, or no home / position when recorded) are
// derived from the current home.
func place(c Charge, home *LatLon) (string, *float64) {
	derived, km := derivePlace(c, home)
	switch snap := strings.ToLower(strings.TrimSpace(c.Place)); snap {
	case "home", "away":
		if derived != snap {
			km = nil
		}
		return snap, km
	}
	return derived, km
}

func derivePlace(c Charge, home *LatLon) (string, *float64) {
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

// CrumbKWh is the energy below which a charge session is a crumb (a
// plug-in blip, an abandoned stub): nothing to price, counted as
// null_energy rather than "needs a price". Seen on preview: a 4.7e-7
// kWh abandoned row counted as unpriced.
const CrumbKWh = 0.05

// hasEnergy reports whether a session delivered more than a crumb.
func hasEnergy(c Charge) bool { return val(c.EnergyKWh) >= CrumbKWh }

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
	derived bool
	kind    string
	place   string
	kmHome  *float64
	flags   []string
	spendOK bool // counts toward spend (priced, same currency)
}

func viewOf(c Charge, cfg Config) sessionView {
	v := sessionView{c: c, kind: kindOf(c)}
	v.source, v.derived = CostSource(c, cfg)
	v.place, v.kmHome = place(c, cfg.Home)
	if !currencyMatches(c, cfg) {
		v.flags = append(v.flags, FlagCurrencyMismatch)
	}
	// A session with no recorded energy (plug-in blip, abandoned stub)
	// has nothing to price: it's counted as null_energy, not unpriced.
	if v.source == SourceUnknown && hasEnergy(c) {
		v.flags = append(v.flags, FlagUnpriced)
	}
	if !hasEnergy(c) {
		v.flags = append(v.flags, FlagNullEnergy)
	}
	if isOpen(c.FinalState) {
		v.flags = append(v.flags, FlagOpenSession)
	}
	if strings.EqualFold(c.FinalState, "abandoned") {
		v.flags = append(v.flags, FlagAbandoned)
	}
	homeStamped := homeRateStamped(c, cfg)
	if v.source == SourceHomeRateMisapplied {
		v.flags = append(v.flags, FlagEstimatePending)
	}
	if v.source == SourceUser && c.Cost != nil && *c.Cost == 0 {
		v.flags = append(v.flags, FlagFree)
	}
	if homeStamped && isDCFC(c) {
		v.flags = append(v.flags, FlagHomeRateOnDCFC)
	}
	if homeStamped && v.place == "away" {
		v.flags = append(v.flags, FlagHomeRateAway)
	}
	v.spendOK = isPriced(v.source) && c.Cost != nil && currencyMatches(c, cfg)
	sort.Strings(v.flags)
	return v
}

// Classification is the ledger's view of one charge: the same cost
// source, kind, place and flags the Money tab uses, for other surfaces
// (the Charges list, the charge page, the Activity feed) so they don't
// re-derive them with their own rules.
type Classification struct {
	CostSource        string   `json:"cost_source_effective"`
	CostSourceDerived bool     `json:"cost_source_derived"`
	Kind              string   `json:"kind"`
	Place             string   `json:"place"`
	KMFromHome        *float64 `json:"km_from_home"`
	// CountsInSpend is true when the session's cost is part of
	// spend.total (priced, same currency).
	CountsInSpend bool     `json:"counts_in_spend"`
	Flags         []string `json:"ledger_flags"`
}

// Classify returns the ledger's classification of c under cfg.
func Classify(c Charge, cfg Config) Classification {
	v := viewOf(c, cfg)
	return Classification{
		CostSource:        v.source,
		CostSourceDerived: v.derived,
		Kind:              v.kind,
		Place:             v.place,
		KMFromHome:        v.kmHome,
		CountsInSpend:     v.spendOK,
		Flags:             nonNil(v.flags),
	}
}

// FromCharge converts a charges.Charge as the store lists it (zeros for
// NULL columns) into the ledger's nullable shape. A zero cost stays 0
// only on user-priced rows, where it means Free.
func FromCharge(c charges.Charge) Charge {
	opt := func(v float64) *float64 {
		if v == 0 {
			return nil
		}
		return &v
	}
	out := Charge{
		ID: c.ID, VehicleID: c.VehicleID,
		StartedAt: c.StartedAt, EndedAt: c.EndedAt,
		StartSoC: opt(c.StartSoCPct), EndSoC: opt(c.EndSoCPct),
		EnergyKWh: opt(c.EnergyAddedKWh), PeakKW: opt(c.MaxPowerKW),
		Cost: opt(c.Cost), Currency: c.Currency, PricePerKWh: opt(c.PricePerKWh),
		FinalState: c.FinalState, Source: c.Source,
		Lat: opt(c.Lat), Lon: opt(c.Lon),
		ActiveSeconds: opt(c.ActiveSeconds),
		Place:         c.Place, CostSource: c.CostSource, Network: c.Network,
	}
	if strings.EqualFold(c.CostSource, SourceUser) {
		cost := c.Cost
		out.Cost = &cost
	}
	return out
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
			BySource: map[string]float64{
				SourceRivian: 0, SourceHomeRate: 0, SourceNetworkEstimate: 0, SourceUser: 0,
			},
		},
		Energy: Energy{
			ByKind:  map[string]KindLine{"ac": {}, "dcfc": {}},
			ByPlace: map[string]KindLine{"home": {}, "away": {}, "unknown": {}},
			ByPlaceKind: map[string]map[string]KindLine{
				"home":    {"ac": {}, "dcfc": {}},
				"away":    {"ac": {}, "dcfc": {}},
				"unknown": {"ac": {}, "dcfc": {}},
			},
		},
		Driving: Driving{CostPerMileBasis: "spend_over_miles"},
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
		if !hasEnergy(c) {
			out.Energy.NullEnergySessions++
		}
		var cost float64
		switch {
		case hasFlag(v.flags, FlagCurrencyMismatch):
			out.DataQuality.CurrencyMismatchExcluded++
		case v.spendOK:
			cost = *c.Cost
			out.Spend.Total += cost
			out.Spend.BySource[v.source] += cost
			out.Spend.PricedSessions++
		case v.source == SourceHomeRateMisapplied:
			out.Spend.EstimatePendingSessions++
			out.Spend.EstimatePendingKWh += kwh
		case !hasEnergy(c):
			// Nothing to price; counted in energy.null_energy_sessions.
		default:
			out.Spend.UnpricedSessions++
			out.Spend.UnpricedKWh += kwh
			// The "≈ $X at your home rate" hint only makes sense for
			// charging that could have been at home.
			if v.kind == "ac" && v.place != "away" {
				unpricedEstimate += kwh * in.Config.HomeRate
			}
		}
		addLine(out.Energy.ByKind, v.kind, c, kwh, cost)
		addLine(out.Energy.ByPlace, v.place, c, kwh, cost)
		addLine(out.Energy.ByPlaceKind[v.place], v.kind, c, kwh, cost)
		if v.derived {
			out.DataQuality.CostSourceInferred = true
			out.DataQuality.CostSourceDerivedSessions++
		}

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
	if out.Spend.Total > 0 {
		share := round4(out.Spend.BySource[SourceNetworkEstimate] / out.Spend.Total)
		out.Spend.EstimatedShare = &share
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
		// Below MinMilesForCostPerMile a single charge dominates and the
		// ratio is noise (e.g. $1.13/mi from 5.75 mi early in a month).
		if out.Spend.Total > 0 && out.Driving.Miles >= MinMilesForCostPerMile {
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
		// Zero-energy crumbs don't decide the receipt's cost source or
		// make a priced receipt look unpriced.
		if hasEnergy(c) || v.spendOK {
			sources[v.source] = true
		}
		if v.spendOK {
			priced = true
			cost += *c.Cost
			pricedKWh += val(c.EnergyKWh)
		} else if hasEnergy(c) {
			r.UnpricedSessions++
		}
		for _, f := range v.flags {
			flagSet[f] = true
		}
		if r.Network == nil {
			r.Network = networkOf(c)
		}
		r.Sessions = append(r.Sessions, ReceiptSession{
			ChargeID:          c.ID,
			StartedAt:         c.StartedAt.UTC(),
			EndedAt:           c.EndedAt.UTC(),
			SoCStart:          c.StartSoC,
			SoCEnd:            c.EndSoC,
			KWh:               c.EnergyKWh,
			PeakKW:            c.PeakKW,
			Cost:              c.Cost,
			PricePerKWh:       c.PricePerKWh,
			CostSource:        v.source,
			CostSourceDerived: v.derived,
			Network:           networkOf(c),
			Kind:              v.kind,
			Place:             v.place,
			FinalState:        c.FinalState,
			Source:            c.Source,
			Flags:             nonNil(v.flags),
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
	switch len(sources) {
	case 0: // only zero-energy crumbs
		r.CostSource = SourceUnknown
	case 1:
		for s := range sources {
			r.CostSource = s
		}
	default:
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

// addLine folds one session into a by_kind / by_place bucket. cost is
// the session's spend contribution (0 when not priced).
func addLine(m map[string]KindLine, key string, c Charge, kwh, cost float64) {
	l := m[key]
	l.Sessions++
	l.KWh += kwh
	l.Cost += cost
	if c.PeakKW != nil && (l.PeakKW == nil || *c.PeakKW > *l.PeakKW) {
		pk := *c.PeakKW
		l.PeakKW = &pk
	}
	m[key] = l
}

func roundMonthly(m *Monthly) {
	m.Spend.Total = round4(m.Spend.Total)
	for k, v := range m.Spend.BySource {
		m.Spend.BySource[k] = round4(v)
	}
	m.Spend.UnpricedKWh = round4(m.Spend.UnpricedKWh)
	m.Energy.KWhAdded = round4(m.Energy.KWhAdded)
	all := []map[string]KindLine{m.Energy.ByKind, m.Energy.ByPlace}
	for _, byKind := range m.Energy.ByPlaceKind {
		all = append(all, byKind)
	}
	for _, lines := range all {
		for k, v := range lines {
			v.KWh = round4(v.KWh)
			v.Cost = round4(v.Cost)
			lines[k] = v
		}
	}
	m.Spend.EstimatePendingKWh = round4(m.Spend.EstimatePendingKWh)
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
