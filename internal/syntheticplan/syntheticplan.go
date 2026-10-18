// Package syntheticplan is the deterministic trip planner that stands in for
// the gateway on installs that have no Rivian access (preview): a Valhalla
// route, an energy model, and charging stops picked from the DC fast-charger
// archive. It is a plausible plan, not Rivian's: every plan it returns is
// marked Simulated. The same inputs, route and archive give the same plan.
// The only clock is Planner.Now, which tests pin.
package syntheticplan

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// Router is the part of maps.Valhalla the planner uses.
type Router interface {
	Route(ctx context.Context, locs [][2]float64, opts maps.RouteOpts) (maps.Route, error)
}

// Chargers is the part of chargers.Archive the planner uses.
type Chargers interface {
	QueryCorridor(path [][2]float64, filter chargers.Filter, opts chargers.QueryCorridorOptions) ([]chargers.POI, error)
}

// Vehicle is the energy model of the planned truck.
type Vehicle struct {
	PackKWh  float64
	MiPerKWh float64 // average efficiency
	DCMaxKW  float64 // the truck's peak DC rate
}

// SampleVehicle is the sample truck's energy model (the replay's vehicle).
func SampleVehicle() Vehicle {
	return Vehicle{PackKWh: rivian.SamplePackKWh, MiPerKWh: rivian.SampleMiPerKWh, DCMaxKW: 220}
}

// LazyArchive loads the charger archive on first use, as the chargers-along
// endpoint does, then queries it.
type LazyArchive struct{ A *chargers.Archive }

// QueryCorridor implements Chargers.
func (l LazyArchive) QueryCorridor(path [][2]float64, f chargers.Filter, o chargers.QueryCorridorOptions) ([]chargers.POI, error) {
	if l.A == nil {
		return nil, errors.New("syntheticplan: no charger archive")
	}
	if l.A.LoadedAt().IsZero() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := l.A.Reload(ctx); err != nil {
			return nil, err
		}
	}
	return l.A.QueryCorridor(path, f, o)
}

// Planner implements rivian.TripPlanner.
type Planner struct {
	Router   Router
	Chargers Chargers
	Vehicle  Vehicle
	Now      func() time.Time
}

var _ rivian.TripPlanner = (*Planner)(nil)

// Tunables. Waypoint limits match the gateway's (2 to 10 including the
// origin and the destination).
const (
	minWaypoints   = 2
	maxWaypoints   = 10
	defaultReserve = 10.0 // SoC kept at arrival when no target was asked for
	taper          = 0.62 // average / peak kW over a fast charge (sample_plan.go)
	corridorKm     = 5.0  // a station counts as on the route within this
	minStationKW   = 50.0 // slower chargers are not fast-charge stops
	chargeTo       = 80.0 // default charge target
	chargeCap      = 90.0 // never plan a charge above this
	finishMargin   = 2.0  // SoC points above what the rest of the trip needs
	minTopUp       = 5.0  // the least a stop charges, in SoC points
	tieKm          = 10.0 // preferred networks win ties this close to the best
	minLegM        = 1000 // a stop must be at least this far on
	maxStops       = 25
	trailerFactor  = 1.6
	metersPerMile  = 1609.344
)

func driveFactor(mode string) float64 {
	switch mode {
	case settings.DriveModeDistance:
		return 0.95
	case settings.DriveModeSport:
		return 1.10
	}
	return 1.0
}

