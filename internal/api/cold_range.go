package api

import (
	"database/sql"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/insights"
	"github.com/apohor/rivolt/internal/rivian"
)

// GET /api/vehicles/{vehicleID}/cold-range?temp_c=-6.7&grid=-17.8,-6.7,4.4&units=imperial|metric:
// what a full charge has gone, on this truck's own drives, at about the asked
// outside temperature. It is a range (the middle half of the drives' efficiency
// times the usable pack), never a point and never a comparison with anyone.
//
// The temperature of a drive is the start-hour weather already stored for it in
// drive_weather (the truck's own outside-temperature reading is 0 on every row,
// #617). Nothing is fetched here: a drive without a stored temperature does not
// count. Temperatures in and out are °C; the UI converts.
//
// Rule: drives within coldWindowC of the asked temperature, widened once to
// coldWideWindowC when there are too few. At least coldMinDrives drives and
// coldMinMiles miles are needed; below that there is no figure, and the answer
// says why and carries the thresholds so the UI states them from here. The
// figure never extrapolates past the temperatures actually driven.

const (
	coldMinDrives   = 5
	coldMinMiles    = 50.0
	coldWindowC     = 4.0
	coldWideWindowC = 7.0
	// A drive enters only if long enough and with enough energy for the ratio to
	// mean something (the same filter as the monthly trend).
	coldMinDriveMi  = trendMinDriveMi
	coldMinDriveKWh = trendMinDriveKWh
	// Efficiency outside this is a recording glitch, not a way of driving.
	coldMinPlausible = 0.3
	coldMaxPlausible = 10.0
	coldMaxGrid      = 8
	coldMaxDrives    = 5000
	coldHistoryYears = 2
	coldMinTempC     = -60.0
	coldMaxTempC     = 60.0
)

// coldDrive is one drive with its stored temperature.
type coldDrive struct {
	TempC float64
	Eff   float64 // mi/kWh
	Mi    float64
}

type coldBand struct {
	Drives       int
	Miles        float64
	LoC, HiC     float64 // the temperature window actually used
	EffLow       float64
	EffHigh      float64
	Insufficient bool
}

// coldWindow selects the drives near tempC (narrow window, then wide) and
// reports whether they are enough. The returned slice is the selection.
func coldWindow(ds []coldDrive, tempC float64) ([]coldDrive, float64, float64, bool) {
	for _, half := range []float64{coldWindowC, coldWideWindowC} {
		var sel []coldDrive
		var mi float64
		for _, d := range ds {
			if math.Abs(d.TempC-tempC) <= half {
				sel = append(sel, d)
				mi += d.Mi
			}
		}
		if len(sel) >= coldMinDrives && mi >= coldMinMiles {
			return sel, tempC - half, tempC + half, true
		}
	}
	return nil, 0, 0, false
}

// coldQuantile is the q-quantile of the drives' efficiency, weighted by distance.
func coldQuantile(sel []coldDrive, q float64) float64 {
	s := append([]coldDrive(nil), sel...)
	sort.Slice(s, func(i, j int) bool { return s[i].Eff < s[j].Eff })
	var total float64
	for _, d := range s {
		total += d.Mi
	}
	var cum float64
	for _, d := range s {
		cum += d.Mi
		if cum >= q*total {
			return d.Eff
		}
	}
	return s[len(s)-1].Eff
}

// coldBandFor is the efficiency band at tempC, or Insufficient.
func coldBandFor(ds []coldDrive, tempC float64) coldBand {
	sel, lo, hi, ok := coldWindow(ds, tempC)
	if !ok {
		return coldBand{Insufficient: true}
	}
	b := coldBand{Drives: len(sel), LoC: lo, HiC: hi, EffLow: coldQuantile(sel, 0.25), EffHigh: coldQuantile(sel, 0.75)}
	for _, d := range sel {
		b.Miles += d.Mi
	}
	return b
}

type coldBasedOn struct {
	Drives int     `json:"drives"`
	Miles  float64 `json:"miles"`
	TempLo float64 `json:"temp_lo_c"`
	TempHi float64 `json:"temp_hi_c"`
}

type coldRange struct {
	Units          insights.Units `json:"units"`
	DistanceUnit   string         `json:"distance_unit"`
	EfficiencyUnit string         `json:"efficiency_unit"`
	TempC          float64        `json:"temp_c"`
	// Reason is empty with a figure; "not_enough_data" or "not_tracked" without.
	Reason         string       `json:"reason,omitempty"`
	EfficiencyLow  *float64     `json:"efficiency_low"`
	EfficiencyHigh *float64     `json:"efficiency_high"`
	RangeLow       *float64     `json:"range_low"`  // null when the pack is not known
	RangeHigh      *float64     `json:"range_high"` // in DistanceUnit
	BasedOn        *coldBasedOn `json:"based_on"`
	// Available are the grid temperatures that meet the minimum, so the UI can
	// grey out the others without asking five times.
	Available []float64 `json:"available"`
	MinDrives int       `json:"min_drives"`
	MinMiles  float64   `json:"min_miles"` // in DistanceUnit
}

