package ledger

import (
	"math"
	"sort"
	"time"

	"github.com/apohor/rivolt/internal/offpeak"
)

// Savings is the "what moving home charging to your cheapest hours could have
// saved" estimate for a stretch of months. It looks only at the user's own home
// AC sessions, exactly the ones the Money statement counts as home AC charging
// (so AsChargedTotal equals that line), prices each at the cheapest time its
// plug-in could have held the charge, and leaves out the sessions that could not
// move. It is an estimate: the plug-in span is the best we know of when the
// charge could have run, and the user's real bill can differ.
type Savings struct {
	// Considered is how many home AC sessions were looked at (priced, counted in
	// spend). Moved of them would have cost less in a cheaper window; LeftOut
	// could not move or would not have been cheaper (Reasons says why).
	Considered int            `json:"sessions_considered"`
	Moved      int            `json:"sessions_moved"`
	LeftOut    int            `json:"sessions_left_out"`
	Reasons    map[string]int `json:"left_out_reasons"`
	// AsChargedTotal is what Money counts for all considered sessions;
	// IfMovedTotal is the same sessions with the moved ones at their cheaper rate.
	AsChargedTotal float64 `json:"as_charged_total"`
	IfMovedTotal   float64 `json:"if_moved_total"`
	// CouldHaveSaved is AsChargedTotal - IfMovedTotal, never negative. Absent
	// (with Needs set) when the estimate cannot be made yet.
	CouldHaveSaved *float64 `json:"could_have_saved,omitempty"`
	// Needs says what is missing: "home_rate", "offpeak_schedule" or
	// "more_home_sessions". When set, no figure is given.
	Needs string `json:"needs,omitempty"`
}

// Reasons a considered session is left out of the saving.
const (
	ReasonTooBrief       = "plugged_in_too_briefly" // no single stretch of the plug-in could hold the charge
	ReasonNoCheaperHours = "no_cheaper_hours"       // the cheapest hours it could use cost no less than it paid
)

// MinSavingsSessions is the fewest home sessions an estimate needs before a
// number is shown.
const MinSavingsSessions = 3

// HomeSession is one home AC session as the Money statement counts it.
type HomeSession struct {
	ID         string
	Start, End time.Time // plugged in
	KWh        float64
	ActiveSec  float64 // time actually drawing power; 0 when not recorded
	PeakKW     float64 // 0 when not recorded
	Cost       float64 // what the statement counts
}

// HomeACSessions returns the sessions Build counts into
// energy.by_place_kind.home.ac with a cost for period p: the same dedupe,
// billed-cost rule, month and currency checks, so their costs add up to that line.
func HomeACSessions(in Input, p Period) []HomeSession {
	charges := applyBilled(in.Charges, in.Config)
	eff, _ := EffectiveCharges(charges)
	var out []HomeSession
	for _, c := range eff {
		if c.StartedAt.Before(p.Start) || !c.StartedAt.Before(p.End) {
			continue
		}
		v := viewOf(c, in.Config)
		if v.kind != "ac" || v.place != "home" || !v.spendOK || hasFlag(v.flags, FlagCurrencyMismatch) {
			continue
		}
		out = append(out, HomeSession{
			ID: c.ID, Start: c.StartedAt, End: c.EndedAt,
			KWh: val(c.EnergyKWh), ActiveSec: val(c.ActiveSeconds), PeakKW: val(c.PeakKW), Cost: *c.Cost,
		})
	}
	return out
}

// assumedHomeKW is the charging power assumed for a session that recorded neither
// active time nor a peak.
const assumedHomeKW = 7.0

// EstimateSavings prices sessions at the cheapest hours of sch they could have
// used. baseRate is the flat home rate that applies outside every window.
func EstimateSavings(sessions []HomeSession, sch offpeak.Schedule, baseRate float64) Savings {
	out := Savings{Reasons: map[string]int{}}
	for _, s := range sessions {
		out.Considered++
		out.AsChargedTotal += s.Cost
		moved, reason := cheapestMove(s, sch, baseRate)
		switch {
		case reason != "":
			out.LeftOut++
			out.Reasons[reason]++
			out.IfMovedTotal += s.Cost
		case moved >= s.Cost-0.005:
			out.LeftOut++
			out.Reasons[ReasonNoCheaperHours]++
			out.IfMovedTotal += s.Cost
		default:
			out.Moved++
			out.IfMovedTotal += moved
		}
	}
	out.AsChargedTotal = round2(out.AsChargedTotal)
	out.IfMovedTotal = round2(out.IfMovedTotal)
	switch {
	case baseRate <= 0:
		out.Needs = "home_rate"
	case len(sch.Windows) == 0:
		out.Needs = "offpeak_schedule"
	case out.Considered < MinSavingsSessions:
		out.Needs = "more_home_sessions"
	}
	if out.Needs == "" {
		s := math.Max(0, round2(out.AsChargedTotal-out.IfMovedTotal))
		out.CouldHaveSaved = &s
	}
	return out
}

// cheapestMove is what the session's energy would cost at the cheapest single
// stretch of its plug-in that can hold the whole charge. reason is set when no
// stretch can.
func cheapestMove(s HomeSession, sch offpeak.Schedule, baseRate float64) (cost float64, reason string) {
	span := s.End.Sub(s.Start)
	if s.KWh <= 0 || span <= 0 {
		return s.Cost, ReasonTooBrief
	}
	d := time.Duration(s.ActiveSec * float64(time.Second))
	if d <= 0 {
		kw := s.PeakKW
		if kw <= 0 {
			kw = assumedHomeKW
		}
		d = time.Duration(s.KWh / kw * float64(time.Hour))
	}
	if d > span {
		d = span
	}
	type seg struct {
		start, end time.Time
		rate       float64
	}
	var segs []seg
	cursor := s.Start
	ins := sch.Instances(s.Start, s.End)
	sort.Slice(ins, func(i, j int) bool { return ins[i].Start.Before(ins[j].Start) })
	for _, w := range ins {
		ws, we := w.Start, w.End
		if ws.Before(s.Start) {
			ws = s.Start
		}
		if we.After(s.End) {
			we = s.End
		}
		if ws.After(cursor) {
			segs = append(segs, seg{cursor, ws, baseRate}) // the hours between windows
		}
		segs = append(segs, seg{ws, we, w.Rate})
		if we.After(cursor) {
			cursor = we
		}
	}
	if cursor.Before(s.End) {
		segs = append(segs, seg{cursor, s.End, baseRate})
	}
	best := math.Inf(1)
	for _, g := range segs {
		if g.end.Sub(g.start) >= d && g.rate < best {
			best = g.rate
		}
	}
	if math.IsInf(best, 1) {
		return s.Cost, ReasonTooBrief
	}
	return s.KWh * best, ""
}
