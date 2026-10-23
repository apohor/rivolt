package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/insights"
	"github.com/apohor/rivolt/internal/samples"
)

// driveConditionLister is the slice of *samples.Store the efficiency
// breakdown reads.
type driveConditionLister interface {
	DriveConditions(ctx context.Context, vehicleID string, since, until time.Time) ([]samples.DriveCondition, error)
}

// handleEfficiencyInsights serves GET /api/insights/efficiency: mi/kWh
// (km/kWh with units=metric) by outside temperature, average speed and
// drive mode over [since, until) (RFC 3339; default the last 30 days),
// optionally for one vehicle. Read-only, over the user's own drives and
// samples.
func handleEfficiencyInsights(store driveConditionLister, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		units := insights.Imperial
		if q.Get("units") == string(insights.Metric) {
			units = insights.Metric
		}
		if store == nil {
			writeJSON(w, http.StatusOK, insights.BuildEfficiency(nil, units))
			return
		}
		until := time.Now()
		if s := q.Get("until"); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				http.Error(w, "until: want RFC 3339", http.StatusBadRequest)
				return
			}
			until = t
		}
		since := until.Add(-30 * 24 * time.Hour)
		if s := q.Get("since"); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				http.Error(w, "since: want RFC 3339", http.StatusBadRequest)
				return
			}
			since = t
		}
		if !since.Before(until) {
			http.Error(w, "since must be before until", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		rows, err := store.DriveConditions(ctx, q.Get("vehicle"), since, until)
		if err != nil {
			if logger != nil {
				logger.Warn("efficiency insights query failed", "err", err.Error())
			}
			http.Error(w, "efficiency query failed", http.StatusInternalServerError)
			return
		}
		in := make([]insights.DriveInput, len(rows))
		for i, d := range rows {
			in[i] = insights.DriveInput{ID: d.ID, VehicleID: d.VehicleID, StartedAt: d.StartedAt, EndedAt: d.EndedAt,
				CreatedAt: d.CreatedAt, Source: d.Source, DistanceMi: d.DistanceMi, EnergyKWh: d.EnergyKWh,
				AvgSpeedMph: d.AvgSpeedMph, OutsideTempC: d.OutsideTempC, DriveMode: d.DriveMode, Trailer: d.Trailer}
		}
		writeJSON(w, http.StatusOK, insights.BuildEfficiency(in, units))
	}
}
