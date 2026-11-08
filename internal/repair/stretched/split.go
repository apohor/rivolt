package stretched

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/apohor/rivolt/internal/charges"
)

// detectSegments is DetectSegments; a variable so tests can feed a plan without frames.
var detectSegments = DetectSegments

// An original is "empty" when nothing before the first drive was a charge: no plugged frame and an SoC gain under this
// many points. Its remainder (stored minus the new rows) may be at most the larger of the two bounds below.
const (
	emptyMaxSoCGain        = 0.5
	emptyMaxRemainderKWh   = 1.0
	emptyMaxRemainderShare = 0.25
)

// firstSoC is the SoC of the first frame (fallback when there is none).
func firstSoC(frames []Frame, fallback float64) float64 {
	if len(frames) == 0 {
		return fallback
	}
	return frames[0].SoC
}

// A split whose new rows exceed the stored energy by no more than max(clampMinKWh, clampShare of stored) is scaled down to it.
const (
	clampMinKWh = 1.0
	clampShare  = 0.05
)

// scaleNewRows scales the new rows' kWh proportionally so they sum to exactly total (the last takes the rounding), and
// recomputes their average power.
func scaleNewRows(news []NewRow, total, moved float64) {
	if moved <= 0 || len(news) == 0 {
		return
	}
	scale := total / moved
	var sum float64
	for i := range news {
		if i == len(news)-1 {
			news[i].KWh = total - sum
		} else {
			news[i].KWh *= scale
			sum += news[i].KWh
		}
		news[i].AvgPowerKW = avgKW(news[i].KWh, news[i].ActiveSeconds)
	}
}

// maxActiveGap caps the time one active frame stands for, so a hole in the data is not integrated as charging.
const maxActiveGap = 10 * time.Minute

// liveSessionID is the recorder's charge id format (internal/rivian liveSessionID(vehicleID, "c", t)): the Rivian
// vehicle id and the Unix second of the session's first frame, so a re-run of the recorder over the same session
// collapses to the same external id.
func liveSessionID(rivianVehicleID string, t time.Time) string {
	return fmt.Sprintf("live_%s_c_%d", rivianVehicleID, t.UTC().Unix())
}

// trySplit plans the split of a row with plugged frames after a drive: the original keeps the stretch before the
// first drive, ended at its last plugged frame, and every parked segment after a drive becomes a new row. It
// returns the plan and the original's new end, or a reason the row is flagged instead. Energy is conserved: the new
// rows' kWh (the power integral, cross-checked against the SoC gain) is taken off the original's stored energy,
// which must stay >= 0.
func trySplit(r Row, opt Options) (*Split, time.Time, string) {
	flag := func(why string) (*Split, time.Time, string) { return nil, time.Time{}, "flagged: " + why }
	switch {
	case len(r.Frames) == 0:
		return flag("no frames loaded for the split")
	case r.PackKWh <= 0:
		return flag("no pack size to cross-check the energy")
	case r.RivianVehicleID == "":
		return flag("no Rivian vehicle id for the external id")
	}
	cut := r.LastPluggedBeforeDrive
	// Nothing plugged before the first drive: the original may be empty (the real charge came after the drive). It is
	// then kept as a zero-energy abandoned row ended at the first drive, if the SoC did not rise before it either.
	empty := cut.IsZero()
	if empty {
		cut = r.FirstDrive
		if gain := socAt(r.Frames, r.FirstDrive, r.StartSoC) - firstSoC(r.Frames, r.StartSoC); gain >= emptyMaxSoCGain {
			return flag("no plugged frame before the first drive, but the SoC rose before it")
		}
	}
	if cut.Before(r.StartedAt) || !cut.Before(r.EndedAt) {
		return flag("cap outside the row")
	}
	segs := detectSegments(r.Frames, r.FirstDrive)
	if len(segs) == 0 {
		return flag("charging frames after a drive, no qualifying segment")
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].FirstActive.Before(segs[j].FirstActive) })
	sp := &Split{}
	var moved float64
	prevEnd := cut
	for i, sg := range segs {
		if !sg.FirstActive.After(prevEnd) {
			return flag("a segment overlaps the original or another segment")
		}
		// The charge ends at its last active frame, not at the parked frame that follows it.
		end := lastActive(r.Frames, sg.FirstActive, sg.End)
		if end.After(r.EndedAt) {
			end = r.EndedAt
		}
		if !end.After(sg.FirstActive) {
			return flag("an empty segment")
		}
		if !Agree(sg.IntegralKWh, sg.SoCKWh(r.PackKWh)) {
			return flag("energy figures disagree (power integral vs SoC gain)")
		}
		ext := liveSessionID(r.RivianVehicleID, sg.FirstActive)
		if r.Taken[ext] {
			return flag("external id already used: " + ext)
		}
		// The SoC at the charge's own start and end, not the parked stretch's: idle drain around it would skew the row.
		n := NewRow{ExternalID: ext, Start: sg.FirstActive, End: end, SoCStart: socAt(r.Frames, sg.FirstActive, sg.SoCStart), SoCEnd: socAt(r.Frames, end, sg.SoCEnd), KWh: sg.IntegralKWh}
		n.MaxPowerKW, n.ActiveSeconds = activeStats(r.Frames, n.Start, n.End)
		n.AvgPowerKW = avgKW(n.KWh, n.ActiveSeconds)
		n.FinalState = "charging_complete"
		if i == len(segs)-1 {
			n.FinalState = r.FinalState // the stretched row's last state belongs to its last charge
		}
		n.Lat, n.Lon = nearestLoc(r.Locs, n.Start)
		sp.News = append(sp.News, n)
		moved += n.KWh
		prevEnd = end
	}
	sp.OrigKWh = r.EnergyKWh - moved
	clamped := false
	if sp.OrigKWh < 0 {
		// A small excess is the figures' noise (a power integral against an integrated total): the new rows are scaled to the
		// stored energy, the original keeps exactly 0 and becomes an empty one, and conservation stays exact. A larger excess means
		// the stored energy missed part of a real charge: a separate energy repair, so the row stays flagged.
		excess := moved - r.EnergyKWh
		if excess > math.Max(clampMinKWh, clampShare*r.EnergyKWh) {
			return flag("the new rows' energy exceeds the row's stored energy")
		}
		if gain := socAt(r.Frames, r.FirstDrive, r.StartSoC) - firstSoC(r.Frames, r.StartSoC); gain >= emptyMaxSoCGain {
			return flag("the new rows' energy exceeds the stored energy and the SoC rose before the first drive")
		}
		scaleNewRows(sp.News, r.EnergyKWh, moved)
		sp.OrigKWh, sp.ClampedKWh, clamped = 0, excess, true
	}
	for i := range sp.News {
		sp.News[i].priceWith(r, opt) // after the position and the final energy: the place and the cost depend on them
	}
	sp.OrigEndSoC = socAt(r.Frames, cut, r.EndSoC)
	sp.EmptyOriginal = empty || clamped
	if !clamped { // a clamped original has remainder 0 by construction: nothing to compare with its own SoC gain
		if empty {
			// The remainder is kept (conservation stays exact), but an empty original must not be left holding real energy.
			if sp.OrigKWh > math.Max(emptyMaxRemainderKWh, emptyMaxRemainderShare*r.EnergyKWh) {
				return flag("an empty original would keep energy the new rows do not account for")
			}
		} else if !Agree(sp.OrigKWh, (sp.OrigEndSoC-r.StartSoC)/100*r.PackKWh) {
			// The remainder must agree with the original's own SoC gain, or the stored energy may not have held the tail.
			return flag("the original's remainder disagrees with its own SoC gain")
		}
	}
	sp.OrigMaxKW, sp.OrigActiveSec = activeStats(r.Frames, r.StartedAt, cut)
	sp.OrigAvgKW = avgKW(sp.OrigKWh, sp.OrigActiveSec)
	sp.OrigFinalState = "abandoned"
	if !empty && !clamped && sp.OrigEndSoC > r.StartSoC {
		sp.OrigFinalState = "charging_complete"
	}
	// An auto-priced original is re-priced by energy share (its own rate times what it keeps); a user's price never gets here.
	if (r.CostSource == "home_rate" || r.CostSource == "network_estimate") && r.PricePerKWh > 0 {
		c := math.Round(r.PricePerKWh*sp.OrigKWh*1e4) / 1e4
		sp.OrigCost = &c
	}
	return sp, cut, ""
}

