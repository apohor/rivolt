// Package milestones notices when a vehicle passes one of a few round numbers
// (1,000 miles recorded, 1 MWh charged at home, 100 charges, halfway through
// the lease, and a personal-best efficiency on a long drive), keeps a dismissible
// card for each, and pushes it to the devices that turned milestones on.
//
// Each milestone is announced once per vehicle, ever: the (account, vehicle, key)
// row is both the card and the guard. Milestones already passed when this shipped
// are marked 'backfilled' by a one-off pass and never announced; until that pass
// is done nothing is announced at all. Pushes go through internal/engage (the
// monthly budget, quiet hours, the toggle read at send time) and give way when
// the weekly recap needs the budget: a milestone push is only sent while at
// least two budget slots are left. Aggregates only: no place, no coordinate.
package milestones

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/ledger"
)

// Kinds, and their key prefixes. A key is stored per vehicle.
const (
	KindMiles   = "miles_1000"
	KindHomeMWh = "home_mwh_1"
	KindCharges = "charges_100"
	KindLease   = "lease_half"
	KindBestEff = "best_eff" // key best_eff:YYYY-MM
)

// Thresholds.
const (
	MilesThreshold   = 1000.0
	HomeKWhThreshold = 1000.0
	ChargesThreshold = 100
	// A personal best needs a long drive, a history to beat and a clear margin.
	BestEffMinMiles  = 50.0
	BestEffMinPrior  = 5
	BestEffMargin    = 1.05
	StatusAchieved   = "achieved"
	StatusBackfilled = "backfilled"
	CardWindow       = 30 * 24 * time.Hour
	// PushReserve is how many budget slots a milestone push leaves for the weekly
	// recap: it is sent only while at most MonthlyBudget-PushReserve are used.
	PushReserve = 2
)

// Detail is what a milestone records: numbers only.
type Detail struct {
	Miles       float64 `json:"miles,omitempty"`
	MiPerKWh    float64 `json:"mi_per_kwh,omitempty"`
	CostPerMile float64 `json:"cost_per_mile,omitempty"`
	Currency    string  `json:"currency,omitempty"`
	HomeKWh     float64 `json:"home_kwh,omitempty"`
	HomeCost    float64 `json:"home_cost,omitempty"`
	Charges     int     `json:"charges,omitempty"`
	KWhAdded    float64 `json:"kwh_added,omitempty"`
	DriveMiles  float64 `json:"drive_miles,omitempty"`
	DriveMiKWh  float64 `json:"drive_mi_per_kwh,omitempty"`
	LeaseDays   int     `json:"lease_days_elapsed,omitempty"`
	LeaseTotal  int     `json:"lease_days_total,omitempty"`
}

// Found is a milestone that has been passed.
type Found struct {
	Key    string
	Kind   string
	Detail Detail
}

// Totals are a vehicle's all-time figures from the ledger.
type totals struct {
	miles, mipk, cpm            float64
	currency                    string
	charges                     int
	kwhAdded, homeKWh, homeCost float64
}

func allTime(now time.Time) ledger.Period {
	return ledger.Period{Month: "all", Loc: time.UTC, Start: time.Unix(0, 0).UTC(), End: now.AddDate(0, 0, 1)}
}

func totalsOf(in ledger.Input, now time.Time) totals {
	m := ledger.Build(in, allTime(now), false)
	t := totals{miles: m.Driving.Miles, currency: m.Currency, charges: m.Energy.Sessions, kwhAdded: m.Energy.KWhAdded, homeKWh: m.Energy.ByPlace["home"].KWh, homeCost: m.Energy.ByPlace["home"].Cost}
	if m.Driving.KWhUsedKnown > 0 && m.Driving.MilesWithEnergy > 0 {
		t.mipk = m.Driving.MilesWithEnergy / m.Driving.KWhUsedKnown
	}
	if m.Driving.CostPerMile != nil {
		t.cpm = *m.Driving.CostPerMile
	}
	return t
}

// LeaseTerm is how far through its lease a vehicle is; Total 0 means no lease.
type LeaseTerm struct{ Elapsed, Total int }

