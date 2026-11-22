package api

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/rivian"
)

// nowLiveMaxAge is how old the recorder's last write to the open charge or drive row may be and still describe the session now. The
// recorder rewrites both rows every few seconds while the truck streams; an older row belongs to an earlier session or a stalled feed, and
// the card shows nothing rather than a stale number.
const nowLiveMaxAge = 15 * time.Minute

// nowDrive is the drive in progress, read from the recorder's open drive row.
type nowDrive struct {
	StartedAt  time.Time `json:"started_at"`
	DistanceMi float64   `json:"distance_mi"`
	DurationS  float64   `json:"duration_s"`
	// EnergyKWh is the pack energy used so far; null until the state of charge has dropped.
	EnergyKWh *float64 `json:"energy_kwh"`
	// EfficiencyMiPerKWh is distance over energy; null until the drive is long enough (nowMinDriveMi, nowMinDriveKWh) for the ratio to mean anything.
	EfficiencyMiPerKWh *float64 `json:"efficiency_mi_per_kwh"`
	AvgSpeedMph        *float64 `json:"avg_speed_mph"`
}

const (
	nowMinDriveMi  = 0.3
	nowMinDriveKWh = 0.1
)

// nowChargeLive is what the open charge row adds to the charge object.
type nowChargeLive struct {
	EnergyAddedKWh *float64
	Cost           *float64
	Currency       string
	CostSource     string
	CostIsEstimate bool
}

// nowDriveFromRow is the drive object for an open drive row; nil when the row is older than nowLiveMaxAge at now.
func nowDriveFromRow(d drives.Drive, now time.Time) *nowDrive {
	if now.Sub(d.EndedAt) > nowLiveMaxAge {
		return nil
	}
	out := &nowDrive{StartedAt: d.StartedAt, DistanceMi: d.DistanceMi, DurationS: d.EndedAt.Sub(d.StartedAt).Seconds()}
	if out.DurationS < 0 {
		out.DurationS = 0
	}
	if d.EnergyUsedKWh > 0 {
		e := d.EnergyUsedKWh
		out.EnergyKWh = &e
		if d.DistanceMi >= nowMinDriveMi && e >= nowMinDriveKWh {
			eff := d.DistanceMi / e
			out.EfficiencyMiPerKWh = &eff
		}
	}
	if d.AvgSpeedMph > 0 {
		v := d.AvgSpeedMph
		out.AvgSpeedMph = &v
	}
	return out
}

// homeRateFits reports whether the home rate is a fair price for the open charge. The charge list's own guard (IsDCFCSession) needs 1 kWh
// delivered, so in the first minutes of a fast charge it still says "not a fast charge": a peak at or above DCFCMinKW is no home charge, and a Rivian
// charger away from a known home (or with no saved home to tell) is the network's to price. A Wall Charger at home stays priced at the home rate.
func homeRateFits(c charges.Charge, placeAuto string) bool {
	if c.MaxPowerKW >= rivian.DCFCMinKW {
		return false
	}
	place := c.Place // the person's own place wins over the automatic one
	if place == "" {
		place = placeAuto
	}
	return !(c.RivianCharger != nil && *c.RivianCharger && place != rivian.PlaceHome)
}

// nowChargeFromResponse reads the cost of an open charge the way the charge list shows a finished one: the classification's effective source
// decides whether the stored cost counts, a row with no stored price falls back to the home-rate estimate, and an unknown or unpriced session
// has no cost (null), never zero. Estimates (anything but Rivian-billed or typed in by the user) are marked.
func nowChargeFromResponse(resp chargeResponse, now time.Time) *nowChargeLive {
	c := resp.Charge
	if now.Sub(c.EndedAt) > nowLiveMaxAge {
		return nil
	}
	out := &nowChargeLive{}
	if c.EnergyAddedKWh > 0 {
		e := c.EnergyAddedKWh
		out.EnergyAddedKWh = &e
	}
	if resp.Classification == nil {
		return out // the price book could not be read: no cost rather than a wrong one
	}
	src := resp.Classification.CostSource
	switch {
	case src == "user" || (c.Cost > 0 && (src == "rivian" || src == "home_rate" || src == "network_estimate" || src == "observed_estimate")):
		cost := c.Cost
		out.Cost, out.Currency, out.CostSource = &cost, c.Currency, src
	case resp.EstimatedCost > 0 && homeRateFits(c, resp.PlaceAuto):
		cost := resp.EstimatedCost
		out.Cost, out.Currency, out.CostSource = &cost, resp.EstimatedCurrency, "home_rate"
	}
	out.CostIsEstimate = out.Cost != nil && out.CostSource != "rivian" && out.CostSource != "user"
	return out
}

// newNowLive wires the production seams. Both read Postgres only (the recorder's open rows), so any pod answers and none calls Rivian.
func newNowLive(d Deps) (func(ctx context.Context, uid uuid.UUID, rivianID string, now time.Time) *nowChargeLive, func(ctx context.Context, uid uuid.UUID, rivianID string, now time.Time) *nowDrive) {
	charge := func(ctx context.Context, uid uuid.UUID, rivianID string, now time.Time) *nowChargeLive {
		if d.Charges == nil || d.Settings == nil {
			return nil
		}
		c, err := d.Charges.For(uid).LatestOpenLive(ctx, rivianID)
		if err != nil || c == nil {
			return nil
		}
		resp := decorateCharges(ctx, []charges.Charge{*c}, d.Settings.For(uid), billedFor(d, uid)(ctx))
		return nowChargeFromResponse(resp[0], now)
	}
	drive := func(ctx context.Context, uid uuid.UUID, rivianID string, now time.Time) *nowDrive {
		if d.Drives == nil {
			return nil
		}
		row, err := d.Drives.For(uid).LatestOpenLive(ctx, rivianID)
		if err != nil || row == nil {
			return nil
		}
		return nowDriveFromRow(*row, now)
	}
	return charge, drive
}