func parseColdGrid(s string) ([]float64, bool) {
	if s == "" {
		return nil, true
	}
	parts := strings.Split(s, ",")
	if len(parts) > coldMaxGrid {
		return nil, false
	}
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || math.IsNaN(v) || v < coldMinTempC || v > coldMaxTempC {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

func handleColdRange(sqlDB *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if sqlDB == nil {
			http.Error(w, "cold range unavailable", http.StatusServiceUnavailable)
			return
		}
		q := r.URL.Query()
		tempC, err := strconv.ParseFloat(q.Get("temp_c"), 64)
		if err != nil || math.IsNaN(tempC) || tempC < coldMinTempC || tempC > coldMaxTempC {
			http.Error(w, "temp_c: want a temperature in °C between -60 and 60", http.StatusBadRequest)
			return
		}
		grid, ok := parseColdGrid(q.Get("grid"))
		if !ok {
			http.Error(w, "grid: want up to 8 comma-separated °C values between -60 and 60", http.StatusBadRequest)
			return
		}
		units := insights.Imperial
		if q.Get("units") == string(insights.Metric) {
			units = insights.Metric
		}

		var (
			vid         uuid.UUID
			reportedKWh sql.NullFloat64
			model, trim sql.NullString
			modelYear   sql.NullInt64
		)
		if err := sqlDB.QueryRowContext(r.Context(), `
			SELECT v.id, v.pack_kwh, v.model, v.trim, v.model_year
			  FROM vehicles v WHERE v.user_id = $1 AND v.rivian_vehicle_id = $2`,
			uid, chi.URLParam(r, "vehicleID")).Scan(&vid, &reportedKWh, &model, &trim, &modelYear); err != nil {
			http.Error(w, "vehicle not found", http.StatusNotFound)
			return
		}

		perMi := 1.0
		out := coldRange{
			Units: units, DistanceUnit: "mi", EfficiencyUnit: "mi/kWh", TempC: tempC,
			Available: []float64{}, MinDrives: coldMinDrives, MinMiles: coldMinMiles,
		}
		if units == insights.Metric {
			out.DistanceUnit, out.EfficiencyUnit = "km", "km/kWh"
			perMi = 1.609344
			out.MinMiles = trendRound(coldMinMiles*perMi, 0)
		}
		w.Header().Set("Cache-Control", "private, max-age=300")

		// A truck whose drives cannot be trusted (the R2 without the signals) gets
		// no figure rather than a confident-looking one.
		if rivian.FieldSupport(model.String)["pack_capacity_estimate"] == rivian.FieldUnavailable {
			out.Reason = "not_tracked"
			writeJSON(w, http.StatusOK, out)
			return
		}

		since := time.Now().AddDate(-coldHistoryYears, 0, 0)
		rows, err := sqlDB.QueryContext(r.Context(), `
			SELECT w.temp_c, d.distance_mi, d.energy_used_kwh
			  FROM drives d
			  JOIN drive_weather w ON w.user_id = d.user_id AND w.drive_id = d.external_id
			 WHERE d.user_id = $1 AND d.vehicle_id = $2 AND d.started_at >= $3
			   AND w.temp_c IS NOT NULL AND d.distance_mi >= $4 AND d.energy_used_kwh >= $5
			 ORDER BY d.started_at DESC LIMIT $6`,
			uid, vid, since, coldMinDriveMi, coldMinDriveKWh, coldMaxDrives)
		if err != nil {
			serverError(w, r, "cold range: drives", err)
			return
		}
		defer rows.Close()
		var ds []coldDrive
		for rows.Next() {
			var t, mi, kwh float64
			if err := rows.Scan(&t, &mi, &kwh); err != nil {
				serverError(w, r, "cold range: scan", err)
				return
			}
			if eff := mi / kwh; eff >= coldMinPlausible && eff <= coldMaxPlausible {
				ds = append(ds, coldDrive{TempC: t, Eff: eff, Mi: mi})
			}
		}
		if err := rows.Err(); err != nil {
			serverError(w, r, "cold range: drives", err)
			return
		}

		for _, g := range grid {
			if !coldBandFor(ds, g).Insufficient {
				out.Available = append(out.Available, g)
			}
		}
		b := coldBandFor(ds, tempC)
		if b.Insufficient {
			out.Reason = "not_enough_data"
			writeJSON(w, http.StatusOK, out)
			return
		}
		lo, hi := trendRound(b.EffLow*perMi, 2), trendRound(b.EffHigh*perMi, 2)
		out.EfficiencyLow, out.EfficiencyHigh = &lo, &hi
		out.BasedOn = &coldBasedOn{Drives: b.Drives, Miles: trendRound(b.Miles*perMi, 0), TempLo: trendRound(b.LoC, 1), TempHi: trendRound(b.HiC, 1)}

		// The pack the drives' energy was computed with: what the truck reports,
		// else the model's documented figure when the trim identifies it.
		var pack float64
		if reportedKWh.Valid && reportedKWh.Float64 > 0 {
			pack = reportedKWh.Float64
		} else if spec, known := rivian.InferPackKWhKnown(model.String, trim.String, int(modelYear.Int64)); known {
			pack = spec
		}
		if pack > 0 {
			rl, rh := trendRound(b.EffLow*perMi*pack, 0), trendRound(b.EffHigh*perMi*pack, 0)
			out.RangeLow, out.RangeHigh = &rl, &rh
		}
		writeJSON(w, http.StatusOK, out)
	}
}
