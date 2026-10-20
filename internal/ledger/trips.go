package ledger

import (
	"math"
	"sort"
	"time"
)

// Trips (Activity auto-Trips; trips-contract v1).
//
// A trip is a road trip: consecutive drives and charges from leaving
// home to getting back that went more than TripMinKMFromHome from home
// AND stopped at a DC fast charger (ux-slice-spec §4.3). A fast charge
// near home is just a fast charge, not a trip. When distance from home
// can't be measured (no saved home, no positions), a DC fast charge
// alone qualifies. Money uses the ledger's own per-session
// classification, so a trip's cost always matches the Money tab.

// TripMinKMFromHome is how far from home an outing must go to count as
// a trip (when the home location is known).
const TripMinKMFromHome = 150.0

// TripMaxParked is the longest a car can sit parked at home (or where
// we can't tell) inside one outing; a longer park ends it. Away from
// home a multi-night stay stays one trip (arriving home ends it), up to
// TripMaxParkedAway as a safety valve.
const TripMaxParked = 12 * time.Hour

// TripMaxParkedAway caps a park away from home inside one trip.
const TripMaxParkedAway = 14 * 24 * time.Hour

// TripsResponse is the GET /api/trips body.
type TripsResponse struct {
	Version  int    `json:"version"`
	TZ       string `json:"tz"`
	Currency string `json:"currency"`
	Trips    []Trip `json:"trips"`
}

// Trip is one auto-detected trip.
type Trip struct {
	TripID           string             `json:"trip_id"`
	VehicleID        string             `json:"vehicle_id"`
	StartedAt        time.Time          `json:"started_at"`
	EndedAt          time.Time          `json:"ended_at"`
	Miles            float64            `json:"miles"`
	DriveIDs         []string           `json:"drive_ids"`
	ChargeIDs        []string           `json:"charge_ids"`
	FastSessions     int                `json:"fast_sessions"`
	MaxKMFromHome    *float64           `json:"max_km_from_home"`
	KWhUsed          float64            `json:"kwh_used"`
	KWhAdded         float64            `json:"kwh_added"`
	Cost             float64            `json:"cost"`
	CostSourceMix    map[string]float64 `json:"cost_source_mix"`
	EstimatedShare   *float64           `json:"estimated_share"`
	UnpricedSessions int                `json:"unpriced_sessions"`
	EfficiencyMiKWh  *float64           `json:"efficiency_mi_per_kwh"`
	Plan             *TripPlan          `json:"plan"`
}

// TripPlan links a saved planned trip. Always null in v1.
type TripPlan struct {
	PlanID           string    `json:"plan_id"`
	PlannedCost      float64   `json:"planned_cost"`
	PlannedArrivalAt time.Time `json:"planned_arrival_at"`
	PlannedMiles     float64   `json:"planned_miles"`
}

// tripEvent is one drive or charge on a vehicle's timeline.
type tripEvent struct {
	start, end time.Time
	drive      *Drive
	charge     *Charge
}

// BuildTrips detects trips among the effective (deduped) drives and
// charges in in, returning those that start in [from, to), newest
// first. in should be loaded over a window padded beyond [from, to)
// so a trip straddling the edges is seen whole.
func BuildTrips(in Input, from, to time.Time, loc *time.Location) TripsResponse {
	if loc == nil {
		loc = time.UTC
	}
	out := TripsResponse{Version: Version, TZ: loc.String(), Currency: in.Config.Currency, Trips: []Trip{}}
	for _, outing := range outingsOf(in) {
		t, ok := tripFrom(outing, in.Config)
		if !ok || t.StartedAt.Before(from) || !t.StartedAt.Before(to) {
			continue
		}
		out.Trips = append(out.Trips, t)
	}
	sort.SliceStable(out.Trips, func(i, j int) bool { return out.Trips[i].StartedAt.After(out.Trips[j].StartedAt) })
	return out
}

// outingsOf splits the effective (deduped) drives and charges in `in`
// into per-vehicle outings (splitOutings), vehicles in id order, each
// outing in time order. Trips are the outings that qualify (tripFrom);
// plan matching considers them all.
func outingsOf(in Input) [][]tripEvent {
	charges, _ := EffectiveCharges(in.Charges)
	drives, _ := EffectiveDrives(in.Drives)
	byVehicle := map[string][]tripEvent{}
	for i := range drives {
		d := &drives[i]
		byVehicle[d.VehicleID] = append(byVehicle[d.VehicleID], tripEvent{start: d.StartedAt, end: d.EndedAt, drive: d})
	}
	for i := range charges {
		c := &charges[i]
		byVehicle[c.VehicleID] = append(byVehicle[c.VehicleID], tripEvent{start: c.StartedAt, end: c.EndedAt, charge: c})
	}
	vehicles := make([]string, 0, len(byVehicle))
	for v := range byVehicle {
		vehicles = append(vehicles, v)
	}
	sort.Strings(vehicles)
	var out [][]tripEvent
	for _, v := range vehicles {
		evs := byVehicle[v]
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].start.Before(evs[j].start) })
		out = append(out, splitOutings(evs, in.Config.Home)...)
	}
	return out
}

