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
	corridorKm         = 12.0 // a hotel counts as near the route within this
	dcfcNearKm         = 8.0  // "within about 10 minutes" of a morning top-up
	assumedL2KW        = 7.0  // when the listing gives no power
	// Planned power at a campground hookup, through Rivian's Portable Charger
	// (32 A, 7.6 kW at most, per Rivian's page): a NEMA 14-50 site is planned at
	// 7.2 kW (a little under, for campground voltage); a 30 A TT-30 or a 15-20 A
	// outlet is 120 V, which the charger draws 12 A from (1.4 kW).
	nema1450KW     = 7.2
	slow120KW      = 1.4
	assumedSpeedMS = 25.0 // 90 km/h, when the day's drive time isn't given
	maxCandidates  = 3
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
	// Kinds are the stays to suggest: KindHotel, KindCampground. Empty means
	// hotels only, as before.
	Kinds []string
	// FindLodging, when set, lets the search fall back to hotels and motels with no charger listed (from OpenStreetMap), each paired
	// with the nearest fast charger, when no stay with a listed charger is in range. nil: no fallback.
	FindLodging LodgingFinder
}

// Lodging is a place to sleep from outside the charger listing.
type Lodging struct {
	Name     string
	Lat, Lon float64
	// ID is its OpenStreetMap element, "osm:node:123".
	ID       string
	Locality string
}

// LodgingFinder returns the hotels and motels within radiusKm of a point. A failure means none are known near it.
type LodgingFinder func(lat, lon, radiusKm float64) ([]Lodging, error)

// Kinds of stay a candidate can be.
const (
	KindHotel      = "hotel"
	KindCampground = "campground"
)

// Connectors a candidate is reached through.
const (
	ConnJ1772    = "j1772"
	ConnNEMA1450 = "nema_14_50"
	ConnTT30     = "nema_tt_30"
	ConnNEMA520  = "nema_5_20"
)

// The credit CC BY 4.0 needs for the Recreation.gov data: the name, a link to the
// licence and to the source, and a note that we changed it (we keep a few facts
// per campground and plan the power ourselves). A page shows Attribution with
// "Recreation.gov (RIDB)" linked to AttributionSourceURL and "CC BY 4.0" linked
// to AttributionLicenseURL.
const (
	Attribution           = "Data: Recreation.gov (RIDB), CC BY 4.0; filtered by Rivolt"
	AttributionSourceURL  = "https://catalog.data.gov/dataset/recreation-information-database-ridb"
	AttributionLicenseURL = "https://creativecommons.org/licenses/by/4.0/"
)

// recreationCampgroundURL is the only place a candidate links out to.
const recreationCampgroundURL = "https://www.recreation.gov/camping/campgrounds/"

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
	// Kind is KindHotel or KindCampground.
	Kind string `json:"kind"`
	// Connector, NeedsAdapter, ElectricSites, Sites, ReservationRequired, Fee,
	// Source, SourceURL, AsOf and Attribution are set for campgrounds. A 30 A
	// TT-30 site needs an adapter Rivian's Portable Charger does not ship.
	// ReservationRequired is null when the listing does not say.
	Connector           string `json:"connector,omitempty"`
	NeedsAdapter        bool   `json:"needs_adapter,omitempty"`
	ElectricSites       int    `json:"electric_sites,omitempty"`
	Sites               int    `json:"sites,omitempty"`
	ReservationRequired *bool  `json:"reservation_required,omitempty"`
	Fee                 string `json:"fee,omitempty"`
	Source              string `json:"source,omitempty"`
	SourceURL           string `json:"source_url,omitempty"`
	AsOf                string `json:"asof,omitempty"`
	Attribution         string `json:"attribution,omitempty"`
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
	// WindowBeforeMin and WindowAfterMin are how many minutes of driving before and after the budget point the
	// search covered: the window that produced the stays, or the widest one tried when there are none.
	WindowBeforeMin float64 `json:"window_before_min"`
	WindowAfterMin  float64 `json:"window_after_min"`
}

// Fast charger pairing and the OpenStreetMap credit for the fallback stays.
const (
	fastNearKm            = 30.0 // a fallback stay needs a fast charger this near, or it is not offered
	fallbackRadiusKm      = 25.0 // how far from a sample point the lodging search looks
	fallbackStepM         = 40000.0
	fallbackMaxLookups    = 6
	OSMAttribution        = "© OpenStreetMap contributors"
	OSMAttributionURL     = "https://www.openstreetmap.org/copyright"
	OSMAttributionLicense = "https://opendatacommons.org/licenses/odbl/"
)

