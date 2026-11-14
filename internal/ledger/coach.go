package ledger

import (
	"time"

	"github.com/apohor/rivolt/internal/offpeak"
)

// The savings coach: three plain-language insights over a stretch of months, all from the charges and drives already stored (no new Rivian
// calls). Each one carries its figure, whether any of it is an estimate, and, when it cannot be given yet, what is missing ("needs"): a
// figure is never built on partial data.

// Insight ids, and the order the headline picks from: the biggest thing a user can act on first.
const (
	InsightOffpeakShift = "off_peak_shift"
	InsightCostPer100   = "cost_per_100"
	InsightCostPerKWh   = "cost_per_kwh"
)

// MaxUnpricedShare is the largest share of a window's energy that may be charging with no price before the cost per 100 is withheld: above
// it the figure would be built on a part of the spend (needs "unpriced_charges").
const MaxUnpricedShare = 0.25

// MinPricedKWh is the least priced energy a bucket needs before its cost per kWh is given.
const MinPricedKWh = 5.0

// PricedBucket is the priced sessions of one kind of charging: their energy, cost and the average price. Sessions without a price are not in
// it, so PerKWh is a price, not a total over mixed data.
type PricedBucket struct {
	Sessions int      `json:"sessions"`
	KWh      float64  `json:"kwh"`
	Cost     float64  `json:"cost"`
	PerKWh   *float64 `json:"per_kwh,omitempty"`
	// Estimated is true when any of the bucket's cost is an estimate: a typical public price, a network estimate, or the home rate while it
	// is the state average. EstimatedCostShare says how much (0 to 1).
	Estimated          bool    `json:"estimated"`
	EstimatedCostShare float64 `json:"estimated_cost_share"`
}

// CostPerKWhInsight is what a kWh really cost, at home, away and on fast chargers.
type CostPerKWhInsight struct {
	Needs        string       `json:"needs,omitempty"`
	Home         PricedBucket `json:"home"`
	Public       PricedBucket `json:"public"`
	FastCharging PricedBucket `json:"fast_charging"`
	// FastPremiumPct is how much more a fast-charging kWh cost than a home one, in percent; absent unless both have a price.
	FastPremiumPct *float64 `json:"fast_premium_pct,omitempty"`
	Estimated      bool     `json:"estimated"`
}

// CostPer100Insight is what the window's charging cost per 100 miles (and km) driven in it.
type CostPer100Insight struct {
	Needs         string   `json:"needs,omitempty"`
	Per100Mi      *float64 `json:"per_100_mi,omitempty"`
	Per100Km      *float64 `json:"per_100_km,omitempty"`
	Spend         float64  `json:"spend"`
	Miles         float64  `json:"miles"`
	UnpricedKWh   float64  `json:"unpriced_kwh"`
	UnpricedShare float64  `json:"unpriced_share"`
	Estimated     bool     `json:"estimated"`
	// EstimatedCostShare is the part of Spend that is an estimate, as in PricedBucket.
	EstimatedCostShare float64 `json:"estimated_cost_share"`
}

// OffpeakShiftInsight is the existing "move home charging to cheaper hours" estimate. It is always an estimate.
type OffpeakShiftInsight struct {
	// Needs is Savings.Needs, or "flat_rate" when the user said their rate is flat.
	Needs          string   `json:"needs,omitempty"`
	CouldHaveSaved *float64 `json:"could_have_saved,omitempty"`
	MovedSessions  int      `json:"sessions_moved"`
	Considered     int      `json:"sessions_considered"`
	Estimated      bool     `json:"estimated"`
}

// Coach is GET /api/ledger/coach.
type Coach struct {
	Version  int      `json:"version"`
	TZ       string   `json:"tz"`
	Currency string   `json:"currency"`
	Months   []string `json:"months"`
	// HeadlineID names the insight to lead with (one with a figure), "" when none has one yet. Order lists every insight id, the ones with a
	// figure first.
	HeadlineID   string              `json:"headline_id"`
	Order        []string            `json:"order"`
	OffpeakShift OffpeakShiftInsight `json:"off_peak_shift"`
	CostPer100   CostPer100Insight   `json:"cost_per_100"`
	CostPerKWh   CostPerKWhInsight   `json:"cost_per_kwh"`
}

// bucketAcc accumulates one PricedBucket.
type bucketAcc struct {
	sessions           int
	kwh, cost, estCost float64
}

func (b bucketAcc) bucket() PricedBucket {
	out := PricedBucket{Sessions: b.sessions, KWh: round2(b.kwh), Cost: round2(b.cost), Estimated: b.estCost > 0}
	if b.cost > 0 {
		out.EstimatedCostShare = round4(b.estCost / b.cost)
	}
	if b.sessions > 0 && b.kwh >= MinPricedKWh && b.cost > 0 {
		v := round4(b.cost / b.kwh)
		out.PerKWh = &v
	}
	return out
}

// isEstimateSource reports whether a session's price is an estimate: a typical public price or network estimate always, and the home rate
// while it is the state average.
func isEstimateSource(src string, cfg Config) bool {
	switch src {
	case SourceObservedEstimate, SourceNetworkEstimate:
		return true
	case SourceHomeRate:
		return cfg.HomeRateEstimated
	}
	return false
}

