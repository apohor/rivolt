package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/metrics"
)

// POST /api/trips/what-if: the no-commit "what if" preview of one edit to a plan's vias (add, move or remove), before the user commits a Rivian
// re-plan (one of the 60 planner calls an hour, several seconds). Stateless: the client sends the plan's compact state and the one edit; the
// server routes with OUR Valhalla on both sides of the change (never Rivian's leg times: they differ systematically from ours, so a delta of a
// plan time and a Valhalla time would carry that bias even with no edit), answers and stores nothing. No Rivian call.
//
// It keeps the plan's own stops and charge times and previews only the legs around the edit: added drive time and distance, the arrival shift,
// and the battery as a CHANGE at the next charging stop, with one-way warnings only (their absence never means "fine"). Everything is an
// estimate and says so. Design: _shared/what-if-preview-design.md, DA's ruling of Nov 5.

const (
	whatIfBodyMax     = 64 << 10
	whatIfMaxStops    = 60
	whatIfBudget      = 3 * time.Second
	tripsWhatIfPerMin = 60

	whatIfReservePct   = 10.0 // arrival reserve when the request names none
	whatIfSafety       = 1.15 // for the unknowns of a detour
	whatIfRoadFactor   = 1.3  // straight line to road, when Valhalla is out
	whatIfFallbackKmh  = 80.0
	whatIfMinSegmentM  = 1000.0
	whatIfRoundMinutes = 5
)

type whatIfStop struct {
	Lat                 float64  `json:"lat"`
	Lon                 float64  `json:"lon"`
	Source              string   `json:"source"`
	ArrivalSoC          *float64 `json:"arrival_soc,omitempty"`
	DepartureSoC        *float64 `json:"departure_soc,omitempty"`
	ArrivalAt           string   `json:"arrival_at,omitempty"`
	DepartureAt         string   `json:"departure_at,omitempty"`
	DistanceFromOriginM float64  `json:"distance_from_origin_m,omitempty"`
}