// Evaluate lists the milestones the vehicle has passed: the totals ones, the lease
// one when it has a lease, and the personal best when driveID names a drive
// that just closed. It reports every one passed, announced or not; the caller
// keeps only the new ones.
func Evaluate(in ledger.Input, now time.Time, lease LeaseTerm, driveID string) []Found {
	t := totalsOf(in, now)
	var out []Found
	if t.miles >= MilesThreshold {
		out = append(out, Found{Key: KindMiles, Kind: KindMiles, Detail: Detail{Miles: round(t.miles, 0), MiPerKWh: round(t.mipk, 2), CostPerMile: round(t.cpm, 3), Currency: t.currency}})
	}
	if t.homeKWh >= HomeKWhThreshold {
		out = append(out, Found{Key: KindHomeMWh, Kind: KindHomeMWh, Detail: Detail{HomeKWh: round(t.homeKWh, 0), HomeCost: round(t.homeCost, 0), Currency: t.currency}})
	}
	if t.charges >= ChargesThreshold {
		out = append(out, Found{Key: KindCharges, Kind: KindCharges, Detail: Detail{Charges: t.charges, KWhAdded: round(t.kwhAdded, 0)}})
	}
	if lease.Total > 0 && lease.Elapsed*2 >= lease.Total {
		out = append(out, Found{Key: KindLease, Kind: KindLease, Detail: Detail{LeaseDays: lease.Elapsed, LeaseTotal: lease.Total}})
	}
	if f, ok := bestEfficiency(in, driveID); ok {
		out = append(out, f)
	}
	return out
}

func eff(d ledger.Drive) (float64, bool) {
	if d.DistanceMi == nil || d.EnergyKWh == nil || *d.DistanceMi < BestEffMinMiles || *d.EnergyKWh <= 0 {
		return 0, false
	}
	return *d.DistanceMi / *d.EnergyKWh, true
}