// FastCharger is the DC fast charger a fallback stay is paired with.
type FastCharger struct {
	Name       string  `json:"name,omitempty"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	EntityID   string  `json:"entity_id,omitempty"`
	DistanceM  float64 `json:"distance_m"`
	MaxPowerKW float64 `json:"max_power_kw,omitempty"`
}

// FallbackStay is a hotel or motel with no charger listed, offered only when no stay with a listed charger is in range. The truck
// does not charge overnight there: it arrives, sleeps and leaves on the same charge, and the next day starts at FastCharger.
type FallbackStay struct {
	Name     string  `json:"name"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	ID       string  `json:"id,omitempty"`
	Kind     string  `json:"kind"`
	Locality string  `json:"locality,omitempty"`
	// NoOnsiteCharger is always true: no charger is listed at this place.
	NoOnsiteCharger bool `json:"no_onsite_charger"`
	// FastCharger is the nearest DC fast charger in the listing (the first stop of the next day).
	FastCharger FastCharger `json:"fast_charger"`
	DetourM     float64     `json:"detour_m"`
	AlongM      float64     `json:"along_m"`
	// Source and Attribution credit OpenStreetMap, whose data this is.
	Source      string `json:"source"`
	Attribution string `json:"attribution"`
}

// NearestStay is a listed stay found outside the searched windows.
type NearestStay struct {
	Candidate
	// DriveSec is the driving time from the start to the stay.
	DriveSec float64 `json:"drive_sec"`
	// OffsetSec is the driving time from the budget point, negative for a stay before it (a shorter day) and positive after it.
	OffsetSec float64 `json:"offset_sec"`
}

// Nearest is the closest listed stay on each side of the budget point, anywhere on the route. It is set only when the windows
// found no stay, so a screen can say "nearest listed stay: 160 mi back, about 2 h short of the day" instead of "none".
type Nearest struct {
	Before *NearestStay `json:"before,omitempty"`
	After  *NearestStay `json:"after,omitempty"`
}

// Result is the suggestion.
type Result struct {
	Candidates []Candidate `json:"candidates"`
	Target     Target      `json:"target"`
	RouteM     float64     `json:"route_m"`
	// Nearest is set when Candidates is empty and the day does not fit in the budget; nil when no listed stay is on the route.
	Nearest *Nearest `json:"nearest,omitempty"`
	// FallbackStays are hotels and motels with no listed charger, each paired with the nearest fast charger, offered only when
	// Candidates is empty and the request allowed the lookup. Never ranked above a stay with a listed charger: there are none then.
	FallbackStays []FallbackStay `json:"fallback_stays,omitempty"`
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
	tlat, tlon := pth.at(target)
	res := &Result{Candidates: []Candidate{}, RouteM: math.Round(pth.total), Target: Target{Lat: tlat, Lon: tlon, AlongM: math.Round(target), ReachesEnd: reachesEnd}}

	kinds, err := wantedKinds(req.Kinds)
	if err != nil {
		return nil, err
	}
	for i, w := range searchWindows {
		lo := math.Max(0, target-w.beforeMin*60/secPerM)
		hi := math.Min(pth.total, target+w.afterMin*60/secPerM)
		res.Target.WindowBeforeMin, res.Target.WindowAfterMin = w.beforeMin, w.afterMin
		found, err := search(a, pth, kinds, lo, hi, target, i > 0)
		if err != nil {
			return nil, err
		}
		if len(found) > 0 {
			res.Candidates = found
			break
		}
	}
	if len(res.Candidates) == 0 && !reachesEnd {
		n, err := nearest(a, pth, kinds, target, secPerM)
		if err != nil {
			return nil, err
		}
		res.Nearest = n
		if req.FindLodging != nil {
			res.FallbackStays = fallbackStays(a, pth, req.FindLodging, target, secPerM)
		}
	}
	return res, nil
}

