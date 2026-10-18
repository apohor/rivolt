// Package overnight suggests where to sleep at the end of a planned day: hotel
// Level 2 chargers from the NREL listing near the point where the day's drive
// budget runs out. It is archive-only (no Rivian call) and a static snapshot:
// a listed charger may be in use, broken or guests-only, so callers present
// these as "listed, not guaranteed".
package overnight

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/apohor/rivolt/internal/chargers"
)

// Archive is the part of chargers.Archive used here.
type Archive interface {
	QueryCorridor(path [][2]float64, filter chargers.Filter, opts chargers.QueryCorridorOptions) ([]chargers.POI, error)
}

// Defaults and limits.
const (
	DefaultBudgetHours = 8.0
	MaxPathPoints      = 20000
	windowMinutes      = 30.0 // look this far either side of the budget point
	corridorKm         = 12.0 // a hotel counts as near the route within this
	dcfcNearKm         = 8.0  // "within about 10 minutes" of a morning top-up
	assumedL2KW        = 7.0  // when the listing gives no power
	assumedSpeedMS     = 25.0 // 90 km/h, when the day's drive time isn't given
	maxCandidates      = 3
	// Hotels are ranked by a score in points; see score.
	maxScoredL2KW = 19.2
	weakL2KW      = 6.0
)

// Request describes one day's route and its drive budget.
type Request struct {
	// Path is the day's route as (lat, lon) pairs, start to end.
	Path [][2]float64
	// DriveSec is the route's driving time. 0 assumes 90 km/h.
	DriveSec float64
	// BudgetHours is the most driving in the day (0 uses the default of 8).
	// BudgetMiles, when positive, is used instead.
	BudgetHours float64
	BudgetMiles float64
}

// DCFC is a fast charger near a candidate, for a morning top-up.
type DCFC struct {
	Name       string  `json:"name,omitempty"`
	DistanceM  float64 `json:"distance_m"`
	MaxPowerKW float64 `json:"max_power_kw,omitempty"`
}

// Candidate is one suggested overnight stop.
type Candidate struct {
	Name         string  `json:"name"`
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	EntityID     string  `json:"entity_id,omitempty"`
	FacilityType string  `json:"facility_type,omitempty"`
	// L2KW is the listed power, or the assumed 7 kW when the listing has none
	// (then L2KWAssumed is true and the UI says so).
	L2KW        float64 `json:"l2_kw"`
	L2KWAssumed bool    `json:"l2_kw_assumed"`
	L2Count     int     `json:"l2_count,omitempty"`
	// DetourM is the extra distance to go there and back to the route.
	DetourM float64 `json:"detour_m"`
	// AlongM is how far along the day's route the stop is.
	AlongM float64 `json:"along_m"`
	// DCFCNearby is the closest fast charger within a few miles, if any.
	DCFCNearby *DCFC `json:"dcfc_nearby"`
}

// Target is the point the budget reaches.
type Target struct {
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	AlongM float64 `json:"along_m"`
	// ReachesEnd is true when the whole day fits in the budget; the stops are
	// then around the day's end.
	ReachesEnd bool `json:"reaches_end"`
}

// Result is the suggestion.
type Result struct {
	Candidates []Candidate `json:"candidates"`
	Target     Target      `json:"target"`
	RouteM     float64     `json:"route_m"`
}

// ErrBadRequest marks input the suggestion cannot work from.
var ErrBadRequest = errors.New("invalid overnight request")

