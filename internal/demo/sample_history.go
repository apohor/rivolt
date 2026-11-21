package demo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/rivian"
)

// The preview sample truck's older history (rivian.NewSampleHistory): more than a year of drives, charges and weather behind what the
// replay recorded, so Pro's "unlimited history" has something to show past the free window. It is written only where the live Rivian client
// is forbidden and the sim controls are on (the preview admin site), never for the public demo account, and only before the first recorded
// row of the truck, so it never overlaps a replayed session. Rows are keyed by start time and written with Upsert, so a second run is a no-op.

var (
	// ErrSampleHistoryRefused: the deployment is not one where this may run (prod, or no sim controls).
	ErrSampleHistoryRefused = errors.New("the sample history can only be written where the live Rivian client is forbidden and the sim controls are on")
	// ErrSampleHistoryDemo: the public demo account has its own seeded history.
	ErrSampleHistoryDemo = errors.New("the public demo account's history is not written here")
	// ErrNoRecordedHistory: the account has no recorded drive or charge to end the older history before.
	ErrNoRecordedHistory = errors.New("the account has no recorded history yet; connect the sample truck and let it record first")
)

const sampleHistoryPrefix = "hist_"

// SampleHistoryResult is what a run wrote.
type SampleHistoryResult struct {
	From    time.Time `json:"from"`
	Before  time.Time `json:"before"`
	Drives  int       `json:"drives"`
	Charges int       `json:"charges"`
	Weather int64     `json:"weather"`
}

// SampleHistoryWriter writes the older history for one account.
type SampleHistoryWriter struct {
	DB      *sql.DB
	Drives  *drives.Factory
	Charges *charges.Factory
	Getenv  func(string) string
}

// Allowed reports whether this deployment may write the sample history at all.
func (w *SampleHistoryWriter) Allowed() bool {
	return w != nil && w.Getenv != nil && rivian.SimControlsEnabled(w.Getenv)
}

// Run writes uid's older history, ending before the account's first recorded row. Idempotent.
func (w *SampleHistoryWriter) Run(ctx context.Context, uid uuid.UUID) (SampleHistoryResult, error) {
	var res SampleHistoryResult
	if !w.Allowed() {
		return res, ErrSampleHistoryRefused
	}
	if uid == uuid.Nil || uid == UserID() {
		return res, ErrSampleHistoryDemo
	}
	var first sql.NullTime
	err := w.DB.QueryRowContext(ctx, `
		SELECT min(t) FROM (
		  SELECT min(started_at) AS t FROM drives  WHERE user_id = $1 AND external_id NOT LIKE $2
		  UNION ALL
		  SELECT min(started_at) FROM charges WHERE user_id = $1 AND external_id NOT LIKE $2) x`,
		uid, strings.ReplaceAll(sampleHistoryPrefix, "_", `\_`)+"%").Scan(&first)
	if err != nil {
		return res, err
	}
	if !first.Valid {
		return res, ErrNoRecordedHistory
	}
	before := first.Time.UTC().Truncate(24 * time.Hour)
	h := rivian.NewSampleHistory(before)
	res.From, res.Before = rivian.SampleHistoryAnchor, before
	veh := rivian.DemoVehicleID(uid)
	ds, cs := w.Drives.For(uid), w.Charges.For(uid)
	if ds == nil || cs == nil {
		return res, errors.New("no store for the account")
	}
	for _, d := range h.Drives {
		if err := ds.Upsert(ctx, SampleHistoryDriveRow(veh, d)); err != nil {
			return res, fmt.Errorf("drive %s: %w", d.Start.Format(time.RFC3339), err)
		}
		res.Drives++
		n, err := w.DB.ExecContext(ctx, `
			INSERT INTO drive_weather (user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider, temp_c, conditions)
			VALUES ($1, $2, round($3::numeric, 1), round($4::numeric, 1), date_trunc('hour', $5::timestamptz), 'sample', $6, 'sample')
			ON CONFLICT DO NOTHING`, uid, sampleHistoryID("drive", d.Start), d.StartLat, d.StartLon, d.Start, d.TempC)
		if err != nil {
			return res, fmt.Errorf("weather %s: %w", d.Start.Format(time.RFC3339), err)
		}
		k, _ := n.RowsAffected()
		res.Weather += k
	}
	for _, c := range h.Charges {
		if err := cs.Upsert(ctx, SampleHistoryChargeRow(veh, c)); err != nil {
			return res, fmt.Errorf("charge %s: %w", c.Start.Format(time.RFC3339), err)
		}
		res.Charges++
	}
	return res, nil
}

func sampleHistoryID(kind string, start time.Time) string {
	return fmt.Sprintf("%s%s_%d", sampleHistoryPrefix, kind, start.UTC().Unix())
}

// SampleHistoryDriveRow is the stored form of one generated drive.
func SampleHistoryDriveRow(veh string, d rivian.SampleHistoryDrive) drives.Drive {
	return drives.Drive{
		ID: sampleHistoryID("drive", d.Start), VehicleID: veh, StartedAt: d.Start, EndedAt: d.End,
		StartSoCPct: d.StartSoC, EndSoCPct: d.EndSoC, DistanceMi: d.Miles, EnergyUsedKWh: d.EnergyKWh,
		MaxSpeedMph: d.MaxMph, AvgSpeedMph: d.AvgMph,
		StartLat: d.StartLat, StartLon: d.StartLon, EndLat: d.EndLat, EndLon: d.EndLon,
		RoutePolyline: d.Polyline, Source: "seed",
	}
}

// SampleHistoryChargeRow is the stored form of one generated charge.
func SampleHistoryChargeRow(veh string, c rivian.SampleHistoryCharge) charges.Charge {
	limit := c.LimitPct
	return charges.Charge{
		ID: sampleHistoryID("charge", c.Start), VehicleID: veh, StartedAt: c.Start, EndedAt: c.End,
		StartSoCPct: c.StartSoC, EndSoCPct: c.EndSoC, EnergyAddedKWh: c.EnergyKWh, MilesAdded: c.MilesAdded,
		MaxPowerKW: c.MaxKW, AvgPowerKW: c.AvgKW, FinalState: "charging_complete", Lat: c.Lat, Lon: c.Lon,
		Source: "seed", Cost: c.Cost, Currency: "USD", PricePerKWh: c.PricePerKWh, CostSource: c.CostSource,
		Network: c.Network, Place: c.Place, ActiveSeconds: c.End.Sub(c.Start).Seconds(), ChargeLimitPct: &limit,
	}
}
