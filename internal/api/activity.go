package api

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/apohor/rivolt/internal/analytics"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
)

// The iPhone's Activity lists, as the web shows them.
//
// Round trips: the web merges consecutive out-and-back drives in the browser from the
// user's local preferences (lib/drives.ts collapseRoundTrips; on by default, 200 m,
// 90 min). The server has no such setting, so the same three values arrive as
// optional query parameters and default to the web's. The merge below is that web
// function ported exactly (it is not drives.CollapseRoundTrips, which anchors the
// origin differently and keeps no cost), so the two surfaces agree.

const (
	activityMaxRows        = 2000
	defaultRoundTripRadius = 200.0
	defaultRoundTripGapMin = 90.0
)

// activityDrive is one drive of GET /api/activity: no coordinates and no polyline.
type activityDrive struct {
	ID                   string    `json:"id"`
	VehicleID            string    `json:"vehicle_id"`
	StartedAt            time.Time `json:"started_at"`
	EndedAt              time.Time `json:"ended_at"`
	DistanceMi           float64   `json:"distance_mi"`
	EnergyUsedKWh        float64   `json:"energy_used_kwh"`
	StartSoCPct          float64   `json:"start_soc_pct"`
	EndSoCPct            float64   `json:"end_soc_pct"`
	AvgSpeedMph          float64   `json:"avg_speed_mph"`
	MaxSpeedMph          float64   `json:"max_speed_mph"`
	EstimatedCost        *float64  `json:"estimated_cost,omitempty"`
	EstimatedCurrency    string    `json:"estimated_currency,omitempty"`
	EstimatedPricePerKWh *float64  `json:"estimated_price_per_kwh,omitempty"`
	// Set on a row that merges several drives: how many, and the sum of their own
	// durations (the parked gaps between them are not driving).
	RoundTripLegs int     `json:"round_trip_legs,omitempty"`
	DrivingSecs   float64 `json:"driving_seconds,omitempty"`
}

// activityCharge is one charge of GET /api/activity: no coordinates, no place name.
// kind and place are the ledger's classification with the cluster label filling in
// what it cannot say (the web's chargePlaceLabel): kind "dcfc" or "ac" ("" if
// unknown), place "home" or "away" ("" if unknown).
type activityCharge struct {
	ID                string    `json:"id"`
	VehicleID         string    `json:"vehicle_id"`
	StartedAt         time.Time `json:"started_at"`
	EndedAt           time.Time `json:"ended_at"`
	EnergyAddedKWh    float64   `json:"energy_added_kwh"`
	StartSoCPct       float64   `json:"start_soc_pct"`
	EndSoCPct         float64   `json:"end_soc_pct"`
	MaxPowerKW        float64   `json:"max_power_kw"`
	Cost              float64   `json:"cost"`
	Currency          string    `json:"currency"`
	CostSourceEff     string    `json:"cost_source_effective"`
	EstimatedCost     float64   `json:"estimated_cost"`
	EstimatedCurrency string    `json:"estimated_currency"`
	Kind              string    `json:"kind"`
	Place             string    `json:"place"`
	Network           string    `json:"network"`
}

type activityResponse struct {
	Drives  []activityDrive  `json:"drives"`
	Charges []activityCharge `json:"charges"`
}

// rtDrive is a drive row being merged: the stored row plus the figures the web carries.
type rtDrive struct {
	d        drives.Drive
	cost     *float64
	cur      string
	rate     *float64
	legs     int
	drivingS float64
}

func floatPtr(v float64) *float64 { return &v }

// collapseRoundTripsLikeWeb is lib/drives.ts collapseRoundTrips: desc in, desc out.
// Merges chains of consecutive drives whose parked gaps stay within maxGap and whose
// last drive ends within radius of the origin, the origin being the previous drive's
// end (the parking spot the truck left from) when it has a fix, else the chain
// head's own start.
func collapseRoundTripsLikeWeb(ds []rtDrive, radiusM float64, maxGap time.Duration) []rtDrive {
	if len(ds) < 2 {
		return append([]rtDrive(nil), ds...)
	}
	asc := make([]rtDrive, len(ds))
	for i, d := range ds {
		asc[len(ds)-1-i] = d
	}
	var merged []rtDrive
	for i := 0; i < len(asc); {
		originLat, originLon := asc[i].d.StartLat, asc[i].d.StartLon
		if i > 0 && (asc[i-1].d.EndLat != 0 || asc[i-1].d.EndLon != 0) {
			originLat, originLon = asc[i-1].d.EndLat, asc[i-1].d.EndLon
		}
		best := i
		for j := i + 1; j < len(asc); j++ {
			gap := asc[j].d.StartedAt.Sub(asc[j-1].d.EndedAt)
			if gap < 0 || gap > maxGap {
				break
			}
			if drives.HaversineMeters(originLat, originLon, asc[j].d.EndLat, asc[j].d.EndLon) <= radiusM {
				best = j
			}
		}
		if best > i {
			merged = append(merged, mergeChainLikeWeb(asc[i:best+1]))
		} else {
			merged = append(merged, asc[i])
		}
		i = best + 1
	}
	out := make([]rtDrive, len(merged))
	for i, d := range merged {
		out[len(merged)-1-i] = d
	}
	return out
}