// Suggest finds up to three hotel Level 2 stops near where the day's drive
// budget runs out. The result is deterministic for a given archive.
func Suggest(a Archive, req Request) (*Result, error) {
	if len(req.Path) < 2 {
		return nil, fmt.Errorf("%w: path needs at least 2 points", ErrBadRequest)
	}
	if len(req.Path) > MaxPathPoints {
		return nil, fmt.Errorf("%w: path has too many points", ErrBadRequest)
	}
	for _, p := range req.Path {
		if p[0] < -90 || p[0] > 90 || p[1] < -180 || p[1] > 180 || math.IsNaN(p[0]) || math.IsNaN(p[1]) {
			return nil, fmt.Errorf("%w: path has an invalid coordinate", ErrBadRequest)
		}
	}
	if req.BudgetHours < 0 || req.BudgetMiles < 0 || req.DriveSec < 0 {
		return nil, fmt.Errorf("%w: budget and drive time cannot be negative", ErrBadRequest)
	}
	pth := measure(req.Path)
	if pth.total <= 0 {
		return nil, fmt.Errorf("%w: path has no length", ErrBadRequest)
	}

	// How much of the route the budget buys.
	driveSec := req.DriveSec
	if driveSec <= 0 {
		driveSec = pth.total / assumedSpeedMS
	}
	secPerM := driveSec / pth.total
	var target float64
	switch {
	case req.BudgetMiles > 0:
		target = req.BudgetMiles * 1609.344
	default:
		h := req.BudgetHours
		if h <= 0 {
			h = DefaultBudgetHours
		}
		target = h * 3600 / secPerM
	}
	reachesEnd := target >= pth.total
	if reachesEnd {
		target = pth.total
	}
	win := windowMinutes * 60 / secPerM
	lo, hi := math.Max(0, target-win), math.Min(pth.total, target+win)
	tlat, tlon := pth.at(target)
	res := &Result{Candidates: []Candidate{}, RouteM: math.Round(pth.total), Target: Target{Lat: tlat, Lon: tlon, AlongM: math.Round(target), ReachesEnd: reachesEnd}}

	sub := pth.slice(lo, hi)
	if len(sub) < 2 {
		return res, nil
	}
	pois, err := a.QueryCorridor(sub, chargers.FilterHotels, chargers.QueryCorridorOptions{
		CorridorKm: corridorKm, EndpointTrimMeter: 1, IncludeEndpoints: true,
	})
	if err != nil {
		return nil, err
	}
	var all []scored
	for _, p := range pois {
		if !p.IsL2 {
			continue
		}
		along, off := pth.project(p.Lat, p.Lon)
		if along < lo || along > hi {
			continue
		}
		kw, assumed := p.MaxPowerKW, false
		if kw <= 0 {
			kw, assumed = assumedL2KW, true
		}
		// A hotel with a DC fast charger listed shows its fast power; the
		// overnight charge is the Level 2, so cap the claim at the L2 range.
		if kw > maxScoredL2KW && !assumed {
			kw = maxScoredL2KW
		}
		c := Candidate{
			Name: p.Name, Lat: p.Lat, Lon: p.Lon, EntityID: p.EntityID, FacilityType: p.FacilityType,
			L2KW: round1(kw), L2KWAssumed: assumed, L2Count: p.L2Count,
			DetourM: math.Round(2 * off), AlongM: math.Round(along),
		}
		all = append(all, scored{c: c})
	}
	// Rank on the listing alone first, then look for a morning fast charger
	// for the few that are in the running.
	for i := range all {
		all[i].score = score(all[i].c, false)
	}
	sort.SliceStable(all, func(i, j int) bool { return rankBefore(all[i], all[j]) })
	if len(all) > 10 {
		all = all[:10]
	}
	for i := range all {
		all[i].c.DCFCNearby = nearbyDCFC(a, all[i].c)
		all[i].score = score(all[i].c, all[i].c.DCFCNearby != nil)
	}
	sort.SliceStable(all, func(i, j int) bool { return rankBefore(all[i], all[j]) })
	for i := 0; i < len(all) && i < maxCandidates; i++ {
		res.Candidates = append(res.Candidates, all[i].c)
	}
	return res, nil
}

// score ranks a hotel: its Level 2 power matters most, a long detour costs,
// and a fast charger nearby is a bonus only when the Level 2 is weak (the
// morning top-up it makes up for).
func score(c Candidate, hasDCFC bool) float64 {
	s := math.Min(c.L2KW, maxScoredL2KW) / maxScoredL2KW * 50
	s -= c.DetourM / 1000 * 2
	if c.L2KWAssumed {
		s -= 3 // an unlisted power is a guess
	}
	if hasDCFC && c.L2KW < weakL2KW {
		s += 15
	}
	return s
}

type scored struct {
	c     Candidate
	score float64
}

