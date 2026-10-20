package demo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// Older history for the demo truck, so the newer cards draw their charts. The
// generated corpus covers 45 days: two calendar months of drives and four
// qualifying charges, one short of what the Truck tab's trends need (three
// months with 50+ mi each; five charges of 30%+ of the battery over 30+ days).
// These fixtures sit BEFORE the corpus (47 days back and older), so they never
// overlap a replayed session, are day-aligned (reseeding is stable) and carry
// source "seed". All of it is sample data, labelled as such in the demo.

const (
	seedDrivesFirstDay = 47  // days back of the newest seeded drive
	seedDrivesLastDay  = 119 // and of the oldest
	seedDrivesEvery    = 2   // one drive every other day
)

// SeedDrives are short daily-commute drives from 47 to 119 days back, one every
// other day, 9 to 17 mi at the sample truck's 2.3 mi/kWh. They keep every
// calendar month in that span above the trend's 50 mi.
func SeedDrives(uid uuid.UUID, now time.Time) []drives.Drive {
	day := now.UTC().Truncate(24 * time.Hour)
	veh := rivian.DemoVehicleID(uid)
	var out []drives.Drive
	for k, back := 0, seedDrivesFirstDay; back <= seedDrivesLastDay; k, back = k+1, back+seedDrivesEvery {
		miles := float64(9 + (k*7)%9)
		kwh := miles / rivian.SampleMiPerKWh
		drop := kwh / rivian.SamplePackKWh * 100
		start := day.AddDate(0, 0, -back).Add(time.Duration(8*60+(k*11)%50) * time.Minute)
		out = append(out, drives.Drive{
			ID: fmt.Sprintf("demo_seed_drive_%03d", k), VehicleID: veh,
			StartedAt: start, EndedAt: start.Add(time.Duration(miles*2.4) * time.Minute),
			StartSoCPct: round2(80 - drop*float64(k%4)), EndSoCPct: round2(80 - drop*float64(k%4) - drop),
			DistanceMi: miles, EnergyUsedKWh: round2(kwh), MaxSpeedMph: 62, AvgSpeedMph: 31,
			StartLat: HomeLat, StartLon: HomeLon, EndLat: HomeLat, EndLon: HomeLon, Source: "seed",
		})
	}
	return out
}

// SeedDCFCharges are three DC fast stops at 62, 79 and 96 days back that add 58%
// of the battery (from 18%), with effective pack figures of 129 to 133 kWh, so the
// capacity chart has enough qualifying charges over enough days. They are
// user-priced like the hotel L2 so they don't read as pending.
func SeedDCFCharges(uid uuid.UUID, now time.Time) []charges.Charge {
	day := now.UTC().Truncate(24 * time.Hour)
	veh := rivian.DemoVehicleID(uid)
	var out []charges.Charge
	for i, c := range []struct {
		back int
		pack float64
		peak float64
	}{{62, 130.0, 182}, {79, 132.5, 176}, {96, 129.2, 185}} {
		start := day.AddDate(0, 0, -c.back).Add(14 * time.Hour)
		const startSoC, endSoC = 18.0, 76.0
		kwh := round2((endSoC - startSoC) / 100 * c.pack)
		dur := 38 * time.Minute
		out = append(out, charges.Charge{
			ID: fmt.Sprintf("demo_seed_dcfc_%d", i), VehicleID: veh, StartedAt: start, EndedAt: start.Add(dur),
			StartSoCPct: startSoC, EndSoCPct: endSoC, EnergyAddedKWh: kwh, MilesAdded: round2(kwh * rivian.SampleMiPerKWh),
			MaxPowerKW: c.peak, AvgPowerKW: round2(kwh / dur.Hours()), FinalState: "charging_complete",
			Lat: 34.8958, Lon: -117.0173, Source: "seed", Currency: "USD", ActiveSeconds: dur.Seconds(), Place: "away",
			Cost: round2(kwh * 0.48), PricePerKWh: 0.48, CostSource: "user",
		})
	}
	return out
}

// sampleWeatherSQL gives every demo drive without stored weather a sample
// temperature: a seasonal curve (a mild coastal year, 10 to 24 °C) plus a daily
// swing, from the drive's start time, at the drive's start rounded to 0.1 degree
// like the real lookup. provider "sample" says it is a fixture. The cold-range
// card reads drive_weather, so this is what lets it draw in the demo.
const sampleWeatherSQL = `
INSERT INTO drive_weather (user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider, temp_c, conditions)
SELECT d.user_id, d.external_id,
       round(COALESCE(d.start_lat, 0)::numeric, 1), round(COALESCE(d.start_lon, 0)::numeric, 1),
       date_trunc('hour', d.started_at), 'sample',
       round((17 + 7 * sin(2 * pi() * (extract(doy FROM d.started_at) - 110) / 365.0)
                 + 4 * sin(2 * pi() * (extract(hour FROM d.started_at) - 9) / 24.0))::numeric, 1),
       'sample'
  FROM drives d
 WHERE d.user_id = $1
   AND NOT EXISTS (SELECT 1 FROM drive_weather w WHERE w.user_id = d.user_id AND w.drive_id = d.external_id)
ON CONFLICT DO NOTHING`

// ApplySampleWeather fills the sample weather. Idempotent; returns the rows
// added. The corpus loops and the replay records new drives, so the caller runs
// it again periodically.
func ApplySampleWeather(ctx context.Context, d *sql.DB) (int64, error) {
	if !Enabled() {
		return 0, nil
	}
	res, err := d.ExecContext(ctx, sampleWeatherSQL, UserID())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunWeather applies the sample weather now and then every interval until ctx
// ends.
func RunWeather(ctx context.Context, d *sql.DB, every time.Duration, log func(msg string, args ...any)) {
	tick := func() {
		if n, err := ApplySampleWeather(ctx, d); err != nil {
			log("demo: sample weather pass failed", "err", err.Error())
		} else if n > 0 {
			log("demo: sample weather applied", "rows", n)
		}
	}
	tick()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// DemoNetworks is the demo user's price book: sample rates the "sample owner" has
// entered, so the demo's fast-charging sessions (priced from the user's own rate) keep
// their network estimates. The planner and the ledger no longer price anything from the
// dated built-in table, which shows no rate until a user enters one; these are fixtures
// like the rest of the demo, 2 cents off the table so that they read as the user's own
// (a row equal to the table's number is treated as never edited).
func DemoNetworks() []settings.ChargingNetwork {
	var out []settings.ChargingNetwork
	for _, n := range dcfcrates.Networks {
		if len(n.MatchPatterns) == 0 {
			continue
		}
		row := settings.ChargingNetwork{
			Name: n.DisplayName, Slug: n.Slug, Currency: "USD", PricePerKWh: round2(n.GuestRate + 0.02),
			MemberPlan: n.MemberPlan, MemberActive: n.DefaultMemberActive,
		}
		if n.MemberRate != nil {
			row.MemberPrice = round2(*n.MemberRate + 0.02)
		}
		out = append(out, row)
	}
	return out
}