// mergeChainLikeWeb is lib/drives.ts mergeChain.
func mergeChainLikeWeb(chain []rtDrive) rtDrive {
	if len(chain) == 1 {
		return chain[0]
	}
	last := chain[len(chain)-1]
	var totalDist, maxSpd, wSpd, wDur, totalEnergy, totalCost float64
	allHaveEnergy, hasCost := true, true
	for _, c := range chain {
		d := c.d
		totalDist += d.DistanceMi
		if d.MaxSpeedMph > maxSpd {
			maxSpd = d.MaxSpeedMph
		}
		if dur := d.EndedAt.Sub(d.StartedAt).Seconds(); dur > 0 {
			wSpd += d.AvgSpeedMph * dur
			wDur += dur
		}
		if d.DistanceMi > 0.1 {
			socDropped := d.StartSoCPct > d.EndSoCPct
			if d.EnergyUsedKWh > 0 {
				totalEnergy += d.EnergyUsedKWh
			} else if socDropped {
				allHaveEnergy = false
			}
			if c.cost != nil {
				totalCost += *c.cost
			} else if socDropped {
				hasCost = false
			}
		}
	}
	energy := 0.0
	if allHaveEnergy {
		energy = totalEnergy
	}
	m := chain[0]
	m.d.EndedAt = last.d.EndedAt
	m.d.EndSoCPct = last.d.EndSoCPct
	m.d.EndOdometerMi = last.d.EndOdometerMi
	m.d.EndLat, m.d.EndLon = last.d.EndLat, last.d.EndLon
	m.d.DistanceMi = totalDist
	m.d.MaxSpeedMph = maxSpd
	if wDur > 0 {
		m.d.AvgSpeedMph = wSpd / wDur
	}
	m.drivingS = wDur
	m.d.EnergyUsedKWh = energy
	m.cost, m.rate = nil, nil
	if hasCost {
		m.cost = floatPtr(totalCost)
	}
	if chain[0].cur == "" {
		m.cur = last.cur
	}
	if m.cost != nil && energy > 0 {
		m.rate = floatPtr(*m.cost / energy)
	}
	m.legs = len(chain)
	return m
}

// roundTripParams reads round_trips (0 turns merging off), round_trip_radius_m and
// round_trip_gap_min, defaulting to the web's (on, 200 m, 90 min).
func roundTripParams(w http.ResponseWriter, r *http.Request) (on bool, radiusM float64, maxGap time.Duration, ok bool) {
	q := r.URL.Query()
	on, radiusM, maxGap = q.Get("round_trips") != "0", defaultRoundTripRadius, time.Duration(defaultRoundTripGapMin)*time.Minute
	if s := q.Get("round_trip_radius_m"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 1 || v > 5000 {
			http.Error(w, "round_trip_radius_m must be 1 to 5000", http.StatusBadRequest)
			return false, 0, 0, false
		}
		radiusM = v
	}
	if s := q.Get("round_trip_gap_min"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 1 || v > 1440 {
			http.Error(w, "round_trip_gap_min must be 1 to 1440", http.StatusBadRequest)
			return false, 0, 0, false
		}
		maxGap = time.Duration(v * float64(time.Minute))
	}
	return on, radiusM, maxGap, true
}

// resolvedKindPlace is the ledger's kind and place with the cluster label filling in
// what it cannot say, in the web's chargePlaceLabel order: a DC fast charge stays dcfc
// (its place is the ledger's if home or away); an AC charge's place comes from the
// ledger, else the cluster (Home, Public as away); an unclassified charge takes the
// cluster (Fast as dcfc).
func resolvedKindPlace(kind, place string, cluster analytics.ClusterLabel) (string, string) {
	if place != "home" && place != "away" {
		place = ""
	}
	switch kind {
	case "dcfc":
		return "dcfc", place
	case "ac":
		if place == "" {
			place = clusterPlace(cluster)
		}
		return "ac", place
	}
	if cluster == analytics.LabelFast {
		return "dcfc", place
	}
	if place == "" {
		place = clusterPlace(cluster)
	}
	return "", place
}

func clusterPlace(c analytics.ClusterLabel) string {
	switch c {
	case analytics.LabelHome:
		return "home"
	case analytics.LabelPublic:
		return "away"
	}
	return ""
}

