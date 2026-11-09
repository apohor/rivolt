package radar

import (
	"context"
	"math"
	"time"
)

// Level is the coarse reading shown to people. Amounts are never claimed.
type Level string

const (
	Dry  Level = "dry"
	Rain Level = "rain"
	Snow Level = "snow"
)

// RainDBZ is the reflectivity at which a cell counts as "rain likely". Below it, echoes are drizzle-or-less or clutter.
const RainDBZ = 20.0

// Reading is what the forecast says at one point and time.
type Reading struct {
	Level Level
	DBZ   float64
}

// At answers for a point at time t, or false when the forecast cannot say: radar is off, the point is outside the grid or the horizon, the
// newest usable cycle is too old, or the service did not answer. Absence is never reported as dry.
func (s *Store) At(ctx context.Context, lat, lon float64, t time.Time) (Reading, bool) {
	if s == nil {
		return Reading{}, false
	}
	m, err := s.manifest(ctx)
	if err != nil {
		return Reading{}, false
	}
	now := s.now()
	for _, c := range m.Cycles { // newest first
		if c.CycleTime.IsZero() || now.Sub(c.CycleTime) > MaxCycleAge || t.Before(c.CycleTime) {
			continue
		}
		have := map[int]bool{}
		for _, h := range c.Hours {
			have[h] = true
		}
		off := t.Sub(c.CycleTime).Hours()
		lo, hi := int(math.Floor(off)), int(math.Ceil(off))
		if !have[lo] || !have[hi] {
			continue
		}
		var best Echo
		for h := lo; h <= hi; h++ {
			g, err := s.grid(ctx, c.Cycle, h)
			if err != nil {
				return Reading{}, false
			}
			e := g.At(lat, lon)
			if !e.OK {
				return Reading{}, false
			}
			if !best.OK || e.DBZ > best.DBZ {
				best.DBZ, best.OK = e.DBZ, true
			}
			best.Snow = best.Snow || e.Snow
		}
		switch {
		case best.DBZ < RainDBZ:
			return Reading{Level: Dry, DBZ: best.DBZ}, true
		case best.Snow:
			return Reading{Level: Snow, DBZ: best.DBZ}, true
		}
		return Reading{Level: Rain, DBZ: best.DBZ}, true
	}
	return Reading{}, false
}
