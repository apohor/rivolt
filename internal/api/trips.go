package api

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
)

// Trip grouping defaults (scratchpad/trips-contract.md).
const (
	tripAwayKM      = 150.0          // a session this far from home makes a run a trip
	tripMaxGap      = 12 * time.Hour // idle gap that ends a run
	tripDefaultDays = 90             // default window when from/to are absent
	tripMaxDays     = 366            // widest window one request may ask for
	tripIDPrefix    = "trip_"
)

type tripPlan struct {
	PlanID           string    `json:"plan_id"`
	PlannedCost      float64   `json:"planned_cost"`
	PlannedArrivalAt time.Time `json:"planned_arrival_at"`
	PlannedMiles     float64   `json:"planned_miles"`
}

type tripResponseTrip struct {
	TripID             string             `json:"trip_id"`
	StartedAt          time.Time          `json:"started_at"`
	EndedAt            time.Time          `json:"ended_at"`
	Miles              float64            `json:"miles"`
	DriveIDs           []string           `json:"drive_ids"`
	ChargeIDs          []string           `json:"charge_ids"`
	FastSessions       int                `json:"fast_sessions"`
	MaxKMFromHome      *float64           `json:"max_km_from_home"`
	KWhUsed            float64            `json:"kwh_used"`
	KWhAdded           float64            `json:"kwh_added"`
	Cost               float64            `json:"cost"`
	CostSourceMix      map[string]float64 `json:"cost_source_mix"`
	EstimatedShare     float64            `json:"estimated_share"`
	UnpricedSessions   int                `json:"unpriced_sessions"`
	EfficiencyMiPerKWh *float64           `json:"efficiency_mi_per_kwh"`
	Plan               *tripPlan          `json:"plan"`
}

type tripsResponse struct {
	Version  int                `json:"version"`
	TZ       string             `json:"tz"`
	Currency string             `json:"currency"`
	Trips    []tripResponseTrip `json:"trips"`
}

type tripEvent struct {
	start, end time.Time
	drive      *ledger.Drive
	charge     *ledger.Charge
	// set for charges
	source, place, kind string
	kmHome              *float64
}

// buildTrips groups effective drives and charges into trips. Pure.
//
// Events are walked in time order and cut into runs: a run ends at a
// charge at home (the truck is back) or after an idle gap over
// tripMaxGap. A run is a trip when any session was over tripAwayKM
// from home or any charge was DC fast. Distance from home comes from
// charge positions only (drives carry no coordinates in the ledger).
func buildTrips(cs []ledger.Charge, ds []ledger.Drive, cfg ledger.Config) []tripResponseTrip {
	kc, _ := ledger.EffectiveCharges(cs)
	kd, _ := ledger.EffectiveDrives(ds)
	evs := make([]tripEvent, 0, len(kc)+len(kd))
	for i := range kd {
		evs = append(evs, tripEvent{start: kd[i].StartedAt, end: kd[i].EndedAt, drive: &kd[i]})
	}
	for i := range kc {
		c := &kc[i]
		cl := ledger.Classify(*c, cfg)
		e := tripEvent{start: c.StartedAt, end: c.EndedAt, charge: c, source: cl.CostSource, place: cl.Place, kind: cl.Kind}
		if cfg.Home != nil && c.Lat != nil && c.Lon != nil && !(*c.Lat == 0 && *c.Lon == 0) {
			km := math.Round(haversineKM(cfg.Home.Lat, cfg.Home.Lon, *c.Lat, *c.Lon)*10) / 10
			e.kmHome = &km
		}
		evs = append(evs, e)
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if !evs[i].start.Equal(evs[j].start) {
			return evs[i].start.Before(evs[j].start)
		}
		return evID(evs[i]) < evID(evs[j])
	})

	var out []tripResponseTrip
	var run []tripEvent
	flush := func() {
		if t, ok := tripFromRun(run, cfg); ok {
			out = append(out, t)
		}
		run = nil
	}
	var lastEnd time.Time
	for _, e := range evs {
		if len(run) > 0 && e.start.Sub(lastEnd) > tripMaxGap {
			flush()
		}
		if e.end.After(lastEnd) || len(run) == 0 {
			lastEnd = e.end
		}
		if e.charge != nil && e.place == "home" && e.kind != "dcfc" {
			flush() // back home: the home charge is not part of the trip
			lastEnd = e.end
			continue
		}
		run = append(run, e)
	}
	flush()
	return out
}

func evID(e tripEvent) string {
	if e.drive != nil {
		return e.drive.ID
	}
	return e.charge.ID
}

