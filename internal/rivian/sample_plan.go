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
// before now), or nil before the first one ends. A trip's plans stay
// on that loop until the next loop's trip is over, so a plan never
// points at an outing still under way.
func SampleTripPlans(now time.Time) []SampleTripPlan {
	c, tl := sampleShared()
	return c.tripPlans(tl, now)
}

func (c *SampleCorpus) tripPlans(tl replayTimeline, now time.Time) []SampleTripPlan {
	var out []SampleTripPlan
	for _, p := range c.planned {
		trip := c.trips[p.trip]
		start, end := c.segs[trip.from].start, c.segs[trip.to-1].end
		shift := func(k int64) time.Duration { return tl.loopStart(k).Sub(tl.start) }
		k := tl.loopAt(now)
		for k >= 0 && end.Add(shift(k)).After(now) {
			k--
		}
		if k < 0 {
			continue
		}
		out = append(out, c.planLeg(p, shift(k), start.Add(shift(k)), end.Add(shift(k))))
	}
	return out
}

// planLeg builds the plan for p's leg, shifted onto the wall clock.
func (c *SampleCorpus) planLeg(p samplePlanned, shift time.Duration, outA, outB time.Time) SampleTripPlan {
	segs := c.segs[p.leg.from:p.leg.to]
	depart := segs[0].start.Add(shift).Add(-samplePlanLeadTime).Truncate(5 * time.Minute)
	reach := func(soc float64) float64 { return round1(soc / 100 * samplePackKWh * sampleMiPerKWh * 1609.344) }
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	secs := func(s float64) time.Duration { return time.Duration(math.Round(s)) * time.Second }

	soc, at := segs[0].socA, depart
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
		StartSoC: round1(segs[0].socA), TargetSoC: samplePlanTargetSoC, PackKWh: samplePackKWh,
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
