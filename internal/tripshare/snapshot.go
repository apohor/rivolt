package tripshare

import (
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/apohor/rivolt/internal/maps"
)

// Snapshot is what a shared link serves: built field by field from the saved plan (an allowlist, never the stored JSON passed through), so
// a field added to the plan later is not shared by accident. The wire contract is in _shared/share-trip-design.md.
type Snapshot struct {
	Version   int       `json:"version"`
	Title     string    `json:"title"`
	PlannedAt time.Time `json:"planned_at"`
	DepartAt  string    `json:"depart_at,omitempty"`
	TZ        string    `json:"tz,omitempty"`
	StartSoC  float64   `json:"start_soc"`
	Totals    Totals    `json:"totals"`
	Start     Area      `json:"start"`
	End       Area      `json:"end"`
	Stops     []Stop    `json:"stops"`
	Route     RouteOut  `json:"route"`
	Elevation *ElevOut  `json:"elevation_profile,omitempty"`
}

type Totals struct {
	DistanceM  int     `json:"distance_m"`
	DriveS     int     `json:"drive_s"`
	ChargeS    int     `json:"charge_s"`
	ArrivalSoC float64 `json:"arrival_soc"`
}

// Area is a place shown only as a region: coordinates rounded to areaStepDeg.
type Area struct {
	Label string  `json:"label"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
}

type Stop struct {
	Kind      string  `json:"kind"` // "charger" or "stop"
	Name      string  `json:"name,omitempty"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	AtM       *int    `json:"at_m"`
	ArriveAt  string  `json:"arrive_at,omitempty"`
	DepartAt  string  `json:"depart_at,omitempty"`
	ArriveSoC float64 `json:"arrive_soc"`
	DepartSoC float64 `json:"depart_soc"`
	ChargeS   int     `json:"charge_s,omitempty"`
	MaxKW     float64 `json:"max_kw,omitempty"`
}

type RouteOut struct {
	Segments []string `json:"segments"`
}

type ElevOut struct {
	Points      [][2]int `json:"points"`
	WaypointAtM []*int   `json:"waypoint_at_m"`
}

const (
	// areaStepDeg rounds a private place to about 5 km.
	areaStepDeg = 0.05
	// MinCutM and MaxCutM bound the random cut radius drawn per link.
	MinCutM = 3000.0
	MaxCutM = 6000.0
	// homeExtraM is added to the home radius when the home place is wider than the drawn cut.
	homeExtraM = 3000.0
	// minKeptM is the least route that must remain after the cut for a link to be worth sharing.
	minKeptM = 2000.0
	// chargerSnapM is how far a charger may sit from the kept line and still be placed on it.
	chargerSnapM = 600.0
)

var (
	// ErrUnsupported is a saved plan that is not a single-day plan with a road line (multi-day, no line).
	ErrUnsupported = errors.New("tripshare: plan cannot be shared")
	// ErrTooShort is a trip with too little route outside the private areas.
	ErrTooShort = errors.New("tripshare: trip too short to share")
)

// Input is everything Build needs; Plan is the saved plan JSON as stored.
type Input struct {
	Title     string
	Plan      json.RawMessage
	DepartAt  string
	PlannedAt time.Time
	// Home is the saved home place, when one is set (RadiusM is its radius).
	Home *Circle
	// CutM is the per-link random radius from RandomCut.
	CutM float64
}

// RandomCut draws a cut radius in [MinCutM, MaxCutM] from r, a uniform float in [0,1).
func RandomCut(r float64) float64 { return MinCutM + r*(MaxCutM-MinCutM) }

// EffectiveCut is the radius used: the drawn one, or the home radius plus homeExtraM when that is larger.
func EffectiveCut(cutM float64, home *Circle) float64 {
	if home != nil && home.RadiusM+homeExtraM > cutM {
		return home.RadiusM + homeExtraM
	}
	return cutM
}

func roundArea(v float64) float64 { return math.Round(v/areaStepDeg) * areaStepDeg }

// planRoute and planWaypoint are the fields of the stored plan that are read, by the names the plan envelope uses (rivian.TripRoute and
// rivian.PlannedWaypoint). They are declared here, so a field the snapshot does not name is never decoded, and this package stays out of
// the rivian module.
type planRoute struct {
	TotalChargingDurationSec int
	TotalDriveDurationSec    int
	TotalDriveDistanceMeters float64
	ArrivalSoC               float64
	Waypoints                []planWaypoint
	Geometry                 string
	GeometryPrecision        int
}

type planWaypoint struct {
	WaypointType      string
	Name              string
	Latitude          float64
	Longitude         float64
	MaxPowerKW        float64
	ChargeDurationSec int
	ArrivalSoC        float64
	DepartureSoC      float64
	ArrivalTimeUTC    string
	DepartureTimeUTC  string
	Source            string
}

// plan is the part of the stored plan envelope that is read.
type plan struct {
	Routes        []planRoute `json:"Routes"`
	DestinationTZ string      `json:"destination_tz"`
	Elevation     *struct {
		Points [][2]int `json:"points"`
	} `json:"elevation_profile"`
}

