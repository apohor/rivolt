package rivian

import (
	"math"
	"time"
)

// The sample owner's saved trip plans (demo parity): the plans they made
// ahead of each leg of the Las Vegas trip, so the demo's Plan vs Actual
// pairs a saved plan with the outing the replay actually drove. The
// plans are built from the corpus's own legs (same route, stops and
// times), with the differences a real plan has from the drive: it
// expects less energy and no traffic, charges only to 80 %, and leaves
// a little earlier than the driver did.

// The saved plans' names.
const (
	samplePlanVegas     = "Las Vegas weekend"
	samplePlanVegasHome = "Las Vegas back home"
)

type samplePlace struct {
	at    samplePoint
	label string
}

var (
	samplePlaceHome  = samplePlace{SampleHome, "Rose Bowl parking (sample)"}
	samplePlaceVegas = samplePlace{sampleVegas, "Las Vegas, NV"}
)

// samplePlanned is one leg the owner planned ahead.
type samplePlanned struct {
	name         string
	trip         int        // index into SampleCorpus.trips
	leg          sampleSpan // the leg's segments
	origin, dest samplePlace
}

// How a plan differs from the drive it was made for.
const (
	samplePlanEnergyFactor = 0.93             // the plan expects 7 % less energy than the drive used
	samplePlanDriveFactor  = 0.96             // and no traffic
	samplePlanChargeTo     = 80.0             // it charges to 80 %; the driver went on to 85
	samplePlanLeadTime     = 10 * time.Minute // its departure, before the drive's (then down to 5 min)
	samplePlanSavedBefore  = 48 * time.Hour   // saved this long before its departure
	samplePlanTargetSoC    = 10.0             // the arrival SoC the owner asked for
	samplePlanDCFCTaper    = 0.62             // average ÷ peak kW over a fast charge, as charge() uses
)

// SamplePlace is a named point of a sample plan.
type SamplePlace struct {
	Lat, Lon float64
	Label    string
}

// SampleTripPlan is one of the sample owner's saved plans, placed on
// the wall clock for one loop of the replay.
type SampleTripPlan struct {
	Name                string
	Origin, Destination SamplePlace
	DepartAt            time.Time // planned departure
	SavedAt             time.Time // when the owner saved it
	StartSoC            float64
	TargetSoC           float64
	PackKWh             float64 // the sample truck's, for pricing the plan
	// OutingStart and OutingEnd bound the whole road trip (home to
	// home) the plan's leg belongs to, on this loop.
	OutingStart, OutingEnd time.Time
	Plan                   TripPlan
}

// SampleTripPlanNames are the names of the sample owner's saved plans.
func SampleTripPlanNames() []string {
	c, _ := sampleShared()
	out := make([]string, len(c.planned))
	for i, p := range c.planned {
		out[i] = p.name
	}
	return out
}

// SampleTripPlans returns the sample owner's saved plans for the latest
// sample road trip that is over by now (its outing back home at or
// before now) and started at or after since, or nil when there is none.
// A trip's plans stay on that loop until the next loop's trip is over,
// so a plan never points at an outing still under way; since keeps it
// off a trip recorded only in part.
func SampleTripPlans(now, since time.Time) []SampleTripPlan {
	c, tl := sampleShared()
	return c.tripPlans(tl, now, since)
}

func (c *SampleCorpus) tripPlans(tl replayTimeline, now, since time.Time) []SampleTripPlan {
	var out []SampleTripPlan
	for _, p := range c.planned {
		if shift, outA, outB, ok := c.legLoop(tl, p, now, since); ok {
			out = append(out, c.planLeg(p, shift, outA, outB, 0))
		}
	}
	return out
}

// legLoop places p's road trip on the latest loop of the replay where it
// is over by now and started at or after since: the shift onto the wall
// clock and the outing's bounds there. False when there is none.
func (c *SampleCorpus) legLoop(tl replayTimeline, p samplePlanned, now, since time.Time) (time.Duration, time.Time, time.Time, bool) {
	trip := c.trips[p.trip]
	start, end := c.segs[trip.from].start, c.segs[trip.to-1].end
	shift := func(k int64) time.Duration { return tl.loopStart(k).Sub(tl.start) }
	k := tl.loopAt(now)
	for k >= 0 && end.Add(shift(k)).After(now) {
		k--
	}
	if k < 0 || start.Add(shift(k)).Before(since) {
		return 0, time.Time{}, time.Time{}, false
	}
	return shift(k), start.Add(shift(k)), end.Add(shift(k)), true
}

// The sample owner's two-day plan for the Las Vegas trip: the way out as
// day 1, a night at the hotel on its L2, and the way home as day 2.
const (
	SamplePlanVegasTwoDay = "Las Vegas, 2 days"
	// SampleHotelLabel names the overnight stop; SampleHotelKW is its L2.
	SampleHotelLabel = "Las Vegas hotel (sample)"
	SampleHotelKW    = sampleHotelKW
	// SampleOvernightMaxSoC caps the overnight charge, as the owner's
	// charge limit did at the hotel.
	SampleOvernightMaxSoC = 90.0
)