// avgKW is the average power over the active hours (0 when there is none).
func avgKW(kwh, activeSec float64) float64 {
	if activeSec <= 0 {
		return 0
	}
	return kwh / (activeSec / 3600)
}

// activeStats is the peak power and the active charging time of the frames in [from, to].
func activeStats(frames []Frame, from, to time.Time) (maxKW, activeSec float64) {
	var prev *Frame
	for i := range frames {
		f := &frames[i]
		if f.At.Before(from) || f.At.After(to) {
			continue
		}
		if f.PowerKW > maxKW {
			maxKW = f.PowerKW
		}
		if prev != nil && prev.active() && f.active() {
			if dt := f.At.Sub(prev.At); dt > 0 && dt <= maxActiveGap {
				activeSec += dt.Seconds()
			}
		}
		prev = f
	}
	return
}

// socAt is the SoC of the last frame at or before t (fallback when none).
func socAt(frames []Frame, t time.Time, fallback float64) float64 {
	soc := fallback
	for _, f := range frames {
		if f.At.After(t) {
			break
		}
		soc = f.SoC
	}
	return soc
}

// nearestLoc is the position closest in time to t (0, 0 when there is none).
func nearestLoc(locs []Loc, t time.Time) (lat, lon float64) {
	best := time.Duration(math.MaxInt64)
	for _, l := range locs {
		if l.Lat == 0 && l.Lon == 0 {
			continue
		}
		d := l.At.Sub(t)
		if d < 0 {
			d = -d
		}
		if d < best {
			best, lat, lon = d, l.Lat, l.Lon
		}
	}
	return
}

// lastActive is the last charging_active frame with power in [from, to] (to when there is none).
func lastActive(frames []Frame, from, to time.Time) time.Time {
	last := to
	found := false
	for _, f := range frames {
		if f.At.Before(from) || f.At.After(to) {
			continue
		}
		if f.active() {
			last, found = f.At, true
		}
	}
	if !found {
		return to
	}
	return last
}

// priceWith prices and places the new row through opt.Price, as a charge built from it.
func (n *NewRow) priceWith(r Row, opt Options) {
	if opt.Price == nil {
		return
	}
	c := charges.Charge{StartedAt: n.Start, EndedAt: n.End, Lat: n.Lat, Lon: n.Lon, MaxPowerKW: n.MaxPowerKW,
		ActiveSeconds: n.ActiveSeconds, EnergyAddedKWh: n.KWh}
	opt.Price(r.UserID, &c)
	n.Cost, n.PricePerKW, n.Currency, n.CostSource, n.Network, n.Place = c.Cost, c.PricePerKWh, c.Currency, c.CostSource, c.Network, c.Place
}