// splitOutings cuts a vehicle's time-ordered events into outings. An
// outing ends after a drive that arrives home, or before an event that
// follows a park longer than TripMaxParked. Events at home (charging in
// the garage) between outings form their own home-only outings, which
// never qualify as trips.
func splitOutings(evs []tripEvent, home *LatLon) [][]tripEvent {
	var out [][]tripEvent
	var cur []tripEvent
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
	}
	for _, e := range evs {
		if len(cur) > 0 {
			gap := e.start.Sub(cur[len(cur)-1].end)
			limit := TripMaxParked
			if lastAway(cur, home) {
				limit = TripMaxParkedAway
			}
			if gap > limit {
				flush()
			}
		}
		// Leaving home starts a new outing.
		if e.drive != nil && home != nil && isHomePoint(e.drive.StartLat, e.drive.StartLon, home) && len(cur) > 0 {
			flush()
		}
		cur = append(cur, e)
		// Arriving home ends it.
		if e.drive != nil && home != nil && isHomePoint(e.drive.EndLat, e.drive.EndLon, home) {
			flush()
		}
	}
	flush()
	return out
}

// lastAway reports whether the outing's last known position is known
// and away from home.
func lastAway(evs []tripEvent, home *LatLon) bool {
	for i := len(evs) - 1; i >= 0; i-- {
		var lat, lon *float64
		switch {
		case evs[i].drive != nil:
			lat, lon = evs[i].drive.EndLat, evs[i].drive.EndLon
			if lat == nil {
				lat, lon = evs[i].drive.StartLat, evs[i].drive.StartLon
			}
		case evs[i].charge != nil:
			lat, lon = evs[i].charge.Lat, evs[i].charge.Lon
		}
		if km, ok := kmFrom(lat, lon, home); ok {
			return km > HomeRadiusKM
		}
	}
	return false
}

func isHomePoint(lat, lon *float64, home *LatLon) bool {
	km, ok := kmFrom(lat, lon, home)
	return ok && km <= HomeRadiusKM
}

func kmFrom(lat, lon *float64, home *LatLon) (float64, bool) {
	if home == nil || lat == nil || lon == nil || (*lat == 0 && *lon == 0) {
		return 0, false
	}
	return haversineKM(home.Lat, home.Lon, *lat, *lon), true
}

// tripFrom summarises an outing and reports whether it's a trip.
func tripFrom(evs []tripEvent, cfg Config) (Trip, bool) {
	t := Trip{
		DriveIDs:      []string{},
		ChargeIDs:     []string{},
		CostSourceMix: map[string]float64{SourceRivian: 0, SourceHomeRate: 0, SourceNetworkEstimate: 0, SourceObservedEstimate: 0, SourceUser: 0},
	}
	var maxKM float64
	knownKM := false
	note := func(lat, lon *float64) {
		if km, ok := kmFrom(lat, lon, cfg.Home); ok {
			knownKM = true
			if km > maxKM {
				maxKM = km
			}
		}
	}
	var milesWithEnergy float64
	for i, e := range evs {
		if i == 0 {
			t.StartedAt = e.start.UTC()
		}
		if e.end.After(t.EndedAt) {
			t.EndedAt = e.end.UTC()
		}
		switch {
		case e.drive != nil:
			d := e.drive
			t.VehicleID = d.VehicleID
			t.DriveIDs = append(t.DriveIDs, d.ID)
			mi := val(d.DistanceMi)
			t.Miles += mi
			if positive(d.EnergyKWh) {
				t.KWhUsed += *d.EnergyKWh
				milesWithEnergy += mi
			}
			note(d.StartLat, d.StartLon)
			note(d.EndLat, d.EndLon)
		case e.charge != nil:
			c := e.charge
			t.VehicleID = c.VehicleID
			t.ChargeIDs = append(t.ChargeIDs, c.ID)
			v := viewOf(*c, cfg)
			if v.kind == "dcfc" {
				t.FastSessions++
			}
			t.KWhAdded += val(c.EnergyKWh)
			switch {
			case v.spendOK:
				t.Cost += *c.Cost
				t.CostSourceMix[v.source] += *c.Cost
			case hasEnergy(*c) && !hasFlag(v.flags, FlagCurrencyMismatch):
				t.UnpricedSessions++ // unknown or estimate pending
			}
			note(c.Lat, c.Lon)
		}
	}
	if knownKM {
		km := math.Round(maxKM)
		t.MaxKMFromHome = &km
	}
	far := knownKM && maxKM > TripMinKMFromHome
	if len(t.DriveIDs) == 0 || t.FastSessions == 0 || (knownKM && !far) {
		return Trip{}, false
	}
	t.TripID = "trip_" + t.DriveIDs[0]
	t.Miles = round4(t.Miles)
	t.KWhUsed = round4(t.KWhUsed)
	t.KWhAdded = round4(t.KWhAdded)
	t.Cost = round4(t.Cost)
	for k, v := range t.CostSourceMix {
		t.CostSourceMix[k] = round4(v)
	}
	if t.Cost > 0 {
		share := round4((t.CostSourceMix[SourceNetworkEstimate] + t.CostSourceMix[SourceObservedEstimate]) / t.Cost)
		t.EstimatedShare = &share
	}
	if t.KWhUsed > 0 && milesWithEnergy > 0 {
		eff := round4(milesWithEnergy / t.KWhUsed)
		t.EfficiencyMiKWh = &eff
	}
	return t, true
}