// fallbackStays looks for hotels and motels with no listed charger near where the budget runs out, within the same reach as the
// stays that have one (up to 3 h before the budget point and 1 h after), nearest the budget point first, each with a fast charger
// within fastNearKm. It samples the route outward from the budget point, at most fallbackMaxLookups lookups, and stops at three.
func fallbackStays(a Archive, pth *path, find LodgingFinder, target, secPerM float64) []FallbackStay {
	last := searchWindows[len(searchWindows)-1]
	lo := math.Max(0, target-last.beforeMin*60/secPerM)
	hi := math.Min(pth.total, target+last.afterMin*60/secPerM)
	points := []float64{target}
	for k := 1; len(points) < fallbackMaxLookups; k++ {
		before, after := target-float64(k)*fallbackStepM, target+float64(k)*fallbackStepM
		if before < lo && after > hi {
			break
		}
		if before >= lo {
			points = append(points, before)
		}
		if after <= hi && len(points) < fallbackMaxLookups {
			points = append(points, after)
		}
	}
	type found struct {
		l          Lodging
		along, off float64
	}
	var all []found
	seen := map[string]bool{}
	for _, at := range points {
		lat, lon := pth.at(at)
		lodgings, err := find(lat, lon, fallbackRadiusKm)
		if err != nil {
			continue
		}
		for _, l := range lodgings {
			key := l.ID
			if key == "" {
				key = fmt.Sprintf("%s@%.4f,%.4f", l.Name, l.Lat, l.Lon)
			}
			if seen[key] || l.Name == "" || math.IsNaN(l.Lat) || math.IsNaN(l.Lon) {
				continue
			}
			seen[key] = true
			along, off := pth.project(l.Lat, l.Lon)
			if off > corridorKm*1000 || along < lo || along > hi {
				continue
			}
			all = append(all, found{l, along, off})
		}
		if len(all) >= 3*maxCandidates {
			break
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		di, dj := math.Abs(all[i].along-target), math.Abs(all[j].along-target)
		if di != dj {
			return di < dj
		}
		if all[i].along != all[j].along {
			return all[i].along < all[j].along
		}
		return all[i].l.ID+all[i].l.Name < all[j].l.ID+all[j].l.Name
	})
	var out []FallbackStay
	for _, f := range all {
		fc := nearestFastCharger(a, f.l.Lat, f.l.Lon, fastNearKm)
		if fc == nil {
			continue // no fast charger near it: the next morning would have nowhere to start
		}
		out = append(out, FallbackStay{
			Name: f.l.Name, Lat: f.l.Lat, Lon: f.l.Lon, ID: f.l.ID, Kind: KindHotel, Locality: f.l.Locality,
			NoOnsiteCharger: true, FastCharger: *fc, DetourM: math.Round(2 * f.off), AlongM: math.Round(f.along),
			Source: "OpenStreetMap", Attribution: OSMAttribution,
		})
		if len(out) == maxCandidates {
			break
		}
	}
	return out
}

// nearestFastCharger is the closest DC fast charger (50 kW or more) within maxKm of a point, or nil.
func nearestFastCharger(a Archive, lat, lon, maxKm float64) *FastCharger {
	pois, err := a.QueryCorridor(chargers.PointQueryPath(lat, lon, maxKm), chargers.FilterDCFC, chargers.QueryCorridorOptions{
		CorridorKm: maxKm, EndpointTrimMeter: 1, IncludeEndpoints: true, MinPowerKW: 50,
	})
	if err != nil {
		return nil
	}
	var best *FastCharger
	for _, p := range pois {
		d := haversine([2]float64{lat, lon}, [2]float64{p.Lat, p.Lon})
		if d > maxKm*1000 {
			continue
		}
		if best == nil || d < best.DistanceM {
			best = &FastCharger{Name: p.Name, Lat: p.Lat, Lon: p.Lon, EntityID: p.EntityID, DistanceM: math.Round(d), MaxPowerKW: p.MaxPowerKW}
		}
	}
	return best
}

