package stretched

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// detectSegments is DetectSegments; a variable so tests can feed a plan without frames.
var detectSegments = DetectSegments

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
func trySplit(r Row) (*Split, time.Time, string) {
	flag := func(why string) (*Split, time.Time, string) { return nil, time.Time{}, "flagged: " + why }
	switch {
	case len(r.Frames) == 0:
		return flag("no frames loaded for the split")
	case r.LastPluggedBeforeDrive.IsZero():
		return flag("no plugged frame before the first drive")
	case r.PackKWh <= 0:
		return flag("no pack size to cross-check the energy")
	case r.RivianVehicleID == "":
		return flag("no Rivian vehicle id for the external id")
	}
	cut := r.LastPluggedBeforeDrive
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
	for _, sg := range segs {
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
		n := NewRow{ExternalID: ext, Start: sg.FirstActive, End: end, SoCStart: sg.SoCStart, SoCEnd: sg.SoCEnd, KWh: sg.IntegralKWh}
		n.MaxPowerKW, n.ActiveSeconds = activeStats(r.Frames, n.Start, n.End)
		n.Lat, n.Lon = nearestLoc(r.Locs, n.Start)
		sp.News = append(sp.News, n)
		moved += n.KWh
		prevEnd = end
	}
	sp.OrigKWh = r.EnergyKWh - moved
	if sp.OrigKWh < 0 {
		return flag("the new rows' energy exceeds the row's stored energy")
	}
	sp.OrigEndSoC = socAt(r.Frames, cut, r.EndSoC)
	// An auto-priced original is re-priced by energy share (its own rate times what it keeps); a user's price never gets here.
	if (r.CostSource == "home_rate" || r.CostSource == "network_estimate") && r.PricePerKWh > 0 {
		c := math.Round(r.PricePerKWh*sp.OrigKWh*1e4) / 1e4
		sp.OrigCost = &c
	}
	return sp, cut, ""
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
		if prev != nil && f.ChargingState == "charging_active" && f.PowerKW > 0 {
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