// PlanTrip implements rivian.TripPlanner.
func (p *Planner) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	n := len(in.Waypoints)
	if n < minWaypoints || n > maxWaypoints {
		return nil, fmt.Errorf("%w: %d waypoints (need %d to %d)", rivian.ErrInvalidTrip, n, minWaypoints, maxWaypoints)
	}
	if in.StartingSoC <= 0 || in.StartingSoC > 100 {
		return nil, fmt.Errorf("%w: starting SoC %.1f", rivian.ErrInvalidTrip, in.StartingSoC)
	}
	if p.Router == nil || p.Vehicle.PackKWh <= 0 || p.Vehicle.MiPerKWh <= 0 {
		return nil, errors.New("syntheticplan: planner not configured")
	}
	locs := make([][2]float64, n)
	for i, w := range in.Waypoints {
		locs[i] = [2]float64{w.Latitude, w.Longitude}
	}
	route, err := p.Router.Route(ctx, locs, maps.RouteOpts{})
	if errors.Is(err, maps.ErrNoRoute) {
		return nil, fmt.Errorf("%w: no road route between these points", rivian.ErrInvalidTrip)
	}
	if err != nil {
		return nil, fmt.Errorf("syntheticplan: route: %w", err)
	}
	if len(route.Legs) != n-1 {
		return nil, fmt.Errorf("syntheticplan: %d legs for %d waypoints", len(route.Legs), n)
	}
	path := newPath(route)
	if path.total <= 0 {
		return nil, errors.New("syntheticplan: empty route")
	}

	// Energy: SoC points per metre.
	factor := driveFactor(in.DriveMode)
	if in.TrailerProfile != "" {
		factor *= trailerFactor
	}
	kwhPerM := factor / (p.Vehicle.MiPerKWh * metersPerMile)
	socPerM := kwhPerM / p.Vehicle.PackKWh * 100
	reserve := defaultReserve
	if in.TargetArrivalSocPercent != nil {
		reserve = *in.TargetArrivalSocPercent
	}

	pois, err := p.corridorStations(path)
	if err != nil {
		return nil, err
	}
	stops, empty := planStops(path, pois, in, socPerM, reserve, p.Vehicle.PackKWh)
	return p.assemble(in, path, stops, empty, len(pois) > 0, socPerM, kwhPerM), nil
}

// ---- the route as one measured path ----------------------------------------

type pathPoint struct {
	lat, lon float64
	dist     float64 // metres from the origin, scaled so each leg ends at its Valhalla length
}

type path struct {
	pts      []pathPoint
	legEnd   []float64 // cumulative metres at the end of each leg
	legTime  []float64
	legStart []float64
	total    float64
}

func newPath(r maps.Route) *path {
	p := &path{}
	var base float64
	for _, leg := range r.Legs {
		length := leg.LengthMeters
		// Scale the shape's own geometry onto Valhalla's stated leg length.
		geo := make([]float64, len(leg.Shape))
		for i := 1; i < len(leg.Shape); i++ {
			geo[i] = geo[i-1] + haversine(leg.Shape[i-1], leg.Shape[i])
		}
		scale := 1.0
		if g := geo[len(geo)-1]; g > 0 && length > 0 {
			scale = length / g
		} else if length <= 0 {
			length = geo[len(geo)-1]
		}
		p.legStart = append(p.legStart, base)
		for i, pt := range leg.Shape {
			if len(p.pts) > 0 && i == 0 {
				continue // the previous leg ended on this point
			}
			p.pts = append(p.pts, pathPoint{pt[0], pt[1], base + geo[i]*scale})
		}
		base += length
		p.legEnd = append(p.legEnd, base)
		p.legTime = append(p.legTime, leg.TimeSec)
	}
	p.total = base
	return p
}

func (p *path) latLons() [][2]float64 {
	out := make([][2]float64, len(p.pts))
	for i, pt := range p.pts {
		out[i] = [2]float64{pt.lat, pt.lon}
	}
	return out
}

// at is the point d metres along the route.
func (p *path) at(d float64) (lat, lon float64) {
	d = math.Max(0, math.Min(d, p.total))
	i := sort.Search(len(p.pts), func(i int) bool { return p.pts[i].dist >= d })
	if i == 0 {
		return p.pts[0].lat, p.pts[0].lon
	}
	if i >= len(p.pts) {
		last := p.pts[len(p.pts)-1]
		return last.lat, last.lon
	}
	a, b := p.pts[i-1], p.pts[i]
	f := 0.0
	if b.dist > a.dist {
		f = (d - a.dist) / (b.dist - a.dist)
	}
	return a.lat + (b.lat-a.lat)*f, a.lon + (b.lon-a.lon)*f
}

// secs is the driving time to d metres along the route (Valhalla's, per leg).
func (p *path) secs(d float64) float64 {
	var t float64
	for i := range p.legEnd {
		if d >= p.legEnd[i] {
			t += p.legTime[i]
			continue
		}
		if l := p.legEnd[i] - p.legStart[i]; l > 0 {
			t += p.legTime[i] * (d - p.legStart[i]) / l
		}
		break
	}
	return t
}

