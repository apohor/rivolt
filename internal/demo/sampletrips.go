package demo

import (
	"encoding/json"
	"fmt"
	"strings"
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
	Blurb    string // the hub card's one line
	// Zone is the IANA zone the trip's clock times are shown in (where it happens); empty is Pacific, the original three.
	Zone string
}

// SampleTripHub is the /sample-trips page's copy (growth, DA-approved).
var SampleTripHub = SampleTripPage{
	Title: "Sample Rivian road trips, planned with Rivolt",
	Desc:  "Eight sample trips for a Rivian R1S planned by Rivolt: charging stops, arrival charge, time and estimated cost. Simulated sample data, no sign-in needed.",
	Intro: "These are eight simulated sample plans for a Rivian R1S, shown exactly as Rivolt lays them out: every charging stop with its network, the battery on arrival and departure, charge time, and the estimated cost. " +
		"The figures are estimates from the sample truck's efficiency, the route and the chargers along it; a real trip will differ with weather, traffic and charger speed. " +
		"With your own Rivian connected, the plan comes from Rivian's planner plus Rivolt's adjustment for the forecast weather along the route. " +
		"Pick a trip to see the day-by-day plan, open it in the demo planner, or plan your own.",
}

// SampleTripPages lists the pages in hub order, with growth's copy
// (sample-trips-copy.md, DA-approved; figures from the fixtures).
var SampleTripPages = []SampleTripPage{
	{
		Slug:     "la-las-vegas-two-days",
		PresetID: "919ca13a-123f-5423-994d-12d7ccd31319",
		Title:    "LA to Las Vegas and back in a Rivian: a two-day sample plan",
		Desc:     "A two-day Rivian R1S trip from Los Angeles to Las Vegas and back: a fast-charge stop at Barstow, a hotel night on Level 2, and the estimated cost per day.",
		Intro: "Two driving days and two nights in Las Vegas, 530 miles in all, from Pasadena and home again. " +
			"Day one stops once, at the Rivian Adventure Network site in Barstow, and ends at a Las Vegas hotel with a Level 2 charger; over the stay, charging at 6 kW adds about 79 kWh and stops at 90%. " +
			"The drive home needs only a short top-up at Primm. " +
			"Times, charge levels and costs are estimates for the sample truck, and the hotel is a sample stop with a Level 2 charger, not a real listing; on a real trip, check a hotel's charger before you rely on it.",
		Blurb: "Los Angeles to Las Vegas and back in two days: a hotel overnight on Level 2, one fast-charge stop each way.",
	},
	{
		Slug:     "pasadena-san-francisco",
		PresetID: "san-francisco",
		Title:    "Pasadena to San Francisco in a Rivian: I-5 with one stop",
		Desc:     "A 381-mile Rivian R1S trip up I-5 from Pasadena to the Ferry Building, planned by Rivolt: one fast-charge stop at Harris Ranch and an estimated cost.",
		Intro: "The classic I-5 run: 381 miles from the Rose Bowl to the Ferry Building in about 5 h 40 min of driving, about 6 h 15 min with the stop. " +
			"Leaving at 90%, the plan makes one fast-charge stop at Harris Ranch, arriving at 18% and charging to 80% in about 36 minutes using a NACS adapter, then reaches San Francisco with 16% left. " +
			"Every figure is the planner's estimate for the sample truck; your truck, the weather and the charger on the day will change them.",
		Blurb: "Pasadena to San Francisco on I-5: 381 miles with one fast-charge stop at Harris Ranch.",
	},
	{
		Slug:     "la-mammoth-lakes",
		PresetID: "mammoth",
		Title:    "LA to Mammoth Lakes in a Rivian: one stop to the mountains",
		Desc:     "A 314-mile Rivian R1S sample trip from Los Angeles to Mammoth Mountain Main Lodge, planned by Rivolt: one fast-charge stop and an estimated arrival charge.",
		Intro: "From Union Station to Mammoth Mountain Main Lodge at about 9,000 feet: 314 miles. " +
			"The plan stops once on the way, at the TA Travel Center fast chargers, charging from 22% to 78% in about 33 minutes with a NACS adapter, and arrives with 16%. " +
			"This sample plan does not model the climb; when you plan a real trip with your Rivian connected, the route comes from Rivian's planner and Rivolt adjusts for the forecast weather along the route, which matters on a winter run to the mountains. " +
			"All figures are estimates for the sample truck.",
		Blurb: "Los Angeles to Mammoth Lakes: 314 miles to the mountains with one fast-charge stop.",
	},
	{
		Slug:     "seattle-portland",
		PresetID: "seattle-portland",
		Zone:     "America/Los_Angeles",
		Title:    "Seattle to Portland in a Rivian: a sample plan, no stops",
		Desc:     "A 182-mile Rivian R1S sample trip down I-5 from Seattle to Portland, planned by Rivolt: no charging stop needed, arrival charge and estimated cost.",
		Intro:    "The I-5 run most Northwest owners know: 182 miles from downtown Seattle to downtown Portland in about 2 h 54 min. Leaving at 90%, the sample plan needs no charging stop and arrives with about 26%. Every figure is an estimate for the sample truck; traffic, weather and your own driving will change them.",
		Blurb:    "Seattle to Portland: 182 miles, no stops.",
	},
	{
		Slug:     "denver-moab",
		PresetID: "denver-moab",
		Zone:     "America/Denver",
		Title:    "Denver to Moab in a Rivian: a sample plan over the Rockies",
		Desc:     "A 355-mile Rivian R1S sample trip from Denver to Moab on I-70, planned by Rivolt: one fast-charge stop at Rifle, arrival charge and estimated cost.",
		Intro:    "Denver to Moab over I-70 and down US-191: 355 miles with real mountain passes on the way, about 5 h 50 min including charging. The sample plan stops once, at the Rifle fast chargers at mile 182, charging from 16% to 75% in about 34 minutes with a NACS adapter, and arrives with about 16%. The sample plan does not model the climb; with your own Rivian connected, the route comes from Rivian's planner and Rivolt adjusts for the forecast weather along the route. All figures are estimates for the sample truck.",
		Blurb:    "Denver to Moab: 355 miles, one stop at Rifle.",
	},
	{
		Slug:     "sf-tahoe",
		PresetID: "sf-tahoe",
		Zone:     "America/Los_Angeles",
		Title:    "San Francisco to Lake Tahoe in a Rivian: a sample plan",
		Desc:     "A 190-mile Rivian R1S sample trip from San Francisco to South Lake Tahoe, planned by Rivolt: a quick top-up in Folsom, arrival charge and estimated cost.",
		Intro:    "The Bay to the lake: 190 miles up I-80 and US-50 to South Lake Tahoe, about 3 h 19 min. Leaving at 90% would arrive just under the 10% target, so the sample plan takes a quick top-up in Folsom at mile 110, 52% to 58% in about 4 minutes with a NACS adapter, and arrives with about 15%. Elevation is not modelled in this sample plan, so a winter run will differ; every figure is an estimate for the sample truck.",
		Blurb:    "San Francisco to Lake Tahoe: 190 miles, a quick top-up in Folsom.",
	},
	{
		Slug:     "la-phoenix",
		PresetID: "la-phoenix",
		Zone:     "America/Phoenix",
		Title:    "Los Angeles to Phoenix in a Rivian: a sample plan on I-10",
		Desc:     "A 374-mile Rivian R1S sample trip from Los Angeles to Phoenix on I-10, planned by Rivolt: two charging stops, arrival charge and estimated cost.",
		Intro:    "374 miles of I-10 across the desert, the drive Southern California owners make for a weekend in Arizona, about 6 h 20 min including charging. Leaving at 90% the sample plan stops twice: the Indio fast chargers at mile 126 (45% to 90%, about 26 minutes, NACS adapter) and a slower 62 kW charger at a dealership in Buckeye at mile 342 (12% to 26%, about 28 minutes), arriving with about 15%. That second stop is what the simple planner picks, not a recommendation. Summer heat lowers range; the sample plan does not model it, and with your own Rivian connected Rivolt adjusts for the forecast weather along the route. Every figure is an estimate for the sample truck.",
		Blurb:    "Los Angeles to Phoenix: 374 miles, two stops.",
	},
	{
		Slug:     "nyc-boston",
		PresetID: "nyc-boston",
		Zone:     "America/New_York",
		Title:    "New York to Boston in a Rivian: a sample plan on I-95",
		Desc:     "A 212-mile Rivian R1S sample trip from Manhattan to Boston on I-95, planned by Rivolt: no charging stop, arrival charge to spare, estimated cost.",
		Intro:    "Manhattan to Boston, 212 miles of I-95 and the turnpikes in about 3 h 42 min, with tolls you can choose to avoid in the planner. Leaving at 90% the sample plan gets there without stopping, with a little to spare: about 12% on arrival against a 10% target. Every figure is an estimate for the sample truck.",
		Blurb:    "New York to Boston: 212 miles, no stops, a little to spare.",
	},
}