// bestEfficiency: the named drive is a long one that beats the best of at least
// five earlier long drives by 5 % or more. Its key carries the month, so at most
// one is announced a month.
func bestEfficiency(in ledger.Input, driveID string) (Found, bool) {
	if driveID == "" {
		return Found{}, false
	}
	ds, _ := ledger.EffectiveDrives(in.Drives)
	var cur *ledger.Drive
	for i := range ds {
		if ds[i].ID == driveID {
			cur = &ds[i]
		}
	}
	if cur == nil {
		return Found{}, false
	}
	e, ok := eff(*cur)
	if !ok {
		return Found{}, false
	}
	prior, best := 0, 0.0
	for _, d := range ds {
		if d.ID == cur.ID || !d.EndedAt.Before(cur.StartedAt) {
			continue
		}
		if pe, ok := eff(d); ok {
			prior++
			best = math.Max(best, pe)
		}
	}
	if prior < BestEffMinPrior || e < best*BestEffMargin {
		return Found{}, false
	}
	return Found{
		Key: KindBestEff + ":" + cur.EndedAt.UTC().Format("2006-01"), Kind: KindBestEff,
		Detail: Detail{DriveMiles: round(*cur.DistanceMi, 0), DriveMiKWh: round(e, 2)},
	}, true
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func money(v float64, cur string, places int) string {
	if cur == "" || strings.EqualFold(cur, "USD") {
		return fmt.Sprintf("$%.*f", places, v)
	}
	return fmt.Sprintf("%.*f %s", places, v, strings.ToUpper(cur))
}

// thousands writes a whole number with comma separators: 2850 -> "2,850".
func thousands(v float64) string {
	n := int64(math.Round(v))
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Text is the card's title and body, also the push's: aggregates only, one
// sentence, no exclamation marks. The wording is growth's.
func Text(kind string, d Detail) (title, body string) {
	switch kind {
	case KindMiles:
		title = "1,000 miles recorded"
		body = "Your first 1,000 miles in Rivolt"
		var parts []string
		if d.MiPerKWh > 0 {
			parts = append(parts, fmt.Sprintf("%.2f mi/kWh", d.MiPerKWh))
		}
		if d.CostPerMile > 0 {
			parts = append(parts, money(d.CostPerMile, d.Currency, 2)+"/mi")
		}
		if len(parts) > 0 {
			body += ": " + strings.Join(parts, ", ")
		}
		body += "."
	case KindHomeMWh:
		title = "1,000 kWh charged at home"
		body = "1,000 kWh charged at home since you connected."
		if d.HomeCost > 0 {
			body += fmt.Sprintf(" About %s at your home rate.", money(d.HomeCost, d.Currency, 0))
		}
	case KindCharges:
		title = "100 charges"
		body = "100 charges recorded in Rivolt"
		if d.KWhAdded > 0 {
			body += ", " + thousands(d.KWhAdded) + " kWh added"
		}
		body += "."
	case KindLease:
		title, body = "Halfway through your lease", "Halfway through the term. Open Rivolt to see your mileage pace."
	case KindBestEff:
		title = "Your most efficient drive yet"
		body = fmt.Sprintf("%.2f mi/kWh over %.0f mi, your best on a drive this long.", d.DriveMiKWh, d.DriveMiles)
	default:
		title, body = "Milestone", "A new milestone in Rivolt."
	}
	return title, body
}

// Row is a stored milestone.
type Row struct {
	User        uuid.UUID
	Vehicle     uuid.UUID
	RivianID    string
	Key, Kind   string
	Status      string
	AchievedAt  time.Time
	Detail      Detail
	DismissedAt *time.Time
}

// Store is the persistence the checker needs.
type Store interface {
	// Ready reports whether the one-off backfill has finished.
	Ready(ctx context.Context) (bool, error)
	MarkReady(ctx context.Context) error
	// Insert adds a milestone row; false when it already exists.
	Insert(ctx context.Context, r Row) (bool, error)
	// Vehicle maps a Rivian vehicle id to the account's vehicle row.
	Vehicle(ctx context.Context, uid uuid.UUID, rivianID string) (uuid.UUID, bool, error)
	// Vehicles lists accounts' vehicles; active limits it to those with a drive or
	// charge since the given time (the zero time: all).
	Vehicles(ctx context.Context, active time.Time) ([]VehicleRef, error)
	// PendingPush lists achieved, undismissed milestones achieved since the
	// cutoff whose push was never sent (no engagement_sends row with status
	// 'sent' for their claim key), oldest first.
	PendingPush(ctx context.Context, since time.Time) ([]Row, error)
}

// VehicleRef names a vehicle row and its owner.
type VehicleRef struct {
	User     uuid.UUID
	ID       uuid.UUID
	RivianID string
}

// Checker finds new milestones and announces them.
type Checker struct {
	Store Store
	// Input loads the vehicle's whole ledger input.
	Input func(ctx context.Context, uid uuid.UUID, rivianID string) (ledger.Input, error)
	// Lease says how far through its lease the vehicle is; Total 0 when none.
	Lease func(ctx context.Context, uid uuid.UUID, vehicle uuid.UUID) LeaseTerm
	// Push sends the milestone to the account's devices that have it on, through
	// engage; nil sends nothing (the card still appears).
	Push   func(ctx context.Context, uid uuid.UUID, vehicle uuid.UUID, r Row)
	Now    func() time.Time
	Logger *slog.Logger
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// Check looks for new milestones after a drive or charge closed (driveID names
// the drive, empty for a charge or the catch-up pass). Until the backfill is done
// it announces nothing.
func (c *Checker) Check(ctx context.Context, uid uuid.UUID, rivianID, driveID string) {
	ready, err := c.Store.Ready(ctx)
	if err != nil || !ready {
		return
	}
	vid, ok, err := c.Store.Vehicle(ctx, uid, rivianID)
	if err != nil || !ok {
		return
	}
	c.check(ctx, VehicleRef{User: uid, ID: vid, RivianID: rivianID}, driveID, StatusAchieved)
}

func (c *Checker) check(ctx context.Context, v VehicleRef, driveID, status string) (pushed bool) {
	in, err := c.Input(ctx, v.User, v.RivianID)
	if err != nil {
		c.logger().Warn("milestones: ledger unavailable", "err", err.Error())
		return false
	}
	var lease LeaseTerm
	if c.Lease != nil {
		lease = c.Lease(ctx, v.User, v.ID)
	}
	now := c.now()
	// At most one push per pass: after a big import several totals can be found at
	// once, the rest stay cards (and are offered on later catch-up passes).
	for _, f := range Evaluate(in, now, lease, driveID) {
		row := Row{User: v.User, Vehicle: v.ID, RivianID: v.RivianID, Key: f.Key, Kind: f.Kind, Status: status, AchievedAt: now, Detail: f.Detail}
		if status == StatusBackfilled {
			row.Detail = Detail{} // never shown: keep only the key, the status and the time
		}
		inserted, err := c.Store.Insert(ctx, row)
		if err != nil {
			c.logger().Warn("milestones: insert", "kind", f.Kind, "err", err.Error())
			continue
		}
		if inserted && status == StatusAchieved && c.Push != nil && !pushed {
			pushed = true
			c.Push(ctx, v.User, v.ID, row)
		}
	}
	return pushed
}

// Backfill marks every milestone already passed as backfilled (never announced),
// then marks the feature ready. Idempotent and safe on every replica.
func (c *Checker) Backfill(ctx context.Context) error {
	if ready, err := c.Store.Ready(ctx); err != nil || ready {
		return err
	}
	vs, err := c.Store.Vehicles(ctx, time.Time{})
	if err != nil {
		return err
	}
	for _, v := range vs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.check(ctx, v, "", StatusBackfilled)
	}
	return c.Store.MarkReady(ctx)
}

// CatchUp checks the vehicles that drove or charged in the last two days, for a
// close a restart or a failed hook missed.
func (c *Checker) CatchUp(ctx context.Context) error {
	ready, err := c.Store.Ready(ctx)
	if err != nil || !ready {
		return err
	}
	vs, err := c.Store.Vehicles(ctx, c.now().Add(-48*time.Hour))
	if err != nil {
		return err
	}
	pushedBy := map[uuid.UUID]bool{}
	for _, v := range vs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.check(ctx, v, "", StatusAchieved) {
			pushedBy[v.User] = true
		}
	}
	return c.reoffer(ctx, pushedBy)
}

// ReofferWindow is how long a milestone whose push was refused stays eligible
// for another try; older ones stay card-only.
const ReofferWindow = 72 * time.Hour

// reoffer gives the pushes that engage refused (outside 09:00-20:00 local, no
// known zone, the budget reserve) another chance: a milestone that was achieved
// at night had its push dropped, and nothing else would offer it again. Only
// achieved, undismissed milestones under ReofferWindow old, whose push never went
// out, and at most one push per account per pass (an account that already got one
// this pass gets none). engage's claim on (milestone key, vehicle) makes a repeat
// harmless: a sent one is excluded here, and it cannot send twice.
func (c *Checker) reoffer(ctx context.Context, pushedBy map[uuid.UUID]bool) error {
	if c.Push == nil {
		return nil
	}
	pending, err := c.Store.PendingPush(ctx, c.now().Add(-ReofferWindow))
	if err != nil {
		return err
	}
	for _, r := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if pushedBy[r.User] {
			continue
		}
		pushedBy[r.User] = true
		c.Push(ctx, r.User, r.Vehicle, r)
	}
	return nil
}

// Run backfills shortly after start (if it has not been done) and then catches up
// every six hours, until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	for {
		if err := c.Backfill(ctx); err != nil && ctx.Err() == nil {
			c.logger().Warn("milestones: backfill failed", "err", err.Error())
		} else if err := c.CatchUp(ctx); err != nil && ctx.Err() == nil {
			c.logger().Warn("milestones: catch-up failed", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

// PushAllowed says whether a milestone push may use the account's budget now: it
// leaves PushReserve slots for the weekly recap.
func PushAllowed(used int) bool { return used <= engage.MonthlyBudget-PushReserve }