// BuildCoach assembles the coach over the given months (oldest first) in the user's zone. sch and flat are the off-peak schedule and the
// "my rate is flat" choice.
func BuildCoach(in Input, periods []Period, sch offpeak.Schedule, flat bool) Coach {
	out := Coach{Version: Version, Currency: in.Config.Currency, Order: []string{}}
	if len(periods) > 0 {
		out.TZ = periods[0].Loc.String()
	}
	for _, p := range periods {
		out.Months = append(out.Months, p.Month)
	}
	inWindow := func(t time.Time) bool {
		for _, p := range periods {
			if !t.Before(p.Start) && t.Before(p.End) {
				return true
			}
		}
		return false
	}

	// Per-kWh prices: only sessions with a price, the same dedupe, bills-first and currency rules as the statement.
	var home, public, fast bucketAcc
	eff, _ := EffectiveCharges(applyBilled(in.Charges, in.Config))
	for _, c := range eff {
		if !inWindow(c.StartedAt) {
			continue
		}
		v := viewOf(c, in.Config)
		if !v.spendOK || hasFlag(v.flags, FlagCurrencyMismatch) || !hasEnergy(c) {
			continue
		}
		kwh, cost := val(c.EnergyKWh), *c.Cost
		est := 0.0
		if isEstimateSource(v.source, in.Config) {
			est = cost
		}
		add := func(b *bucketAcc) {
			b.sessions++
			b.kwh += kwh
			b.cost += cost
			b.estCost += est
		}
		switch {
		case v.kind == "dcfc":
			add(&fast)
		case v.place == "home":
			add(&home)
		case v.place == "away":
			add(&public)
		}
	}
	k := &out.CostPerKWh
	k.Home, k.Public, k.FastCharging = home.bucket(), public.bucket(), fast.bucket()
	k.Estimated = k.Home.Estimated || k.Public.Estimated || k.FastCharging.Estimated
	if k.Home.PerKWh != nil && k.FastCharging.PerKWh != nil && *k.Home.PerKWh > 0 {
		v := round2((*k.FastCharging.PerKWh/(*k.Home.PerKWh) - 1) * 100)
		k.FastPremiumPct = &v
	}
	if k.Home.PerKWh == nil && k.Public.PerKWh == nil && k.FastCharging.PerKWh == nil {
		k.Needs = "priced_charges"
	}

	// Cost per 100: the statement's own spend and miles over the window, withheld when too much of the energy has no price.
	var spend, miles, unpricedKWh, addedKWh, estSpend float64
	var sessions []HomeSession
	for _, p := range periods {
		m := Build(in, p, false)
		spend += m.Spend.Total
		miles += m.Driving.Miles
		unpricedKWh += m.Spend.UnpricedKWh + m.Spend.EstimatePendingKWh
		addedKWh += m.Energy.KWhAdded
		sessions = append(sessions, HomeACSessions(in, p)...)
	}
	for _, c := range eff {
		if !inWindow(c.StartedAt) {
			continue
		}
		v := viewOf(c, in.Config)
		if v.spendOK && !hasFlag(v.flags, FlagCurrencyMismatch) && isEstimateSource(v.source, in.Config) {
			estSpend += *c.Cost
		}
	}
	c100 := &out.CostPer100
	c100.Spend, c100.Miles, c100.UnpricedKWh = round2(spend), round2(miles), round2(unpricedKWh)
	if addedKWh > 0 {
		c100.UnpricedShare = round4(unpricedKWh / addedKWh)
	}
	if spend > 0 {
		c100.EstimatedCostShare = round4(estSpend / spend)
	}
	c100.Estimated = estSpend > 0
	switch {
	case miles < MinMilesForCostPerMile:
		c100.Needs = "more_driving"
	case spend <= 0 || c100.UnpricedShare > MaxUnpricedShare:
		c100.Needs = "unpriced_charges"
	default:
		mi := round2(spend / miles * 100)
		km := round2(spend / (miles * 1.609344) * 100)
		c100.Per100Mi, c100.Per100Km = &mi, &km
	}

	// Off-peak shift: the savings estimate, always labelled an estimate.
	sh := &out.OffpeakShift
	sh.Estimated = true
	if flat {
		sh.Needs = "flat_rate"
	} else {
		s := EstimateSavings(sessions, sch, in.Config.HomeRate)
		sh.Needs, sh.CouldHaveSaved, sh.MovedSessions, sh.Considered = s.Needs, s.CouldHaveSaved, s.Moved, s.Considered
	}

	// Order: an actionable saving first, then what driving costs, then the price of a kWh; those with no figure yet follow.
	has := map[string]bool{
		InsightOffpeakShift: sh.Needs == "" && sh.CouldHaveSaved != nil && *sh.CouldHaveSaved > 0,
		InsightCostPer100:   c100.Needs == "",
		InsightCostPerKWh:   k.Needs == "",
	}
	for _, id := range []string{InsightOffpeakShift, InsightCostPer100, InsightCostPerKWh} {
		if has[id] {
			out.Order = append(out.Order, id)
		}
	}
	if len(out.Order) > 0 {
		out.HeadlineID = out.Order[0]
	}
	for _, id := range []string{InsightOffpeakShift, InsightCostPer100, InsightCostPerKWh} {
		if !has[id] {
			out.Order = append(out.Order, id)
		}
	}
	return out
}