// project is how far along the route the point nearest (lat, lon) is, and
// how far off the route it lies. It projects onto the segments, not just the
// vertices, so a coarse shape does not snap stations to its corners.
func (p *path) project(lat, lon float64) (dist, offM float64) {
	best := math.MaxFloat64
	cosLat := math.Cos(lat * math.Pi / 180)
	const mPerDeg = 111195.0
	for i := 1; i < len(p.pts); i++ {
		a, b := p.pts[i-1], p.pts[i]
		// Local planar metres around the query point.
		ax, ay := (a.lon-lon)*cosLat*mPerDeg, (a.lat-lat)*mPerDeg
		bx, by := (b.lon-lon)*cosLat*mPerDeg, (b.lat-lat)*mPerDeg
		dx, dy := bx-ax, by-ay
		t := 0.0
		if l2 := dx*dx + dy*dy; l2 > 0 {
			t = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/l2))
		}
		px, py := ax+t*dx, ay+t*dy
		if d := math.Hypot(px, py); d < best {
			best = d
			dist = a.dist + t*(b.dist-a.dist)
		}
	}
	if len(p.pts) == 1 {
		return p.pts[0].dist, haversine([2]float64{lat, lon}, [2]float64{p.pts[0].lat, p.pts[0].lon})
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

// ---- stations --------------------------------------------------------------

type station struct {
	poi       chargers.POI
	dist      float64 // metres along the route
	preferred bool
	kw        float64 // the rate the truck can use
}

func (p *Planner) corridorStations(pt *path) ([]station, error) {
	if p.Chargers == nil {
		return nil, nil
	}
	pois, err := p.Chargers.QueryCorridor(pt.latLons(), chargers.FilterDCFC, chargers.QueryCorridorOptions{
		CorridorKm: corridorKm, MinPowerKW: minStationKW, EndpointTrimMeter: 1, IncludeEndpoints: true,
	})
	if err != nil {
		return nil, fmt.Errorf("syntheticplan: chargers: %w", err)
	}
	var out []station
	for _, poi := range pois {
		d, off := pt.project(poi.Lat, poi.Lon)
		if off > corridorKm*1000*1.5 { // the archive's band is a box; keep it to the corridor
			continue
		}
		kw := poi.MaxPowerKW
		if kw <= 0 || (p.Vehicle.DCMaxKW > 0 && kw > p.Vehicle.DCMaxKW) {
			kw = p.Vehicle.DCMaxKW
		}
		if kw <= 0 {
			kw = 150
		}
		out = append(out, station{poi: poi, dist: d, kw: kw})
	}
	// Stable order so the same archive always gives the same pick.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].dist != out[j].dist {
			return out[i].dist < out[j].dist
		}
		return out[i].poi.EntityID+out[i].poi.Name < out[j].poi.EntityID+out[j].poi.Name
	})
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// markPreferred flags stations of a network the user ranked above zero.
func markPreferred(sts []station, prefs []rivian.NetworkPreference) {
	want := map[string]bool{}
	for _, np := range prefs {
		if np.Preference > 0 {
			want[np.NetworkID] = true
		}
	}
	if len(want) == 0 {
		return
	}
	for i := range sts {
		n := dcfcrates.MatchNetwork(sts[i].poi.Name + " " + sts[i].poi.EVNetwork)
		for _, id := range n.RivianIDs {
			if want[id] {
				sts[i].preferred = true
			}
		}
	}
}

// ---- planning --------------------------------------------------------------

type plannedStop struct {
	st        station
	arrSoC    float64
	depSoC    float64
	durSec    float64
	arrivedAt float64 // metres
}

type emptyPoint struct {
	dist float64
}

// planStops walks the route greedily: while the state of charge would drop
// below the reserve before the destination, charge at the furthest station
// that can still be reached with the reserve intact.
func planStops(pt *path, sts []station, in rivian.PlanTripInput, socPerM, reserve, packKWh float64) ([]plannedStop, *emptyPoint) {
	stops, empty := placeStops(pt, sts, in, socPerM, reserve)
	return settleCharges(stops, pt.total, socPerM, reserve, packKWh, in.StartingSoC), empty
}

