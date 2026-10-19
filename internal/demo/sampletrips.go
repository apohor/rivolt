package demo

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Public sample-trip pages (rivolt.dev/sample-trips, Sprint 4): three preset
// plans shown as indexable pages that end in "plan your own". Everything
// comes from the embedded presets (no database, planner or network), built
// once into SampleTrip, which the server renders as HTML for crawlers and
// embeds as JSON for the page component.

// SampleTripPage is one page: its slug, the preset it shows, and its copy.
type SampleTripPage struct {
	Slug     string
	PresetID string
	Title    string // <title> and h1
	Desc     string // meta description
	Intro    string
}

// SampleTripHub is the /sample-trips page's copy.
var SampleTripHub = SampleTripPage{
	Slug:  "",
	Title: "Sample Rivian road trips, planned with charging stops | Rivolt",
	Desc:  "Three real routes planned for a Rivian: where to charge, for how long, and what it costs. Unofficial, not affiliated with Rivian.",
	Intro: "Three road trips planned the way Rivolt plans yours: real roads, the charging stops it picks, how long each takes, and an estimated cost. Open one in the demo planner, or plan your own.",
}

// SampleTripPages lists the pages in hub order. The copy is growth's (the
// placeholders here until it lands).
var SampleTripPages = []SampleTripPage{
	{
		Slug:     "la-las-vegas-two-days",
		PresetID: "919ca13a-123f-5423-994d-12d7ccd31319",
		Title:    "LA to Las Vegas and back in a Rivian: a two-day plan | Rivolt",
		Desc:     "A two-day Pasadena to Las Vegas round trip planned for a Rivian, with a fast-charge stop each way and an overnight hotel charge. Estimates only.",
		Intro:    "A weekend in Las Vegas from Pasadena: one fast-charging stop on the way out, a slow charge at the hotel, and one short top-up on the way home.",
	},
	{
		Slug:     "pasadena-san-francisco",
		PresetID: "san-francisco",
		Title:    "Pasadena to San Francisco in a Rivian: charging plan | Rivolt",
		Desc:     "A Pasadena to San Francisco drive on I-5 planned for a Rivian: one fast-charging stop, how long it takes and an estimated cost.",
		Intro:    "Pasadena to the Ferry Building up I-5, about 380 miles. One fast-charging stop in the Central Valley gets you there with charge to spare.",
	},
	{
		Slug:     "la-mammoth-lakes",
		PresetID: "mammoth",
		Title:    "LA to Mammoth Lakes in a Rivian: charging plan | Rivolt",
		Desc:     "Union Station to Mammoth Mountain planned for a Rivian: a climb of about 8,000 ft, one fast-charging stop and an estimated cost.",
		Intro:    "From downtown Los Angeles up US-395 to Mammoth Mountain: a long climb that uses more energy than the miles suggest, with one fast-charging stop on the way.",
	},
}

// SampleTrip is the page model: the server renders it and the page component
// reads it as JSON (contract agreed with ux).
type SampleTrip struct {
	Slug      string          `json:"slug"`
	Title     string          `json:"title"`
	Intro     string          `json:"intro"`
	DemoURL   string          `json:"demo_url"`
	SignupURL string          `json:"signup_url"`
	Currency  string          `json:"currency"`
	Totals    SampleTripTotal `json:"totals"`
	Days      []SampleTripDay `json:"days"`
	// Route is the road shape, [lat, lon] pairs, for the map.
	Route [][2]float64 `json:"route"`
}

// SampleTripTotal sums the trip.
type SampleTripTotal struct {
	DistanceMi  float64 `json:"distance_mi"`
	DriveMin    float64 `json:"drive_min"`
	ChargeMin   float64 `json:"charge_min"`
	ChargeStops int     `json:"charge_stops"`
	DCFCCostEst float64 `json:"dcfc_cost_est"`
}

// SampleTripDay is one driving day.
type SampleTripDay struct {
	Label      string               `json:"label"`
	DepartAt   time.Time            `json:"depart_at"`
	ArriveAt   time.Time            `json:"arrive_at"`
	From       string               `json:"from"`
	To         string               `json:"to"`
	DistanceMi float64              `json:"distance_mi"`
	DriveMin   float64              `json:"drive_min"`
	ArrivalSoC float64              `json:"arrival_soc"`
	Stops      []SampleTripStop     `json:"stops"`
	Overnight  *SampleTripOvernight `json:"overnight"`
}