func tripFromRun(run []tripEvent, cfg ledger.Config) (tripResponseTrip, bool) {
	t := tripResponseTrip{
		DriveIDs: []string{}, ChargeIDs: []string{},
		CostSourceMix: map[string]float64{},
	}
	qualifies := false
	var maxKM *float64
	for i, e := range run {
		if i == 0 || e.start.Before(t.StartedAt) {
			t.StartedAt = e.start
		}
		if e.end.After(t.EndedAt) {
			t.EndedAt = e.end
		}
		if e.drive != nil {
			t.DriveIDs = append(t.DriveIDs, e.drive.ID)
			if e.drive.DistanceMi != nil {
				t.Miles += *e.drive.DistanceMi
			}
			if e.drive.EnergyKWh != nil {
				t.KWhUsed += *e.drive.EnergyKWh
			}
			continue
		}
		c := e.charge
		t.ChargeIDs = append(t.ChargeIDs, c.ID)
		kwh := 0.0
		if c.EnergyKWh != nil {
			kwh = *c.EnergyKWh
		}
		t.KWhAdded += kwh
		if e.kind == "dcfc" {
			t.FastSessions++
			qualifies = true
		}
		if e.kmHome != nil {
			if maxKM == nil || *e.kmHome > *maxKM {
				v := *e.kmHome
				maxKM = &v
			}
			if *e.kmHome > tripAwayKM {
				qualifies = true
			}
		}
		sameCur := c.Currency == "" || cfg.Currency == "" || strings.EqualFold(c.Currency, cfg.Currency)
		switch e.source {
		case ledger.SourceRivian, ledger.SourceHomeRate, ledger.SourceNetworkEstimate, ledger.SourceUser:
			if sameCur && c.Cost != nil {
				t.Cost += *c.Cost
				t.CostSourceMix[e.source] += *c.Cost
				continue
			}
		}
		if kwh >= ledger.CrumbKWh {
			t.UnpricedSessions++
		}
	}
	if !qualifies || len(t.DriveIDs)+len(t.ChargeIDs) == 0 {
		return tripResponseTrip{}, false
	}
	t.MaxKMFromHome = maxKM
	t.Miles = round2(t.Miles)
	t.KWhUsed, t.KWhAdded, t.Cost = round2(t.KWhUsed), round2(t.KWhAdded), round2(t.Cost)
	for k, v := range t.CostSourceMix {
		t.CostSourceMix[k] = round2(v)
	}
	if t.Cost > 0 {
		t.EstimatedShare = round2(t.CostSourceMix[ledger.SourceNetworkEstimate]/t.Cost*100) / 100
	}
	if t.KWhUsed > 0 && t.Miles > 0 {
		v := round2(t.Miles / t.KWhUsed)
		t.EfficiencyMiPerKWh = &v
	}
	first := ""
	for _, e := range run {
		if e.start.Equal(t.StartedAt) {
			first = evID(e)
			break
		}
	}
	t.TripID = tripIDPrefix + first
	return t, true
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func haversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0088
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// trips serves GET /api/trips?from&to&vehicle_id&tz. from/to are
// RFC3339 or YYYY-MM-DD (in tz); a trip is included when it starts in
// [from, to). Read-only, scoped to the session user.
func (a ledgerAPI) trips(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	p, ok := a.params(w, r, uid)
	if !ok {
		return
	}
	now := a.now()
	q := r.URL.Query()
	to, from := now, now.AddDate(0, 0, -tripDefaultDays)
	parse := func(name string, dst *time.Time) bool {
		s := strings.TrimSpace(q.Get(name))
		if s == "" {
			return true
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			*dst = t
			return true
		}
		if t, err := time.ParseInLocation("2006-01-02", s, p.loc); err == nil {
			*dst = t
			return true
		}
		http.Error(w, name+" must be RFC3339 or YYYY-MM-DD", http.StatusBadRequest)
		return false
	}
	if q.Get("from") != "" && q.Get("to") == "" {
		to = time.Time{}
	}
	if !parse("from", &from) {
		return
	}
	if to.IsZero() {
		to = from.AddDate(0, 0, tripDefaultDays)
	}
	if !parse("to", &to) {
		return
	}
	if !to.After(from) || to.Sub(from) > tripMaxDays*24*time.Hour {
		http.Error(w, "range must be positive and at most "+strconv.Itoa(tripMaxDays)+" days", http.StatusBadRequest)
		return
	}
	cfg, err := a.config(r.Context(), uid)
	if err != nil {
		http.Error(w, "trips: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Pad the load so runs that straddle the window edges are whole.
	cs, ds, err := a.load(r.Context(), uid, p.vehicleID, from.Add(-24*time.Hour), to.Add(72*time.Hour))
	if err != nil {
		http.Error(w, "trips: "+err.Error(), http.StatusInternalServerError)
		return
	}
	all := buildTrips(cs, ds, cfg)
	out := tripsResponse{Version: 1, TZ: p.loc.String(), Currency: cfg.Currency, Trips: []tripResponseTrip{}}
	for _, t := range all {
		if !t.StartedAt.Before(from) && t.StartedAt.Before(to) {
			out.Trips = append(out.Trips, t)
		}
	}
	// Newest first, like the Activity feed.
	sort.SliceStable(out.Trips, func(i, j int) bool { return out.Trips[i].StartedAt.After(out.Trips[j].StartedAt) })
	writeJSON(w, http.StatusOK, out)
}