// nearest finds the closest listed stay before and after the budget point anywhere on the route (nil when there is none), for the
// answer when the windows found nothing. Stays after the point are reported as they are: this is information for the person
// deciding, and OffsetSec says how far past the budget they are.
func nearest(a Archive, pth *path, kinds []string, target, secPerM float64) (*Nearest, error) {
	all, err := collect(a, pth, kinds, 0, pth.total)
	if err != nil {
		return nil, err
	}
	var before, after *scored
	for i := range all {
		c := &all[i]
		if c.c.AlongM < target {
			if before == nil || c.c.AlongM > before.c.AlongM {
				before = c
			}
		} else if after == nil || c.c.AlongM < after.c.AlongM {
			after = c
		}
	}
	if before == nil && after == nil {
		return nil, nil
	}
	mk := func(s *scored) *NearestStay {
		if s == nil {
			return nil
		}
		s.c.DCFCNearby = nearbyDCFC(a, s.c)
		return &NearestStay{Candidate: s.c, DriveSec: math.Round(s.c.AlongM * secPerM), OffsetSec: math.Round((s.c.AlongM - target) * secPerM)}
	}
	return &Nearest{Before: mk(before), After: mk(after)}, nil
}

// searchWindows are the half-widths, in minutes of driving, tried in turn: the day's budget point plus or minus the
// first, then wider. A search widens only when the one before found no stay. The "after" side stops at an hour (a
// nine-hour day at most): stopping early is always safe, driving an eleventh hour to bed is not a thing to suggest.
var searchWindows = []struct{ beforeMin, afterMin float64 }{{30, 30}, {60, 60}, {120, 60}, {180, 60}}

// search finds the ranked stays between lo and hi metres along the route. In a widened window (byDist) the stays
// nearest the budget point come first, an earlier stop winning a tie; in the first window they are ranked on the
// listing as always.
func search(a Archive, pth *path, kinds []string, lo, hi, target float64, byDist bool) ([]Candidate, error) {
	less := rankBefore
	if byDist {
		less = func(x, y scored) bool {
			dx, dy := math.Abs(x.c.AlongM-target), math.Abs(y.c.AlongM-target)
			if dx != dy {
				return dx < dy
			}
			if x.c.AlongM != y.c.AlongM {
				return x.c.AlongM < y.c.AlongM
			}
			return x.c.EntityID+x.c.Name < y.c.EntityID+y.c.Name
		}
	}
	all, err := collect(a, pth, kinds, lo, hi)
	if err != nil {
		return nil, err
	}
	// Rank on the listing alone first, then look for a morning fast charger
	// for the few that are in the running.
	for i := range all {
		all[i].score = score(all[i].c, false)
	}
	sort.SliceStable(all, func(i, j int) bool { return less(all[i], all[j]) })
	wantCamp := false
	for _, k := range kinds {
		wantCamp = wantCamp || k == KindCampground
	}
	if len(all) > 10 {
		// The best campground stays in the running even when ten hotels score
		// higher: it is kept a slot below.
		pool := all[:10:10]
		if wantCamp && indexOfKind(pool, KindCampground) < 0 {
			if i := indexOfKind(all[10:], KindCampground); i >= 0 {
				pool = append(pool, all[10+i])
			}
		}
		all = pool
	}
	for i := range all {
		all[i].c.DCFCNearby = nearbyDCFC(a, all[i].c)
		all[i].score = score(all[i].c, all[i].c.DCFCNearby != nil)
	}
	sort.SliceStable(all, func(i, j int) bool { return less(all[i], all[j]) })
	top := all
	if len(top) > maxCandidates {
		top = top[:maxCandidates]
		// When campgrounds are asked for, one of the three goes to the best
		// campground in reach (the others stay the best-ranked); none in reach
		// leaves the hotels alone.
		if wantCamp && indexOfKind(top, KindCampground) < 0 {
			if i := indexOfKind(all, KindCampground); i >= 0 {
				top = append(top[:maxCandidates-1:maxCandidates-1], all[i])
			}
		}
	}
	out := make([]Candidate, 0, len(top))
	for _, sc := range top {
		out = append(out, sc.c)
	}
	return out, nil
}