// placeStops picks where to charge.
func placeStops(pt *path, sts []station, in rivian.PlanTripInput, socPerM, reserve float64) ([]plannedStop, *emptyPoint) {
	markPreferred(sts, in.NetworkPreferences)
	var stops []plannedStop
	d, soc := 0.0, in.StartingSoC
	for len(stops) < maxStops {
		if soc-socPerM*(pt.total-d) >= reserve {
			return stops, nil // the destination is reachable with the reserve
		}
		reach := d + (soc-reserve)/socPerM
		var cands []station
		for _, s := range sts {
			if s.dist > d+minLegM && s.dist <= reach {
				cands = append(cands, s)
			}
		}
		if len(cands) == 0 {
			if soc-socPerM*(pt.total-d) >= 0 {
				return stops, nil // arrives below the reserve but with charge left
			}
			return stops, &emptyPoint{dist: d + soc/socPerM}
		}
		best := cands[0]
		for _, c := range cands {
			if c.dist > best.dist {
				best = c
			}
		}
		pick := best
		for _, c := range cands {
			// Within tieKm of the furthest, a preferred network wins; then power; then position.
			if best.dist-c.dist > tieKm*1000 {
				continue
			}
			if better(c, pick) {
				pick = c
			}
		}
		arr := soc - socPerM*(pick.dist-d)
		// To place the stops, assume a charge to 80, or only to what the rest of
		// the trip needs when one charge can finish it. The charge each stop
		// really gets is settled below, once the next stop is known.
		need := reserve + socPerM*(pt.total-pick.dist)
		target := chargeTo
		if need+finishMargin <= chargeCap {
			target = math.Ceil(need + finishMargin)
		}
		if target <= arr {
			target = math.Min(chargeCap, arr+1)
		}
		stops = append(stops, plannedStop{st: pick, arrSoC: arr, depSoC: target, arrivedAt: pick.dist})
		d, soc = pick.dist, target
	}
	return stops, nil
}

// settleCharges decides how far each stop charges: only as far as the next
// stop (or the destination) needs, plus the reserve and a small margin, never
// below a useful top-up and never above the cap. A stop that sits a few
// miles short of a via or the destination is a short top-up, not a charge to
// 80. Each stop's arrival SoC follows from the charge before it.
func settleCharges(stops []plannedStop, total, socPerM, reserve, packKWh, startSoC float64) []plannedStop {
	soc, at := startSoC, 0.0
	for i := range stops {
		st := &stops[i]
		st.arrSoC = soc - socPerM*(st.arrivedAt-at)
		next := total
		if i+1 < len(stops) {
			next = stops[i+1].arrivedAt
		}
		target := math.Ceil(reserve + socPerM*(next-st.arrivedAt) + finishMargin)
		target = math.Max(target, st.arrSoC+minTopUp)
		target = math.Min(target, chargeCap)
		if target <= st.arrSoC {
			target = math.Min(chargeCap, st.arrSoC+1)
		}
		st.depSoC = target
		st.durSec = math.Round((target - st.arrSoC) / 100 * packKWh / (st.st.kw * taper) * 3600)
		soc, at = target, st.arrivedAt
	}
	return stops
}

func better(c, cur station) bool {
	if c.preferred != cur.preferred {
		return c.preferred
	}
	if c.kw != cur.kw {
		return c.kw > cur.kw
	}
	return c.dist > cur.dist
}