// SampleTrip is the page model: the server renders it and the page component
// reads it as JSON (contract agreed with ux).
type SampleTrip struct {
	Slug string `json:"slug"`
	// TZ is the IANA zone of the trip's clock times (the page names none; the server's table prints them in it).
	TZ        string          `json:"tz"`
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
	// DCFCCostLow and DCFCCostHigh are the fast-charging cost over the stops that have
	// a price (an estimate: the third-party typical price, see dcfcrates.ObservedFor),
	// PricedStops of ChargeStops. The other stops' price is shown at the charger.
	DCFCCostLow   float64 `json:"dcfc_cost_low"`
	DCFCCostHigh  float64 `json:"dcfc_cost_high"`
	PricedStops   int     `json:"priced_stops"`
	UnpricedStops int     `json:"unpriced_stops"`
	// PriceMonth is the observation month ("2026-10") of the oldest figure behind
	// the estimate, so every wording of it can name the month.
	PriceMonth string `json:"price_month,omitempty"`
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

// SampleTripStop is a charging stop. CostLow and CostHigh are the energy at the
// network's typical observed price (an estimate, a range where the tracker reports a
// membership and a non-member price); nil when the network has no figure and the
// price is shown at the charger. PriceNote says where the figure comes from.
type SampleTripStop struct {
	Name      string   `json:"name"`
	Lat       float64  `json:"lat"`
	Lon       float64  `json:"lon"`
	Network   string   `json:"network"`
	ArriveSoC float64  `json:"arrive_soc"`
	DepartSoC float64  `json:"depart_soc"`
	ChargeMin float64  `json:"charge_min"`
	MaxKW     float64  `json:"max_kw"`
	CostLow   *float64 `json:"cost_low"`
	CostHigh  *float64 `json:"cost_high"`
	PriceNote string   `json:"price_note,omitempty"`
	// PriceMonth is the observation month of the figure behind CostLow/CostHigh.
	PriceMonth string `json:"price_month,omitempty"`
	// PriceURL is the tracker report the figure was copied from (https on
	// dcfctracker.com only); empty when there is no figure.
	PriceURL string `json:"price_url,omitempty"`
}

// SampleTripOvernight is a slow charge where the trip stops for the night.
type SampleTripOvernight struct {
	Name string `json:"name"`
	// Nights is how many nights the stop spans (local dates between arriving
	// and leaving): the Las Vegas preset arrives Saturday and leaves Monday.
	Nights      int     `json:"nights"`
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
	Currency  string `json:"currency"`
	Breakdown []struct {
		NetworkName string   `json:"network_name"`
		ChargerName string   `json:"charger_name"`
		EnergyKWh   float64  `json:"energy_kwh"`
		CostLow     *float64 `json:"cost_low"`
		CostHigh    *float64 `json:"cost_high"`
		Price       *struct {
			Label         string `json:"label"`
			ObservedMonth string `json:"observed_month"`
			SourceURL     string `json:"source_url"`
		} `json:"price"`
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
		Slug: page.Slug, Title: page.Title, Intro: page.Intro, TZ: pageZone(page),
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
			if day.Overnight != nil && i+1 < len(m.Days) {
				if next, ok := firstDepart(m.Days[i+1].Plan); ok {
					day.Overnight.Nights = nightsBetween(day.ArriveAt, next, ZoneOf(t))
				}
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
			if s.CostLow != nil && s.CostHigh != nil {
				t.Totals.PricedStops++
				t.Totals.DCFCCostLow += *s.CostLow
				t.Totals.DCFCCostHigh += *s.CostHigh
				// The oldest observation month names the estimate (months sort as text).
				if s.PriceMonth != "" && (t.Totals.PriceMonth == "" || s.PriceMonth < t.Totals.PriceMonth) {
					t.Totals.PriceMonth = s.PriceMonth
				}
			} else {
				t.Totals.UnpricedStops++
			}
		}
	}
	t.Totals.DistanceMi = round1(t.Totals.DistanceMi)
	t.Totals.DriveMin = round1(t.Totals.DriveMin)
	t.Totals.ChargeMin = round1(t.Totals.ChargeMin)
	t.Totals.DCFCCostLow, t.Totals.DCFCCostHigh = round2(t.Totals.DCFCCostLow), round2(t.Totals.DCFCCostHigh)
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
	type stopInfo struct {
		network   string
		low, high *float64
		note      string
		month     string
		url       string
	}
	byCharger := map[string]stopInfo{}
	if c := firstCost(costs); c != nil {
		for _, b := range c.Breakdown {
			si := stopInfo{network: b.NetworkName, low: b.CostLow, high: b.CostHigh}
			if b.Price != nil {
				si.note, si.month = b.Price.Label, b.Price.ObservedMonth
				if strings.HasPrefix(b.Price.SourceURL, "https://dcfctracker.com/") {
					si.url = b.Price.SourceURL
				}
			}
			byCharger[b.ChargerName] = si
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
			CostLow: roundPtr(info.low), CostHigh: roundPtr(info.high), PriceNote: info.note, PriceMonth: info.month, PriceURL: info.url,
		})
	}
	return day, r, nil
}

