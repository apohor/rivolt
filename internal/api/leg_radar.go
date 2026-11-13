package api

import (
	"context"
	"time"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/radar"
	"github.com/apohor/rivolt/internal/rivian"
)

// RadarSource is the forecast radar as the planner reads it: the reading at a point and time, or false when the forecast cannot say.
// *radar.Store satisfies it.
type RadarSource interface {
	At(ctx context.Context, lat, lon float64, t time.Time) (radar.Reading, bool)
}

type planRadarKey struct{}

// withPlanRadar puts the radar on the request's context for the plan handlers (nil: no radar tags).
func withPlanRadar(ctx context.Context, rs RadarSource) context.Context {
	if rs == nil {
		return ctx
	}
	return context.WithValue(ctx, planRadarKey{}, rs)
}

type planRadarFreeKey struct{}

// withPlanRadarPlan marks the request's account plan for the radar tags: a free account (pro false) gets only the teaser, never leg.radar.
func withPlanRadarPlan(ctx context.Context, pro bool) context.Context {
	if pro {
		return ctx
	}
	return context.WithValue(ctx, planRadarFreeKey{}, true)
}

// radarTeaserDTO is what a free account sees of the forecast radar (Pro): how many legs of the plan have rain or snow likely, out of the legs
// the forecast could read. It names no leg and no level, and comes from the same in-cluster grid as the Pro tags (no extra call).
type radarTeaserDTO struct {
	WetLegs int `json:"wet_legs"`
	Legs    int `json:"legs"`
}

const (
	// radarBudget bounds the radar work for one plan response (all days together): a leg not read in time simply has no tag.
	radarBudget = 2 * time.Second
	// radarStepM is the spacing of the points checked along a leg, and radarMaxSamples bounds them for one leg.
	radarStepM       = 20000.0
	radarMaxSamples  = 40
	radarMinCoverage = 0.8
)

// withRadarBudget is the one deadline for the radar work of a response.
func withRadarBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, radarBudget)
}

// legRadarDTO is the forecast along a leg at the times the truck is there: "dry", "rain" or "snow" (the worst of the points checked). It
// claims likelihood only, never an amount.
type legRadarDTO struct {
	Level string `json:"level"`
}

// addLegRadar fills Radar of each leg of adj from the forecast radar along the plan's first route, at the time the truck reaches each point.
// It does nothing without an adjustment, a radar on the request, or a road line, and never fails the plan. Like addLegClimb it has no deadline
// of its own: the caller puts one on ctx for the whole response.
func addLegRadar(ctx context.Context, adj *tripWeatherAdjustmentDTO, plan *rivian.TripPlan) {
	rs, _ := ctx.Value(planRadarKey{}).(RadarSource)
	if adj == nil || rs == nil || plan == nil || len(plan.Routes) == 0 {
		return
	}
	defer observePlanStage(ctx, "radar", time.Now())
	r := plan.Routes[0]
	got := routeLegRadar(ctx, rs, r.Geometry, r.GeometryPrecision, r.Waypoints)
	if len(got) != len(adj.Legs) {
		return
	}
	if free, _ := ctx.Value(planRadarFreeKey{}).(bool); free {
		t := radarTeaserDTO{}
		for _, g := range got {
			if g == "" {
				continue
			}
			t.Legs++
			if g != string(radar.Dry) {
				t.WetLegs++
			}
		}
		if t.Legs > 0 {
			adj.RadarTeaser = &t
		}
		return
	}
	for i, g := range got {
		if g != "" {
			adj.Legs[i].Radar = &legRadarDTO{Level: g}
		}
	}
}

// routeLegRadar returns, for each leg between consecutive waypoints, "dry", "rain" or "snow", or "" when the leg cannot be read: no line,
// no times, or fewer than radarMinCoverage of its points answered (a partial leg is never reported as dry).
func routeLegRadar(ctx context.Context, rs RadarSource, geometry string, precision int, wps []rivian.PlannedWaypoint) []string {
	if geometry == "" || precision != 6 || len(wps) < 2 {
		return nil
	}
	line := maps.DecodePolyline6(geometry)
	if len(line) < 2 {
		return nil
	}
	at := make([]int, len(wps))
	for i := 1; i < len(wps); i++ {
		if i == len(wps)-1 {
			at[i] = len(line) - 1
			break
		}
		at[i] = matchStop(line, at[i-1], wps[i].Latitude, wps[i].Longitude)
	}
	out := make([]string, len(wps)-1)
	for i := range out {
		if at[i+1] <= at[i] {
			continue
		}
		t0, ok0 := wpTime(wps[i].DepartureTimeUTC, wps[i].ArrivalTimeUTC)
		t1, ok1 := wpTime(wps[i+1].ArrivalTimeUTC, wps[i+1].DepartureTimeUTC)
		if !ok0 || !ok1 || t1.Before(t0) {
			continue
		}
		out[i] = legRadarLevel(ctx, rs, line[at[i]:at[i+1]+1], t0, t1)
	}
	return out
}

// wpTime is the first parseable of the given RFC3339 times.
func wpTime(vals ...string) (time.Time, bool) {
	for _, v := range vals {
		if v == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func legRadarLevel(ctx context.Context, rs RadarSource, leg [][2]float64, t0, t1 time.Time) string {
	total := lineLengthM(leg)
	n := int(total/radarStepM) + 1
	if n > radarMaxSamples {
		n = radarMaxSamples
	}
	// n+1 points from the start to the end of the leg, each at the time the truck is there (constant speed along the leg).
	answered, worst := 0, radar.Dry
	pts := n + 1
	run, seg := 0.0, 1
	for k := 0; k < pts; k++ {
		target := total * float64(k) / float64(pts-1)
		for seg < len(leg)-1 && run+distM(leg[seg-1][0], leg[seg-1][1], leg[seg][0], leg[seg][1]) < target {
			run += distM(leg[seg-1][0], leg[seg-1][1], leg[seg][0], leg[seg][1])
			seg++
		}
		a, b := leg[seg-1], leg[seg]
		d := distM(a[0], a[1], b[0], b[1])
		f := 0.0
		if d > 0 {
			f = (target - run) / d
		}
		if f < 0 {
			f = 0
		} else if f > 1 {
			f = 1
		}
		lat, lon := a[0]+f*(b[0]-a[0]), a[1]+f*(b[1]-a[1])
		frac := 0.0
		if total > 0 {
			frac = target / total
		}
		t := t0.Add(time.Duration(frac * float64(t1.Sub(t0))))
		rd, ok := rs.At(ctx, lat, lon, t)
		if !ok {
			continue
		}
		answered++
		switch {
		case rd.Level == radar.Snow:
			worst = radar.Snow
		case rd.Level == radar.Rain && worst == radar.Dry:
			worst = radar.Rain
		}
	}
	if float64(answered) < radarMinCoverage*float64(pts) {
		return ""
	}
	return string(worst)
}
