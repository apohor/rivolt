package api

import (
	"database/sql"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/insights"
	"github.com/apohor/rivolt/internal/packhealth"
	"github.com/apohor/rivolt/internal/rivian"
)

// GET /api/vehicles/{vehicleID}/trends?months=12&units=imperial|metric: one
// truck's own efficiency and battery-capacity trend for the Truck page. It reads
// only that truck's rows from drives and vehicle_pack_health_samples (never the
// raw samples), in two bounded queries, and compares with nobody.
//
// Efficiency is distance over energy per calendar month in the user's time zone.
// The energy of a drive is the pack-side energy the recorder stored: the SoC
// change times the truck's own usable capacity (the capacity it reports, else the
// model's profile), not the learned estimate below, so the two charts do not feed
// each other. Capacity is the effective pack kWh of each clean charge session
// (/pack-health lists the same samples) with a median once there are enough of
// them. The rules (minimum distance, samples, span) come from here so the UI does
// not hard-code them. Capacity is null on a truck that cannot be tracked.

const (
	trendDefaultMonths = 12
	trendMaxMonths     = 24
	// A month counts toward the trend with at least this much driving.
	trendMinMonthMi = 50.0
	// Efficiency needs this many such months before it is a trend.
	trendMinMonths = 3
	// A drive enters the sums only if it is long enough and used enough energy
	// for the ratio to mean something (SoC is whole percents).
	trendMinDriveMi  = 5.0
	trendMinDriveKWh = 1.0
	// Capacity needs this many clean samples spread over at least this many days.
	trendMinSamples  = 5
	trendMinSpanDays = 30
	// The most capacity samples returned (newest kept).
	trendMaxCapacitySamples = 500
)

// trendEfficiencyPoint is one month. Distance, EnergyKWh, Efficiency and Drives are the drives without a trailer
// connected (the user's correction, else what was recorded): towing is left out so that it does not drag the truck's
// own figure. Trailer, present only when the month has such drives, is their figure on its own.
type trendEfficiencyPoint struct {
	Month      string        `json:"month"` // YYYY-MM in the user's zone
	Distance   float64       `json:"distance"`
	EnergyKWh  float64       `json:"energy_kwh"`
	Efficiency float64       `json:"efficiency"`
	Drives     int           `json:"drives"`
	Trailer    *trendTrailer `json:"trailer,omitempty"`
}

// trendTrailer is a month's drives with a trailer connected (the connector, not the load).
type trendTrailer struct {
	Drives     int     `json:"drives"`
	Distance   float64 `json:"distance"`
	EnergyKWh  float64 `json:"energy_kwh"`
	Efficiency float64 `json:"efficiency"`
}

type trendCapacitySample struct {
	At        time.Time `json:"at"`
	KWhEffect float64   `json:"kwh_effective"`
	// What the estimate came from, for the chart's tooltip: the energy the charge put in (kWh, one decimal) and the
	// battery percentage it added (whole points). Both are the session's own measured figures.
	EnergyKWh   float64 `json:"energy_kwh"`
	SoCDeltaPct int     `json:"soc_delta_pct"`
}

type trendCapacity struct {
	Samples   []trendCapacitySample `json:"samples"`
	MedianKWh *float64              `json:"median_kwh"` // nil until the sample rule is met
	// SpecKWh is the documented capacity of this truck's pack, only when the trim
	// identified the pack (rivian.InferPackKWhKnown); null otherwise, so the UI
	// draws no "Rivian's spec" line against a guess. ReportedKWh is what the truck
	// itself reports (not a spec).
	SpecKWh     *float64 `json:"spec_kwh"`
	ReportedKWh *float64 `json:"reported_kwh"`
	// The rule a charge must meet to produce a sample, from internal/packhealth, so
	// the UI states it from here: it adds at least MinSoCDeltaPct of battery and
	// peaks above MinPeakKW (measured energy, not a SoC-derived fill-in).
	MinSoCDeltaPct float64 `json:"min_soc_delta_pct"`
	MinPeakKW      float64 `json:"min_peak_kw"`
	QualifyingCnt  int     `json:"qualifying_count"`
	SpanDays       int     `json:"span_days"`
	MinSamples     int     `json:"min_samples"`
	MinSpanDays    int     `json:"min_span_days"`
}