// roundPtr rounds a cost to cents, keeping "no price" as nil.
func roundPtr(f *float64) *float64 {
	if f == nil {
		return nil
	}
	v := round2(*f)
	return &v
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

// tripZone is the zone of the original three (Pacific); each page names its own (SampleTripPage.Zone).
var tripZone = func() *time.Location {
	if l, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return l
	}
	return time.FixedZone("PT", -8*3600)
}()

// TripZone is the zone the original sample trips' times are shown in.
func TripZone() *time.Location { return tripZone }

// pageZone is the zone a page names, Pacific when it names none.
func pageZone(p SampleTripPage) string {
	if p.Zone != "" {
		return p.Zone
	}
	return "America/Los_Angeles"
}

// ZoneOf is the zone a trip's times are shown in: its own, else Pacific.
func ZoneOf(t SampleTrip) *time.Location {
	if t.TZ != "" {
		if l, err := time.LoadLocation(t.TZ); err == nil {
			return l
		}
	}
	return tripZone
}

func firstDepart(p singlePlan) (time.Time, bool) {
	if len(p.Routes) == 0 || len(p.Routes[0].Waypoints) == 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, p.Routes[0].Waypoints[0].DepartureTimeUTC)
	return t, err == nil
}

// nightsBetween counts the local dates passed from arriving to leaving.
func nightsBetween(arrive, leave time.Time, zone *time.Location) int {
	a, l := arrive.In(zone), leave.In(zone)
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	dl := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	return int(dl.Sub(da).Hours() / 24)
}