// SampleTwoDayPlan returns the two days of the sample owner's two-day
// Las Vegas plan, placed on the same loop as SampleTripPlans' (nil when
// there is none). Day 1 is the way-out plan. Day 2 is the way home as a
// multi-day plan chains it: it leaves with the charge the hotel's L2 puts
// in overnight (SampleOvernightMaxSoC; the stay allows far more), not with
// what the truck had left after a day of driving around town.
func SampleTwoDayPlan(now, since time.Time) []SampleTripPlan {
	c, tl := sampleShared()
	var out []SampleTripPlan
	for _, p := range c.planned {
		var startSoC float64
		switch p.name {
		case samplePlanVegas:
		case samplePlanVegasHome:
			startSoC = SampleOvernightMaxSoC
		default:
			continue
		}
		shift, outA, outB, ok := c.legLoop(tl, p, now, since)
		if !ok {
			return nil
		}
		out = append(out, c.planLeg(p, shift, outA, outB, startSoC))
	}
	if len(out) != 2 {
		return nil
	}
	return out
}

// planLeg builds the plan for p's leg, shifted onto the wall clock. A
// positive startSoC replaces the SoC the truck actually left with.
func (c *SampleCorpus) planLeg(p samplePlanned, shift time.Duration, outA, outB time.Time, startSoC float64) SampleTripPlan {
	segs := c.segs[p.leg.from:p.leg.to]
	depart := segs[0].start.Add(shift).Add(-samplePlanLeadTime).Truncate(5 * time.Minute)
	reach := func(soc float64) float64 { return round1(soc / 100 * samplePackKWh * sampleMiPerKWh * 1609.344) }
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	secs := func(s float64) time.Duration { return time.Duration(math.Round(s)) * time.Second }

	soc, at := segs[0].socA, depart
	if startSoC > 0 {
		soc = startSoC
	}
	first := soc
	var meters, driveSec, chargeSec, kwh float64
	wps := []PlannedWaypoint{{WaypointType: "ORIGIN", Name: p.origin.label,
		Latitude: p.origin.at.Lat, Longitude: p.origin.at.Lon,
		DepartureSoC: round1(soc), DepartureReachableMeters: reach(soc), DepartureTimeUTC: stamp(at)}}
	for _, s := range segs {
		switch {
		case s.kind == segDrive:
			used := (s.socA - s.socB) * samplePlanEnergyFactor
			soc -= used
			kwh += used / 100 * samplePackKWh
			d := math.Round(s.end.Sub(s.start).Seconds() * samplePlanDriveFactor)
			driveSec += d
			at = at.Add(secs(d))
			meters += (s.odoB - s.odoA) * 1609.344
		case s.kind == segCharge && s.dcfc:
			to := math.Max(samplePlanChargeTo, soc)
			d := math.Round((to - soc) / 100 * samplePackKWh / (s.peakKW * samplePlanDCFCTaper) * 3600)
			wp := PlannedWaypoint{WaypointType: "DC_CHARGE_STATION", Name: sampleStationName(s.path[0]),
				Latitude: s.path[0].Lat, Longitude: s.path[0].Lon, MaxPowerKW: s.peakKW,
				ChargeDurationSec: int(d), ArrivalSoC: round1(soc), DepartureSoC: round1(to),
				ArrivalReachableMeters: reach(soc), DepartureReachableMeters: reach(to),
				ArrivalTimeUTC: stamp(at), DistanceFromOriginMeters: math.Round(meters)}
			at = at.Add(secs(d))
			wp.DepartureTimeUTC = stamp(at)
			wps = append(wps, wp)
			chargeSec += d
			soc = to
		}
	}
	wps = append(wps, PlannedWaypoint{WaypointType: "DESTINATION", Name: p.dest.label,
		Latitude: p.dest.at.Lat, Longitude: p.dest.at.Lon, ArrivalSoC: round1(soc),
		ArrivalReachableMeters: reach(soc), ArrivalTimeUTC: stamp(at), DistanceFromOriginMeters: math.Round(meters)})
	route := TripRoute{DestinationReached: true,
		TotalChargingDurationSec: int(chargeSec), TotalDriveDurationSec: int(driveSec),
		TotalTripDurationSec: int(chargeSec + driveSec), TotalDriveDistanceMeters: math.Round(meters),
		ArrivalSoC: round1(soc), ArrivalReachableMeters: reach(soc), EnergyConsumptionKWh: round1(kwh),
		Waypoints: wps}
	return SampleTripPlan{
		Name:        p.name,
		Origin:      SamplePlace{p.origin.at.Lat, p.origin.at.Lon, p.origin.label},
		Destination: SamplePlace{p.dest.at.Lat, p.dest.at.Lon, p.dest.label},
		DepartAt:    depart, SavedAt: depart.Add(-samplePlanSavedBefore),
		StartSoC: round1(first), TargetSoC: samplePlanTargetSoC, PackKWh: samplePackKWh,
		OutingStart: outA, OutingEnd: outB,
		Plan: TripPlan{Status: "OK", ChargeStationsAvailable: true, Routes: []TripRoute{route}},
	}
}

// sampleStationName is the trip-plan name of the sample DC fast
// station at p.
func sampleStationName(p samplePoint) string {
	for _, st := range sampleDCFCStations {
		if st.at == p {
			return st.planned
		}
	}
	return ""
}