// ---- the plan --------------------------------------------------------------

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// assemble turns the stops into the gateway-shaped plan: ORIGIN, the user's
// intermediate stops (WAYPOINT), the chosen DC_CHARGE_STATIONs in route order,
// and DESTINATION, each with arrival and departure times and SoC.
func (p *Planner) assemble(in rivian.PlanTripInput, pt *path, stops []plannedStop, empty *emptyPoint, stationsAvailable bool, socPerM, kwhPerM float64) *rivian.TripPlan {
	depart := in.DepartureTime
	if depart.IsZero() {
		now := time.Now
		if p.Now != nil {
			now = p.Now
		}
		depart = now().Truncate(time.Minute)
	}
	depart = depart.UTC()
	stamp := func(offsetSec float64) string {
		return depart.Add(time.Duration(math.Round(offsetSec)) * time.Second).Format(time.RFC3339)
	}
	reach := func(soc float64) float64 {
		return round1(soc / 100 * p.Vehicle.PackKWh * p.Vehicle.MiPerKWh * metersPerMile)
	}

	type item struct {
		dist float64
		kind int // 0 via, 1 stop
		stop *plannedStop
		via  int
	}
	var items []item
	for i := range stops {
		items = append(items, item{dist: stops[i].arrivedAt, kind: 1, stop: &stops[i]})
	}
	for i := 1; i < len(in.Waypoints)-1; i++ {
		items = append(items, item{dist: pt.legEnd[i-1], kind: 0, via: i})
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].dist < items[b].dist })

	o := in.Waypoints[0]
	wps := []rivian.PlannedWaypoint{{
		WaypointType: "ORIGIN", Latitude: o.Latitude, Longitude: o.Longitude, EntityID: o.EntityID,
		DepartureSoC: round1(in.StartingSoC), DepartureReachableMeters: reach(in.StartingSoC), DepartureTimeUTC: stamp(0),
	}}
	soc, lastDist, dwell := in.StartingSoC, 0.0, 0.0
	var chargeSec float64
	for _, it := range items {
		arr := math.Max(0, soc-socPerM*(it.dist-lastDist))
		t := pt.secs(it.dist) + dwell
		switch it.kind {
		case 0:
			w := in.Waypoints[it.via]
			wps = append(wps, rivian.PlannedWaypoint{
				WaypointType: "WAYPOINT", Latitude: w.Latitude, Longitude: w.Longitude, EntityID: w.EntityID,
				ArrivalSoC: round1(arr), DepartureSoC: round1(arr),
				ArrivalReachableMeters: reach(arr), DepartureReachableMeters: reach(arr),
				ArrivalTimeUTC: stamp(t), DepartureTimeUTC: stamp(t), DistanceFromOriginMeters: math.Round(it.dist),
			})
			soc, lastDist = arr, it.dist
		case 1:
			st := it.stop
			name := st.st.poi.Name
			if name == "" {
				name = "DC fast charger"
			}
			wps = append(wps, rivian.PlannedWaypoint{
				WaypointType: "DC_CHARGE_STATION", EntityID: st.st.poi.EntityID, Name: name,
				Latitude: st.st.poi.Lat, Longitude: st.st.poi.Lon, MaxPowerKW: st.st.kw,
				ChargeDurationSec: int(st.durSec), ArrivalSoC: round1(arr), DepartureSoC: round1(st.depSoC),
				ArrivalReachableMeters: reach(arr), DepartureReachableMeters: reach(st.depSoC),
				ArrivalTimeUTC: stamp(t), DepartureTimeUTC: stamp(t + st.durSec), DistanceFromOriginMeters: math.Round(it.dist),
			})
			dwell += st.durSec
			chargeSec += st.durSec
			soc, lastDist = st.depSoC, it.dist
		}
	}
	destArr := soc - socPerM*(pt.total-lastDist)
	reached := empty == nil && destArr >= 0
	if destArr < 0 {
		destArr = 0
	}
	d := in.Waypoints[len(in.Waypoints)-1]
	wps = append(wps, rivian.PlannedWaypoint{
		WaypointType: "DESTINATION", Latitude: d.Latitude, Longitude: d.Longitude, EntityID: d.EntityID,
		ArrivalSoC: round1(destArr), ArrivalReachableMeters: reach(destArr),
		ArrivalTimeUTC: stamp(pt.secs(pt.total) + dwell), DistanceFromOriginMeters: math.Round(pt.total),
	})

	driveSec := pt.secs(pt.total)
	route := rivian.TripRoute{
		DestinationReached:       reached,
		TotalChargingDurationSec: int(chargeSec),
		TotalDriveDurationSec:    int(math.Round(driveSec)),
		TotalTripDurationSec:     int(math.Round(driveSec + chargeSec)),
		TotalDriveDistanceMeters: math.Round(pt.total),
		ArrivalSoC:               round1(destArr),
		ArrivalReachableMeters:   reach(destArr),
		EnergyConsumptionKWh:     round1(pt.total * kwhPerM),
		Waypoints:                wps,
	}
	belowLimit := false
	if in.TargetArrivalSocPercent != nil {
		belowLimit = destArr < *in.TargetArrivalSocPercent
	} else {
		belowLimit = destArr < defaultReserve
	}
	if empty != nil {
		lat, lon := pt.at(empty.dist)
		route.BatteryEmptyLat, route.BatteryEmptyLon = lat, lon
		route.BatteryEmptyToDestMeters = math.Round(pt.total - empty.dist)
		belowLimit = true
	}
	return &rivian.TripPlan{
		Status: "OK", ChargeStationsAvailable: stationsAvailable, SoCBelowLimit: belowLimit,
		Routes: []rivian.TripRoute{route}, Simulated: true,
	}
}