type vehicleTrends struct {
	Units            insights.Units `json:"units"`
	DistanceUnit     string         `json:"distance_unit"`
	EfficiencyUnit   string         `json:"efficiency_unit"`
	Timezone         string         `json:"timezone"`
	Months           int            `json:"months"`
	MinMonthDistance float64        `json:"min_month_distance"`
	MinMonths        int            `json:"min_months"`
	QualifyingMonths int            `json:"qualifying_months"`
	// ExcludedTrailerDrives counts the drives over the window that the efficiency points leave out because a trailer
	// was connected (their figure is in each month's Trailer).
	ExcludedTrailerDrives int                    `json:"excluded_trailer_drives"`
	Efficiency            []trendEfficiencyPoint `json:"efficiency"`
	Capacity              *trendCapacity         `json:"capacity"`
}

func trendRound(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	for i := 1; i < len(s); i++ { // short slices; insertion sort keeps this dependency-free
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func handleVehicleTrends(sqlDB *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if sqlDB == nil {
			http.Error(w, "trends unavailable", http.StatusServiceUnavailable)
			return
		}
		q := r.URL.Query()
		months := trendDefaultMonths
		if s := q.Get("months"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > trendMaxMonths {
				http.Error(w, "months: want 1 to 24", http.StatusBadRequest)
				return
			}
			months = n
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
			tzName      sql.NullString
		)
		if err := sqlDB.QueryRowContext(r.Context(), `
			SELECT v.id, v.pack_kwh, v.model, v.trim, v.model_year,
			       (SELECT timezone FROM engagement_prefs e WHERE e.user_id = v.user_id)
			  FROM vehicles v WHERE v.user_id = $1 AND v.rivian_vehicle_id = $2`,
			uid, chi.URLParam(r, "vehicleID")).Scan(&vid, &reportedKWh, &model, &trim, &modelYear, &tzName); err != nil {
			http.Error(w, "vehicle not found", http.StatusNotFound)
			return
		}
		loc, zone := time.UTC, "UTC"
		if tzName.Valid && tzName.String != "" {
			if l, err := time.LoadLocation(tzName.String); err == nil {
				loc, zone = l, tzName.String
			}
		}
		now := time.Now().In(loc)
		since := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -(months - 1), 0)

		out := vehicleTrends{
			Units: units, DistanceUnit: "mi", EfficiencyUnit: "mi/kWh", Timezone: zone, Months: months,
			MinMonthDistance: trendMinMonthMi, MinMonths: trendMinMonths, Efficiency: []trendEfficiencyPoint{},
		}
		perMi := 1.0
		if units == insights.Metric {
			out.DistanceUnit, out.EfficiencyUnit = "km", "km/kWh"
			perMi = 1.609344
			out.MinMonthDistance = trendRound(trendMinMonthMi*perMi, 0)
		}

		rows, err := sqlDB.QueryContext(r.Context(), `
			SELECT to_char(date_trunc('month', started_at AT TIME ZONE $3), 'YYYY-MM'),
			       count(*) FILTER (WHERE NOT t), COALESCE(sum(distance_mi) FILTER (WHERE NOT t), 0), COALESCE(sum(energy_used_kwh) FILTER (WHERE NOT t), 0),
			       count(*) FILTER (WHERE t), COALESCE(sum(distance_mi) FILTER (WHERE t), 0), COALESCE(sum(energy_used_kwh) FILTER (WHERE t), 0)
			  FROM (SELECT started_at, distance_mi, energy_used_kwh, COALESCE(trailer_override, trailer, false) AS t
			          FROM drives
			         WHERE user_id = $1 AND vehicle_id = $2 AND started_at >= $4
			           AND distance_mi >= $5 AND energy_used_kwh >= $6) d
			 GROUP BY 1 ORDER BY 1`, uid, vid, zone, since.UTC(), trendMinDriveMi, trendMinDriveKWh)
		if err != nil {
			serverError(w, r, "vehicle trends: drives", err)
			return
		}
		for rows.Next() {
			var p trendEfficiencyPoint
			var mi, tmi, tkwh float64
			var tn int
			if err := rows.Scan(&p.Month, &p.Drives, &mi, &p.EnergyKWh, &tn, &tmi, &tkwh); err != nil {
				rows.Close()
				serverError(w, r, "vehicle trends: scan drives", err)
				return
			}
			p.Distance = trendRound(mi*perMi, 1)
			if p.EnergyKWh > 0 {
				p.Efficiency = trendRound(mi*perMi/p.EnergyKWh, 2)
			}
			p.EnergyKWh = trendRound(p.EnergyKWh, 1)
			if mi >= trendMinMonthMi {
				out.QualifyingMonths++
			}
			if tn > 0 {
				tp := &trendTrailer{Drives: tn, Distance: trendRound(tmi*perMi, 1), EnergyKWh: trendRound(tkwh, 1)}
				if tkwh > 0 {
					tp.Efficiency = trendRound(tmi*perMi/tkwh, 2)
				}
				p.Trailer = tp
				out.ExcludedTrailerDrives += tn
			}
			out.Efficiency = append(out.Efficiency, p)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			serverError(w, r, "vehicle trends: drives", err)
			return
		}
		rows.Close()

		// Capacity is not tracked where there is no measured charge energy (the
		// R2 today), so say null rather than an empty chart.
		if rivian.FieldSupport(model.String)["pack_capacity_estimate"] != rivian.FieldUnavailable {
			c := &trendCapacity{
				Samples: []trendCapacitySample{}, MinSamples: trendMinSamples, MinSpanDays: trendMinSpanDays,
				MinSoCDeltaPct: packhealth.MinSoCDeltaPct, MinPeakKW: packhealth.MinPeakKWForMeasured,
			}
			if reportedKWh.Valid && reportedKWh.Float64 > 0 {
				rk := reportedKWh.Float64
				c.ReportedKWh = &rk
			}
			if spec, known := rivian.InferPackKWhKnown(model.String, trim.String, int(modelYear.Int64)); known {
				c.SpecKWh = &spec
			}
			srows, err := sqlDB.QueryContext(r.Context(), `
				SELECT at, pack_kwh_effective, energy_delivered_kwh, soc_delta_pct FROM (
				    SELECT at, pack_kwh_effective, energy_delivered_kwh, soc_delta_pct FROM vehicle_pack_health_samples
				     WHERE vehicle_id = $1 AND NOT derate_active AND at >= $2
				     ORDER BY at DESC LIMIT $3) s
				ORDER BY at`, vid, since.UTC(), trendMaxCapacitySamples)
			if err != nil {
				serverError(w, r, "vehicle trends: capacity", err)
				return
			}
			defer srows.Close()
			var kwh []float64
			for srows.Next() {
				var s trendCapacitySample
				var energy, delta float64
				if err := srows.Scan(&s.At, &s.KWhEffect, &energy, &delta); err != nil {
					serverError(w, r, "vehicle trends: scan capacity", err)
					return
				}
				s.At, s.KWhEffect = s.At.UTC(), trendRound(s.KWhEffect, 1)
				s.EnergyKWh, s.SoCDeltaPct = trendRound(energy, 1), int(math.Round(delta))
				c.Samples = append(c.Samples, s)
				kwh = append(kwh, s.KWhEffect)
			}
			if err := srows.Err(); err != nil {
				serverError(w, r, "vehicle trends: capacity", err)
				return
			}
			c.QualifyingCnt = len(c.Samples)
			if n := len(c.Samples); n > 1 {
				c.SpanDays = int(c.Samples[n-1].At.Sub(c.Samples[0].At).Hours() / 24)
			}
			if c.QualifyingCnt >= trendMinSamples && c.SpanDays >= trendMinSpanDays {
				m := trendRound(median(kwh), 1)
				c.MedianKWh = &m
			}
			out.Capacity = c
		}

		w.Header().Set("Cache-Control", "private, max-age=300")
		writeJSON(w, http.StatusOK, out)
	}
}