// collect is every stay of the wanted kinds within the corridor of the route between lo and hi metres along it, unranked.
func collect(a Archive, pth *path, kinds []string, lo, hi float64) ([]scored, error) {
	sub := pth.slice(lo, hi)
	if len(sub) < 2 {
		return nil, nil
	}
	var all []scored
	for _, kind := range kinds {
		filter := chargers.FilterHotels
		if kind == KindCampground {
			filter = chargers.FilterCamp
		}
		pois, err := a.QueryCorridor(sub, filter, chargers.QueryCorridorOptions{
			CorridorKm: corridorKm, EndpointTrimMeter: 1, IncludeEndpoints: true,
		})
		if err != nil {
			return nil, err
		}
		for _, p := range pois {
			along, off := pth.project(p.Lat, p.Lon)
			if along < lo || along > hi {
				continue
			}
			var c Candidate
			if kind == KindCampground {
				cc, ok := campCandidate(p)
				if !ok {
					continue
				}
				c = cc
			} else {
				if !p.IsL2 {
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
				c = Candidate{
					Name: p.Name, Lat: p.Lat, Lon: p.Lon, EntityID: p.EntityID, FacilityType: p.FacilityType,
					Kind: KindHotel, L2KW: round1(kw), L2KWAssumed: assumed, L2Count: p.L2Count,
				}
			}
			c.DetourM, c.AlongM = math.Round(2*off), math.Round(along)
			all = append(all, scored{c: c})
		}
	}
	return all, nil
}

// indexOfKind is the first stay of the given kind, or -1.
func indexOfKind(list []scored, kind string) int {
	for i, sc := range list {
		if sc.c.Kind == kind {
			return i
		}
	}
	return -1
}

// wantedKinds is the stays to look for: hotels when none is asked for.
func wantedKinds(in []string) ([]string, error) {
	if len(in) == 0 {
		return []string{KindHotel}, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range in {
		if k != KindHotel && k != KindCampground {
			return nil, fmt.Errorf("%w: unknown kind %q", ErrBadRequest, k)
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// campCandidate turns a campground listing into a candidate, with the power
// the stay is planned at. A J1772 Level 2 stall is a stall; otherwise the
// hookup decides: a 50 A site (or a listed NEMA 14-50) is 240 V and takes
// Rivian's Portable Charger at nema1450KW; a 30 A site is a 120 V TT-30 that
// needs an adapter; 15-20 A is 120 V. Slow 120 V sites (under 30 A, or no
// hookup and no stall) are not offered. ok is false for a listing that is
// not a way to charge overnight.
func campCandidate(p chargers.POI) (Candidate, bool) {
	c := Candidate{
		Name: p.Name, Lat: p.Lat, Lon: p.Lon, EntityID: p.EntityID, FacilityType: p.FacilityType,
		Kind: KindCampground, ElectricSites: p.ElectricSites, Sites: p.Sites, L2Count: p.L2Count,
		Source: campSource(p), AsOf: p.AsOf, Fee: p.EVPricing,
	}
	if p.RIDBID != "" {
		b := p.Reservable
		c.ReservationRequired = &b
		if id, ok := ridbFacilityID(p.RIDBID); ok {
			c.SourceURL = recreationCampgroundURL + id
			c.Attribution = Attribution
		}
	}
	switch {
	case p.SocketJ1772 && p.IsL2:
		c.Connector = ConnJ1772
		c.L2KW, c.L2KWAssumed = assumedL2KW, true
		if p.MaxPowerKW > 0 && p.MaxPowerKW <= maxScoredL2KW {
			c.L2KW, c.L2KWAssumed = p.MaxPowerKW, false
		}
	case p.HookupAmps >= 50 || p.SocketNEMA1450:
		c.Connector, c.L2KW, c.L2KWAssumed = ConnNEMA1450, nema1450KW, true
	case p.HookupAmps >= 30:
		c.Connector, c.L2KW, c.L2KWAssumed, c.NeedsAdapter = ConnTT30, slow120KW, true, true
	case p.L2Count > 0 && p.HookupAmps == 0:
		// A listed Level 2 stall with no connector flag: an ordinary stall.
		c.L2KW, c.L2KWAssumed = assumedL2KW, true
	default:
		return Candidate{}, false
	}
	c.L2KW = round1(c.L2KW)
	return c, true
}

func campSource(p chargers.POI) string {
	if p.RIDBID != "" {
		return "Recreation.gov"
	}
	return ""
}

// ridbFacilityID checks a Recreation.gov FacilityID is numeric, so it can only
// ever build a plain campground link.
func ridbFacilityID(id string) (string, bool) {
	if id == "" || len(id) > 12 {
		return "", false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return id, true
}

// score ranks a stay: its Level 2 power matters most, a long detour costs,
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
	pois, err := a.QueryCorridor(chargers.PointQueryPath(c.Lat, c.Lon, dcfcNearKm), chargers.FilterDCFC, chargers.QueryCorridorOptions{
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