type whatIfEdit struct {
	Kind  string `json:"kind"`
	Index int    `json:"index"`
	Via   *struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"via,omitempty"`
}

type whatIfRequest struct {
	Stops                   []whatIfStop    `json:"stops"`
	Edit                    whatIfEdit      `json:"edit"`
	RoutePreferences        *tripRoutePrefs `json:"route_preferences,omitempty"`
	TargetArrivalSocPercent *float64        `json:"target_arrival_soc_percent,omitempty"`
}

type whatIfResponse struct {
	Estimate  bool   `json:"estimate"`
	Estimated bool   `json:"estimated"`
	Notice    string `json:"notice"`
	Segment   struct {
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"segment"`
	Geometry          string  `json:"geometry,omitempty"`
	GeometryPrecision int     `json:"geometry_precision,omitempty"`
	AddedM            float64 `json:"added_m"`
	AddedSec          float64 `json:"added_sec"`
	ArrivalShiftSec   float64 `json:"arrival_shift_sec"`
	AtEdit            *struct {
		ArrivalAt string `json:"arrival_at"`
	} `json:"at_edit,omitempty"`
	Destination *struct {
		ArrivalAt string `json:"arrival_at"`
	} `json:"destination,omitempty"`
	NextCharge *whatIfNextCharge `json:"next_charge,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
}

type whatIfNextCharge struct {
	Index         int     `json:"index"`
	IsDestination bool    `json:"is_destination"`
	SoCDropPts    float64 `json:"soc_drop_pts"`
}

// whatIfRoute is one routed side of the change: total length, total time and the line.
type whatIfRoute struct {
	m, sec   float64
	firstSec float64 // the time of the first leg (P to the edited point, on the new side)
	line     [][2]float64
}

func handleTripWhatIf(router geometryRouter, m *metrics.Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req whatIfRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, whatIfBodyMax))
		if err := dec.Decode(&req); err != nil {
			badRequest(w, "invalid body")
			return
		}
		if _, err := dec.Token(); err != io.EOF { // anything after the request, a stray } or ] included; whitespace is fine
			badRequest(w, "invalid body")
			return
		}
		old, nw, p, n, err := req.sides()
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		opts := routeOptsOf(req.RoutePreferences.toRivian())
		ctx, cancel := context.WithTimeout(r.Context(), whatIfBudget)
		defer cancel()
		oldRt, newRt, estimated := routeBothSides(ctx, router, m, old, nw, opts)
		writeJSON(w, http.StatusOK, req.answer(p, n, oldRt, newRt, estimated))
	}
}

// sides validates the request and returns the two location lists (before and after the edit), and the indexes of P and N, the stops either side.
func (q *whatIfRequest) sides() (old, nw [][2]float64, p, n int, err error) {
	if len(q.Stops) < 2 || len(q.Stops) > whatIfMaxStops {
		return nil, nil, 0, 0, errors.New("between 2 and 60 stops are needed")
	}
	for i, s := range q.Stops {
		if !validLatLon(s.Lat, s.Lon) {
			return nil, nil, 0, 0, errors.New("a stop has an invalid coordinate")
		}
		for _, v := range []*float64{s.ArrivalSoC, s.DepartureSoC} {
			if v != nil && (math.IsNaN(*v) || *v < 0 || *v > 100) {
				return nil, nil, 0, 0, errors.New("a stop's SoC must be between 0 and 100")
			}
		}
		if s.Source == "" && i == 0 {
			q.Stops[i].Source = "origin"
		}
	}
	if q.TargetArrivalSocPercent != nil && !validTargetPct(*q.TargetArrivalSocPercent) {
		return nil, nil, 0, 0, errors.New("target_arrival_soc_percent must be between 0 and 100")
	}
	e := q.Edit
	pt := func(i int) [2]float64 { return [2]float64{q.Stops[i].Lat, q.Stops[i].Lon} }
	switch e.Kind {
	case "add", "move":
		if e.Via == nil || !validLatLon(e.Via.Lat, e.Via.Lon) {
			return nil, nil, 0, 0, errors.New("the edit needs a via with a valid coordinate")
		}
	case "remove":
	default:
		return nil, nil, 0, 0, errors.New("edit.kind must be add, move or remove")
	}
	x := [2]float64{}
	if e.Via != nil {
		x = [2]float64{e.Via.Lat, e.Via.Lon}
	}
	switch e.Kind {
	case "add":
		if e.Index < 1 || e.Index > len(q.Stops)-1 {
			return nil, nil, 0, 0, errors.New("edit.index must be 1 to n-1 for an add")
		}
		p, n = e.Index-1, e.Index
		return [][2]float64{pt(p), pt(n)}, [][2]float64{pt(p), x, pt(n)}, p, n, nil
	default: // move, remove
		if e.Index < 1 || e.Index > len(q.Stops)-2 || q.Stops[e.Index].Source != "via" {
			return nil, nil, 0, 0, errors.New("only a via can be moved or removed")
		}
		p, n = e.Index-1, e.Index+1
		old = [][2]float64{pt(p), pt(e.Index), pt(n)}
		if e.Kind == "move" {
			return old, [][2]float64{pt(p), x, pt(n)}, p, n, nil
		}
		return old, [][2]float64{pt(p), pt(n)}, p, n, nil
	}
}

// routeBothSides routes the two location lists concurrently with the guarded router; if either fails (no router, Valhalla down, a refused
// shape, the budget) both fall back to a straight-line estimate with no line, and estimated is true.
func routeBothSides(ctx context.Context, router geometryRouter, m *metrics.Metrics, old, nw [][2]float64, opts maps.RouteOpts) (whatIfRoute, whatIfRoute, bool) {
	if router != nil && router.Enabled() {
		var oldRt, newRt whatIfRoute
		var errOld, errNew error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); oldRt, errOld = routeOne(ctx, router, old, opts, false) }()
		go func() { defer wg.Done(); newRt, errNew = routeOne(ctx, router, nw, opts, true) }()
		wg.Wait()
		if errOld == nil && errNew == nil {
			return oldRt, newRt, false
		}
		for _, err := range []error{errOld, errNew} {
			var bad *maps.BadShapeError
			if errors.As(err, &bad) && m != nil {
				m.RoutePointRefusedTotal.WithLabelValues("plan_" + bad.Kind).Inc()
			}
		}
	}
	return whatIfStraight(old), whatIfStraight(nw), true
}

func routeOne(ctx context.Context, router geometryRouter, locs [][2]float64, opts maps.RouteOpts, wantLine bool) (whatIfRoute, error) {
	rt, err := router.RouteChecked(ctx, locs, opts)
	if err != nil {
		return whatIfRoute{}, err
	}
	var out whatIfRoute
	if len(rt.Legs) > 0 {
		out.firstSec = rt.Legs[0].TimeSec
	}
	shapes := make([][][2]float64, len(rt.Legs))
	for k, leg := range rt.Legs {
		out.m += leg.LengthMeters
		out.sec += leg.TimeSec
		shapes[k] = leg.Shape
	}
	if !wantLine {
		return out, nil // the old side is for its length and time only
	}
	// The served line is thinned and capped like a plan's (the same tolerance and cap), and checked again with the same guard: an edit with
	// a far via is a long road, and this answer is asked for on every pause of a drag.
	thin := maps.SimplifyLegs(shapes, geometryToleranceM, maxGeometryPoints)
	for k, shape := range thin {
		if k+1 < len(locs) {
			if err := maps.CheckShape(shape, locs[k], locs[k+1]); err != nil {
				return whatIfRoute{}, err
			}
		}
	}
	out.line = maps.JoinLegs(thin)
	return out, nil
}

// whatIfStraight is the rough fallback: great-circle length x 1.3 at 80 km/h, no line.
func whatIfStraight(locs [][2]float64) whatIfRoute {
	var m, first float64
	for i := 0; i+1 < len(locs); i++ {
		d := distM(locs[i][0], locs[i][1], locs[i+1][0], locs[i+1][1]) * whatIfRoadFactor
		if i == 0 {
			first = d
		}
		m += d
	}
	speed := whatIfFallbackKmh / 3.6
	return whatIfRoute{m: m, sec: m / speed, firstSec: first / speed}
}

// answer builds the preview from the two routed sides. P and N are the stops either side of the edit.
func (q *whatIfRequest) answer(p, n int, oldRt, newRt whatIfRoute, estimated bool) whatIfResponse {
	var out whatIfResponse
	out.Estimate, out.Estimated, out.Notice = true, estimated, "estimate_replan"
	out.Segment.From, out.Segment.To = p, n
	if !estimated && len(newRt.line) >= 2 {
		out.Geometry, out.GeometryPrecision = maps.EncodePolyline6(newRt.line), 6
	}
	out.AddedM = round1(newRt.m - oldRt.m)
	out.AddedSec = math.Round(newRt.sec - oldRt.sec)
	out.ArrivalShiftSec = out.AddedSec

	// Clock times: secondary, rounded to 5 minutes. At the edited point: P's departure plus the time to get there; at the end: the plan's own
	// arrival plus the shift.
	if q.Edit.Kind != "remove" {
		if dep, ok := parseWhen(q.Stops[p].DepartureAt); ok {
			out.AtEdit = &struct {
				ArrivalAt string `json:"arrival_at"`
			}{roundWhen(dep.Add(time.Duration(newRt.firstSec * float64(time.Second))))}
		}
	}
	if last := q.Stops[len(q.Stops)-1]; last.ArrivalAt != "" {
		if at, ok := parseWhen(last.ArrivalAt); ok {
			out.Destination = &struct {
				ArrivalAt string `json:"arrival_at"`
			}{roundWhen(at.Add(time.Duration(out.AddedSec * float64(time.Second))))}
		}
	}

	q.battery(&out, p, n)
	return out
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func parseWhen(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

func roundWhen(t time.Time) string {
	d := time.Duration(whatIfRoundMinutes) * time.Minute
	return t.UTC().Round(d).Format(time.RFC3339)
}

// battery fills the battery change and the one-way warnings. It says something only when the edit ADDS distance (it never claims a gain) and
// the plan's own numbers allow it: the departure SoC at P, the arrival SoC at the next charging stop C (the first planner stop at or after N,
// else the destination) and the distances between. The rate is the worse of the P-to-C stretch's own and the whole plan's, times 1.15; the
// warning is raised when the estimated arrival at C falls below the reserve. Anything missing: no fields, no claim.
func (q *whatIfRequest) battery(out *whatIfResponse, p, n int) {
	if out.AddedM <= 0 {
		return
	}
	last := len(q.Stops) - 1
	c := last
	for j := n; j < last; j++ {
		if q.Stops[j].Source == "planner" {
			c = j
			break
		}
	}
	dep := socOf(q.Stops[p].DepartureSoC, q.Stops[p].ArrivalSoC)
	arr := socOf(q.Stops[c].ArrivalSoC, q.Stops[c].DepartureSoC)
	span := q.Stops[c].DistanceFromOriginM - q.Stops[p].DistanceFromOriginM
	if dep == nil || arr == nil || span < whatIfMinSegmentM || *dep-*arr <= 0 {
		return
	}
	segRate := (*dep - *arr) / span
	var used, dist float64
	for k := 0; k < last; k++ {
		from := socOf(q.Stops[k].DepartureSoC, q.Stops[k].ArrivalSoC)
		to := socOf(q.Stops[k+1].ArrivalSoC, q.Stops[k+1].DepartureSoC)
		d := q.Stops[k+1].DistanceFromOriginM - q.Stops[k].DistanceFromOriginM
		if from != nil && to != nil && d >= whatIfMinSegmentM && *from-*to > 0 {
			used += *from - *to
			dist += d
		}
	}
	if dist <= 0 {
		return
	}
	rate := math.Max(segRate, used/dist) * whatIfSafety
	drop := rate * out.AddedM
	out.NextCharge = &whatIfNextCharge{Index: c, IsDestination: c == last, SoCDropPts: round1(drop)}
	reserve := whatIfReservePct
	if q.TargetArrivalSocPercent != nil {
		reserve = *q.TargetArrivalSocPercent
	}
	if *arr-drop < reserve {
		if c == last {
			out.Warnings = append(out.Warnings, "may_not_reach_destination")
		} else {
			out.Warnings = append(out.Warnings, "may_not_reach_next_charge")
		}
	}
}

// socOf is the first SoC that is known: a stop passes through at one value, so a via with only one of arrival and departure is as good as both.
func socOf(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}