// rankBefore orders by score, then earlier along the route, then name and
// id, so equal scores rank the same way every time.
func rankBefore(a, b scored) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.c.AlongM != b.c.AlongM {
		return a.c.AlongM < b.c.AlongM
	}
	return a.c.EntityID+a.c.Name < b.c.EntityID+b.c.Name
}

// nearbyDCFC is the closest DC fast charger within a few miles of the hotel.
func nearbyDCFC(a Archive, c Candidate) *DCFC {
	const eps = 0.0001
	pois, err := a.QueryCorridor([][2]float64{{c.Lat, c.Lon}, {c.Lat + eps, c.Lon}}, chargers.FilterDCFC, chargers.QueryCorridorOptions{
		CorridorKm: dcfcNearKm, EndpointTrimMeter: 1, IncludeEndpoints: true, MinPowerKW: 50,
	})
	if err != nil {
		return nil
	}
	var best *DCFC
	for _, p := range pois {
		d := haversine([2]float64{c.Lat, c.Lon}, [2]float64{p.Lat, p.Lon})
		if d > dcfcNearKm*1000 {
			continue
		}
		if best == nil || d < best.DistanceM {
			best = &DCFC{Name: p.Name, DistanceM: math.Round(d), MaxPowerKW: p.MaxPowerKW}
		}
	}
	return best
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// ---- the route as a measured path ------------------------------------------

type pt struct {
	lat, lon, dist float64
}

type path struct {
	pts   []pt
	total float64
}

func measure(p [][2]float64) *path {
	out := &path{pts: make([]pt, len(p))}
	for i, c := range p {
		if i > 0 {
			out.total += haversine(p[i-1], c)
		}
		out.pts[i] = pt{c[0], c[1], out.total}
	}
	return out
}

func (p *path) at(d float64) (lat, lon float64) {
	d = math.Max(0, math.Min(d, p.total))
	i := sort.Search(len(p.pts), func(i int) bool { return p.pts[i].dist >= d })
	if i == 0 {
		return p.pts[0].lat, p.pts[0].lon
	}
	if i >= len(p.pts) {
		l := p.pts[len(p.pts)-1]
		return l.lat, l.lon
	}
	a, b := p.pts[i-1], p.pts[i]
	f := 0.0
	if b.dist > a.dist {
		f = (d - a.dist) / (b.dist - a.dist)
	}
	return a.lat + (b.lat-a.lat)*f, a.lon + (b.lon-a.lon)*f
}

// slice returns the route between two distances, with interpolated ends.
func (p *path) slice(lo, hi float64) [][2]float64 {
	if hi <= lo {
		return nil
	}
	la, lb := p.at(lo)
	out := [][2]float64{{la, lb}}
	for _, q := range p.pts {
		if q.dist > lo && q.dist < hi {
			out = append(out, [2]float64{q.lat, q.lon})
		}
	}
	ha, hb := p.at(hi)
	return append(out, [2]float64{ha, hb})
}

// project is how far along the route the point nearest (lat, lon) is, and how
// far off the route it lies.
func (p *path) project(lat, lon float64) (dist, offM float64) {
	best := math.MaxFloat64
	cosLat := math.Cos(lat * math.Pi / 180)
	const mPerDeg = 111195.0
	for i := 1; i < len(p.pts); i++ {
		a, b := p.pts[i-1], p.pts[i]
		ax, ay := (a.lon-lon)*cosLat*mPerDeg, (a.lat-lat)*mPerDeg
		bx, by := (b.lon-lon)*cosLat*mPerDeg, (b.lat-lat)*mPerDeg
		dx, dy := bx-ax, by-ay
		t := 0.0
		if l2 := dx*dx + dy*dy; l2 > 0 {
			t = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/l2))
		}
		if d := math.Hypot(ax+t*dx, ay+t*dy); d < best {
			best, dist = d, a.dist+t*(b.dist-a.dist)
		}
	}
	return dist, best
}

func haversine(a, b [2]float64) float64 {
	const r = 6371000.0
	la1, la2 := a[0]*math.Pi/180, b[0]*math.Pi/180
	dla, dlo := la2-la1, (b[1]-a[1])*math.Pi/180
	h := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(h)))
}
