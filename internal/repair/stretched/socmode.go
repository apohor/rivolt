package stretched

import "time"

// socRiseStep is how far above the highest SoC seen so far a frame must be to count as the SoC rising: the readings jitter
// by a tenth of a point at rest.
const socRiseStep = 0.1

// capBySoC is mode A: the row has no plugged frame and no charging after its first drive (a vehicle that reports no
// charger status or power: the plug rule has nothing to see). Its charging, if any, shows only in the SoC before the first
// drive. The cap is the last frame before the drive that set a new SoC high, else the first drive's start, and never later
// than the drive. Energy and SoC are untouched. With fewer than two frames before the drive there is no evidence to judge.
func capBySoC(r Row) Verdict {
	skip := func(s string) Verdict { return Verdict{Reason: s} }
	var pre []Frame
	for _, f := range r.Frames {
		if f.At.After(r.FirstDrive) {
			break
		}
		pre = append(pre, f)
	}
	if len(pre) < 2 {
		return skip("no plugged frame inside, and too few frames before the first drive to judge the SoC")
	}
	high, cut := pre[0].SoC, time.Time{}
	for _, f := range pre[1:] {
		if f.SoC >= high+socRiseStep {
			high, cut = f.SoC, f.At
		}
	}
	if cut.IsZero() {
		cut = r.FirstDrive
	}
	switch {
	case cut.Before(r.StartedAt) || cut.After(r.FirstDrive):
		return skip("cap outside the span before the first drive")
	case !cut.Before(r.EndedAt) || r.EndedAt.Sub(cut) <= MinStretch:
		return skip("not stretched")
	}
	// The stored energy against what the SoC gain implies: capped all the same, counted when they disagree.
	gainKWh := (high - pre[0].SoC) / 100 * r.PackKWh
	off := r.PackKWh > 0 && !(r.EnergyKWh < 0.5 && gainKWh < 0.5) && !Agree(r.EnergyKWh, gainKWh)
	return Verdict{CapTo: cut, BySoC: true, SoCOff: off}
}