// SampleTripStop is a charging stop. CostEst is the energy at the network's
// guest rate (no membership), an estimate.
type SampleTripStop struct {
	Name      string  `json:"name"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Network   string  `json:"network"`
	ArriveSoC float64 `json:"arrive_soc"`
	DepartSoC float64 `json:"depart_soc"`
	ChargeMin float64 `json:"charge_min"`
	MaxKW     float64 `json:"max_kw"`
	CostEst   float64 `json:"cost_est"`
}

// SampleTripOvernight is a slow charge where the trip stops for the night.
type SampleTripOvernight struct {
	Name        string  `json:"name"`
	ParkedHours float64 `json:"parked_hours"`
	L2KW        float64 `json:"l2_kw"`
	AddedKWh    float64 `json:"added_kwh"`
	SoCAfter    float64 `json:"soc_after"`
}

const metersPerMile = 1609.344

var (
	sampleTripsOnce sync.Once
	sampleTrips     map[string]SampleTrip
	sampleTripsErr  error
)

// SampleTripBySlug returns a page's model, built once from the presets.
func SampleTripBySlug(slug string) (SampleTrip, bool) {
	sampleTripsOnce.Do(buildSampleTrips)
	t, ok := sampleTrips[slug]
	return t, ok
}

// SampleTripsErr reports a preset the pages couldn't be built from (tests,
// startup log).
func SampleTripsErr() error {
	sampleTripsOnce.Do(buildSampleTrips)
	return sampleTripsErr
}

func buildSampleTrips() {
	sampleTrips = map[string]SampleTrip{}
	var index PresetIndex
	if err := json.Unmarshal(PresetsIndex().Body, &index); err != nil {
		sampleTripsErr = err
		return
	}
	polylines := map[string]string{}
	for _, p := range index.Trips {
		polylines[p.ID] = p.RoutePolyline
	}
	for _, page := range SampleTripPages {
		t, err := buildSampleTrip(page, polylines[page.PresetID])
		if err != nil {
			sampleTripsErr = fmt.Errorf("sample trip %s: %w", page.Slug, err)
			continue
		}
		sampleTrips[page.Slug] = t
	}
}

// The plan files' fields the pages use.
type planWaypoint struct {
	WaypointType      string
	Name              string
	ArrivalSoC        float64
	DepartureSoC      float64
	ChargeDurationSec float64
	MaxPowerKW        float64
	Latitude          float64
	Longitude         float64
	ArrivalTimeUTC    string
	DepartureTimeUTC  string
}

type planRoute struct {
	TotalDriveDistanceMeters float64
	TotalDriveDurationSec    float64
	TotalChargingDurationSec float64
	ArrivalSoC               float64
	Waypoints                []planWaypoint
	RoutePolyline            string `json:"route_polyline"`
}

type planCost struct {
	Currency  string  `json:"currency"`
	DCFCSpend float64 `json:"dcfc_spend"`
	Breakdown []struct {
		NetworkName string  `json:"network_name"`
		ChargerName string  `json:"charger_name"`
		EnergyKWh   float64 `json:"energy_kwh"`
		GuestRate   float64 `json:"guest_rate"`
	} `json:"breakdown"`
}

type singlePlan struct {
	Routes []planRoute
	Costs  []planCost `json:"costs"`
}

type multiPlan struct {
	Kind string `json:"kind"`
	Days []struct {
		Overnight *struct {
			Name        string  `json:"name"`
			ParkedHours float64 `json:"parked_hours"`
			L2KW        float64 `json:"l2_kw"`
			AddedKWh    float64 `json:"added_kwh"`
			PostSoC     float64 `json:"post_charge_soc_pct"`
		} `json:"overnight"`
		Costs []planCost `json:"costs"`
		Plan  singlePlan `json:"plan"`
	} `json:"days"`
}

func buildSampleTrip(page SampleTripPage, indexPolyline string) (SampleTrip, error) {
	blob, ok := PresetPlan(page.PresetID)
	if !ok {
		return SampleTrip{}, fmt.Errorf("no preset %q", page.PresetID)
	}
	t := SampleTrip{
		Slug: page.Slug, Title: page.Title, Intro: page.Intro,
		DemoURL:   "https://" + "demo.rivolt.dev" + "/trips/plan?preset=" + page.PresetID,
		SignupURL: "/signup",
		Currency:  "USD",
	}
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(blob.Body, &probe); err != nil {
		return SampleTrip{}, err
	}
	if probe.Kind == "multiday" {
		var m multiPlan
		if err := json.Unmarshal(blob.Body, &m); err != nil {
			return SampleTrip{}, err
		}
		for i, d := range m.Days {
			day, route, err := buildDay(d.Plan, d.Costs, fmt.Sprintf("Day %d", i+1))
			if err != nil {
				return SampleTrip{}, err
			}
			if d.Overnight != nil {
				day.Overnight = &SampleTripOvernight{Name: d.Overnight.Name, ParkedHours: d.Overnight.ParkedHours,
					L2KW: d.Overnight.L2KW, AddedKWh: d.Overnight.AddedKWh, SoCAfter: d.Overnight.PostSoC}
			}
			t.Days = append(t.Days, day)
			pts, err := decodeRoute6(route.RoutePolyline)
			if err != nil {
				return SampleTrip{}, err
			}
			t.Route = append(t.Route, pts...)
			if c := firstCost(d.Costs); c != nil {
				t.Currency = c.Currency
			}
		}
	} else {
		var p singlePlan
		if err := json.Unmarshal(blob.Body, &p); err != nil {
			return SampleTrip{}, err
		}
		day, _, err := buildDay(p, p.Costs, "")
		if err != nil {
			return SampleTrip{}, err
		}
		t.Days = []SampleTripDay{day}
		pts, err := decodeRoute6(indexPolyline)
		if err != nil {
			return SampleTrip{}, err
		}
		t.Route = pts
		if c := firstCost(p.Costs); c != nil {
			t.Currency = c.Currency
		}
	}
	if len(t.Route) < 2 {
		return SampleTrip{}, fmt.Errorf("no route shape")
	}
	for _, d := range t.Days {
		t.Totals.DistanceMi += d.DistanceMi
		t.Totals.DriveMin += d.DriveMin
		for _, s := range d.Stops {
			t.Totals.ChargeMin += s.ChargeMin
			t.Totals.ChargeStops++
			t.Totals.DCFCCostEst += s.CostEst
		}
	}
	t.Totals.DistanceMi = round1(t.Totals.DistanceMi)
	t.Totals.DriveMin = round1(t.Totals.DriveMin)
	t.Totals.ChargeMin = round1(t.Totals.ChargeMin)
	t.Totals.DCFCCostEst = round2(t.Totals.DCFCCostEst)
	return t, nil
}

func firstCost(c []planCost) *planCost {
	if len(c) == 0 {
		return nil
	}
	return &c[0]
}

func buildDay(p singlePlan, costs []planCost, label string) (SampleTripDay, planRoute, error) {
	if len(p.Routes) == 0 || len(p.Routes[0].Waypoints) < 2 {
		return SampleTripDay{}, planRoute{}, fmt.Errorf("plan has no route")
	}
	r := p.Routes[0]
	wps := r.Waypoints
	day := SampleTripDay{
		Label:      label,
		From:       wps[0].Name,
		To:         wps[len(wps)-1].Name,
		DistanceMi: round1(r.TotalDriveDistanceMeters / metersPerMile),
		DriveMin:   round1(r.TotalDriveDurationSec / 60),
		ArrivalSoC: round1(r.ArrivalSoC),
		Stops:      []SampleTripStop{},
	}
	day.DepartAt, _ = time.Parse(time.RFC3339, wps[0].DepartureTimeUTC)
	day.ArriveAt, _ = time.Parse(time.RFC3339, wps[len(wps)-1].ArrivalTimeUTC)
	byCharger := map[string]struct {
		network string
		cost    float64
	}{}
	if c := firstCost(costs); c != nil {
		for _, b := range c.Breakdown {
			byCharger[b.ChargerName] = struct {
				network string
				cost    float64
			}{b.NetworkName, b.EnergyKWh * b.GuestRate}
		}
	}
	for _, w := range wps[1 : len(wps)-1] {
		if w.ChargeDurationSec <= 0 {
			continue
		}
		info := byCharger[w.Name]
		day.Stops = append(day.Stops, SampleTripStop{
			Name: w.Name, Lat: w.Latitude, Lon: w.Longitude, Network: info.network,
			ArriveSoC: round1(w.ArrivalSoC), DepartSoC: round1(w.DepartureSoC),
			ChargeMin: round1(w.ChargeDurationSec / 60), MaxKW: w.MaxPowerKW,
			CostEst: round2(info.cost),
		})
	}
	return day, r, nil
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

// decodePolyline6 decodes a Google encoded polyline of precision 6 into
// [lat, lon] pairs.
func decodeRoute6(s string) ([][2]float64, error) {
	var out [][2]float64
	var lat, lon int64
	for i := 0; i < len(s); {
		var vals [2]int64
		for k := 0; k < 2; k++ {
			var result int64
			shift := uint(0)
			for {
				if i >= len(s) {
					return nil, fmt.Errorf("polyline truncated")
				}
				b := int64(s[i]) - 63
				i++
				result |= (b & 0x1f) << shift
				shift += 5
				if b < 0x20 {
					break
				}
			}
			if result&1 != 0 {
				vals[k] = ^(result >> 1)
			} else {
				vals[k] = result >> 1
			}
		}
		lat += vals[0]
		lon += vals[1]
		out = append(out, [2]float64{float64(lat) / 1e6, float64(lon) / 1e6})
	}
	return out, nil
}