// handleActivity: GET /api/activity?since&until&vehicle_id[&round_trips&round_trip_radius_m
// &round_trip_gap_min] is the iPhone's Activity tab in one compact answer: the window's
// drives (the list's rows and decoration, round trips merged as the web does) and
// charges (the list's rows and decoration, the cluster fallback resolved into kind and
// place), newest first, at most activityMaxRows of each. Window and truck as on
// chart-points. No coordinates, polylines, place names, home location or cluster geometry.
func handleActivity(ds *drives.Store, cs *charges.Store, settingsStore *settings.Store, billed func(context.Context) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		since, until, vehicleID, ok := chartWindow(w, r)
		if !ok {
			return
		}
		rtOn, radiusM, maxGap, ok := roundTripParams(w, r)
		if !ok {
			return
		}
		out := activityResponse{Drives: []activityDrive{}, Charges: []activityCharge{}}
		ctx := r.Context()
		if ds != nil {
			rows, err := ds.ListRecentForVehicle(ctx, vehicleID, 5000, false)
			if err != nil {
				http.Error(w, "could not read drives", http.StatusInternalServerError)
				return
			}
			rows = drives.FilterReparking(rows)
			cfg, _ := settings.GetChargingConfig(ctx, settingsStore)
			priced := loadPricedCharges(ctx, cs, cfg)
			fallbackRate, fallbackCur := computeBlendedRate(priced, cfg)
			var win []rtDrive
			for _, d := range rows {
				if d.StartedAt.Before(since) || !d.StartedAt.Before(until) {
					continue
				}
				rate, cur := rateForDrive(d, priced, fallbackRate, fallbackCur)
				dr := decorateDrive(d, rate, cur)
				rt := rtDrive{d: d, cur: dr.EstimatedCurrency}
				if dr.EstimatedCost != 0 {
					rt.cost = floatPtr(dr.EstimatedCost)
				}
				if dr.EstimatedPricePerKWh != 0 {
					rt.rate = floatPtr(dr.EstimatedPricePerKWh)
				}
				win = append(win, rt)
			}
			if rtOn {
				win = collapseRoundTripsLikeWeb(win, radiusM, maxGap)
			}
			for _, rt := range win {
				if len(out.Drives) >= activityMaxRows {
					break
				}
				d := rt.d
				a := activityDrive{
					ID: d.ID, VehicleID: d.VehicleID, StartedAt: d.StartedAt, EndedAt: d.EndedAt,
					DistanceMi: d.DistanceMi, EnergyUsedKWh: d.EnergyUsedKWh, StartSoCPct: d.StartSoCPct, EndSoCPct: d.EndSoCPct,
					AvgSpeedMph: d.AvgSpeedMph, MaxSpeedMph: d.MaxSpeedMph,
					EstimatedCost: rt.cost, EstimatedCurrency: rt.cur, EstimatedPricePerKWh: rt.rate,
				}
				if rt.legs > 1 {
					a.RoundTripLegs, a.DrivingSecs = rt.legs, rt.drivingS
				}
				out.Drives = append(out.Drives, a)
			}
		}
		if cs != nil {
			// Classified and clustered over the latest 5000 charges of every truck, as the
			// web does, then narrowed to the window and truck.
			rows, err := cs.ListRecent(ctx, 5000)
			if err != nil {
				http.Error(w, "could not read charges", http.StatusInternalServerError)
				return
			}
			clusters := clusterLabels(rows)
			for _, c := range decorateCharges(ctx, rows, settingsStore, billed != nil && billed(ctx)) {
				if c.StartedAt.Before(since) || !c.StartedAt.Before(until) || (vehicleID != "" && c.VehicleID != vehicleID) {
					continue
				}
				if len(out.Charges) >= activityMaxRows {
					break
				}
				var kind, place, costSrc string
				if c.Classification != nil {
					kind, place, costSrc = c.Classification.Kind, c.Classification.Place, c.Classification.CostSource
				}
				kind, place = resolvedKindPlace(kind, place, clusters[c.ID])
				out.Charges = append(out.Charges, activityCharge{
					ID: c.ID, VehicleID: c.VehicleID, StartedAt: c.StartedAt, EndedAt: c.EndedAt,
					EnergyAddedKWh: c.EnergyAddedKWh, StartSoCPct: c.StartSoCPct, EndSoCPct: c.EndSoCPct, MaxPowerKW: c.MaxPowerKW,
					Cost: c.Cost, Currency: c.Currency, CostSourceEff: costSrc,
					EstimatedCost: c.EstimatedCost, EstimatedCurrency: c.EstimatedCurrency,
					Kind: kind, Place: place, Network: c.Network,
				})
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}