// Build makes the snapshot, or ErrUnsupported / ErrTooShort.
func Build(in Input) (*Snapshot, error) {
	var p plan
	if err := json.Unmarshal(in.Plan, &p); err != nil || len(p.Routes) == 0 {
		return nil, ErrUnsupported
	}
	rt := p.Routes[0]
	if rt.Geometry == "" || (rt.GeometryPrecision != 0 && rt.GeometryPrecision != 6) {
		return nil, ErrUnsupported
	}
	line := maps.DecodePolyline6(rt.Geometry)
	if len(line) < 2 {
		return nil, ErrUnsupported
	}
	r := EffectiveCut(in.CutM, in.Home)

	// The private places: the two ends, every via and overnight, and home.
	var circles []Circle
	startLat, startLon := line[0][0], line[0][1]
	endLat, endLon := line[len(line)-1][0], line[len(line)-1][1]
	var wps []planWaypoint
	for _, w := range rt.Waypoints {
		switch {
		case w.Source == "origin" || w.WaypointType == "origin":
			startLat, startLon = w.Latitude, w.Longitude
		case w.Source == "destination" || w.WaypointType == "destination":
			endLat, endLon = w.Latitude, w.Longitude
		case w.Source == "via" || w.Source == "overnight":
			circles = append(circles, Circle{w.Latitude, w.Longitude, r})
			wps = append(wps, w)
		default:
			wps = append(wps, w)
		}
	}
	circles = append(circles, Circle{startLat, startLon, r}, Circle{endLat, endLon, r})
	if in.Home != nil {
		circles = append(circles, Circle{in.Home.Lat, in.Home.Lon, r})
	}
	// The line's own ends are cut too, whatever the waypoints say.
	circles = append(circles, Circle{line[0][0], line[0][1], r}, Circle{line[len(line)-1][0], line[len(line)-1][1], r})

	segs, total := Cut(line, circles)
	kept := 0.0
	for _, s := range segs {
		kept += s.ToM - s.FromM
	}
	if kept < minKeptM {
		return nil, ErrTooShort
	}

	snap := &Snapshot{
		Version:   1,
		Title:     in.Title,
		PlannedAt: in.PlannedAt.UTC().Truncate(time.Second),
		DepartAt:  in.DepartAt,
		TZ:        p.DestinationTZ,
		Totals: Totals{
			DistanceM:  int(math.Round(rt.TotalDriveDistanceMeters/1000)) * 1000,
			DriveS:     rt.TotalDriveDurationSec,
			ChargeS:    rt.TotalChargingDurationSec,
			ArrivalSoC: rt.ArrivalSoC,
		},
		Start: Area{"Start area", roundArea(startLat), roundArea(startLon)},
		End:   Area{"Destination area", roundArea(endLat), roundArea(endLon)},
		Stops: []Stop{},
	}
	if len(rt.Waypoints) > 0 {
		snap.StartSoC = rt.Waypoints[0].DepartureSoC
	}
	for _, s := range segs {
		snap.Route.Segments = append(snap.Route.Segments, s.Encode())
	}

	var at []*int
	for _, w := range wps {
		st := Stop{
			Lat: w.Latitude, Lon: w.Longitude,
			ArriveAt: w.ArrivalTimeUTC, DepartAt: w.DepartureTimeUTC,
			ArriveSoC: w.ArrivalSoC, DepartSoC: w.DepartureSoC,
			ChargeS: w.ChargeDurationSec, MaxKW: w.MaxPowerKW,
		}
		private := w.Source == "via" || w.Source == "overnight" || inAny(circles, w.Latitude, w.Longitude)
		if private {
			// A generic stop at a rounded point: no name, no exact position.
			st.Kind, st.Name = "stop", ""
			st.Lat, st.Lon = roundArea(w.Latitude), roundArea(w.Longitude)
			st.ChargeS, st.MaxKW = 0, 0
		} else {
			st.Kind, st.Name = "charger", w.Name
			if m, ok := NearestKept(segs, w.Latitude, w.Longitude, chargerSnapM); ok {
				v := int(math.Round(m))
				st.AtM = &v
			}
		}
		at = append(at, st.AtM)
		snap.Stops = append(snap.Stops, st)
	}

	if p.Elevation != nil && len(p.Elevation.Points) >= 2 && total > 0 {
		profLen := float64(p.Elevation.Points[len(p.Elevation.Points)-1][0])
		scale := 1.0
		if profLen > 0 {
			scale = total / profLen
		}
		var pts [][2]int
		for _, pt := range p.Elevation.Points {
			if c, ok := Collapse(segs, float64(pt[0])*scale); ok {
				pts = append(pts, [2]int{int(math.Round(c)), pt[1]})
			}
		}
		if len(pts) >= 2 {
			snap.Elevation = &ElevOut{Points: pts, WaypointAtM: at}
		}
	}
	return snap, nil
}
