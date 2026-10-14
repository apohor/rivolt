// Package demo seeds and identifies the public read-only demo account
// (B7). It is PREVIEW ONLY: Enabled requires RIVOLT_DEMO=true and
// RIVIAN_CLIENT=replay and RIVOLT_REPLAY_SOURCE=sample, so a production install (live client) never
// turns it on even if the variable leaks into its environment.
package demo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/settings"
)

// Username is the demo account's username. It has no @, so it can't
// collide with a real (email-derived) sign-in, and nobody holds a
// credential for it: it is only ever reached through the anonymous,
// read-only allowlist.
const Username = "rivolt-public-demo"

// The demo truck's "home": the sample generator's public, non-
// residential stadium parking lot (never anyone's address).
const (
	HomeLat   = 34.1613
	HomeLon   = -118.1676
	HomeLabel = "Rose Bowl parking (sample)"
	// HomeRate is the demo's home $/kWh, USD.
	HomeRate = 0.16
)

// replaySessionToken is rivian's marker for a replay login (the
// constant is unexported there; a test pins them together).
const replaySessionToken = "replay-session"

// Enabled reports whether the demo is on: RIVOLT_DEMO=true on an
// install running the replay client over the GENERATED sample corpus
// (RIVOLT_REPLAY_SOURCE=sample). Replaying a recorded vehicle would
// publish a real person's history anonymously, so anything else fails
// closed.
func Enabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("RIVOLT_DEMO")), "true") &&
		strings.EqualFold(strings.TrimSpace(os.Getenv("RIVIAN_CLIENT")), "replay") &&
		strings.TrimSpace(os.Getenv("RIVOLT_REPLAY_SOURCE")) == "sample" &&
		// Defense in depth, independent of merge order: a configured
		// source vehicle means a recorded truck may be what replays.
		strings.TrimSpace(os.Getenv("RIVOLT_REPLAY_SOURCE_VEHICLE")) == ""
}

// UserID is the demo account's user id.
func UserID() uuid.UUID { return db.UserIDFor(Username) }

// VehicleID is the demo vehicle's Rivian id.
func VehicleID() string { return rivian.DemoVehicleID(UserID()) }

// Deps are what Seed writes through.
type Deps struct {
	DB       *sql.DB
	Settings *settings.Factory
	Charges  *charges.Factory
	Secrets  *secrets.Store
	Now      func() time.Time
}

// Seed creates (or refreshes) the demo user, its settings, its replay
// session and a few older charges. Idempotent; safe at every boot. It
// does nothing unless Enabled.
func Seed(ctx context.Context, d Deps) error {
	if !Enabled() {
		return nil
	}
	if d.DB == nil || d.Settings == nil || d.Charges == nil || d.Secrets == nil {
		return errors.New("demo: db, settings, charges and secrets are required")
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	uid, err := db.EnsureUserFull(ctx, d.DB, Username, "", "Demo")
	if err != nil {
		return fmt.Errorf("demo: ensure user: %w", err)
	}
	// The first-ever user row is auto-promoted to admin by a trigger;
	// the demo account must never be one.
	if _, err := d.DB.ExecContext(ctx, `UPDATE users SET role = 'user' WHERE id = $1 AND role <> 'user'`, uid); err != nil {
		return fmt.Errorf("demo: demote: %w", err)
	}
	st := d.Settings.For(uid)
	if err := settings.SetHomeLocation(ctx, st, settings.HomeLocation{
		Set: true, Latitude: HomeLat, Longitude: HomeLon, Label: HomeLabel,
	}); err != nil {
		return fmt.Errorf("demo: home location: %w", err)
	}
	cc, err := settings.GetChargingConfig(ctx, st)
	if err != nil {
		return fmt.Errorf("demo: charging config: %w", err)
	}
	cc.HomePricePerKWh, cc.HomeCurrency = HomeRate, "USD"
	if err := settings.SetChargingConfig(ctx, st, cc); err != nil {
		return fmt.Errorf("demo: charging config: %w", err)
	}
	if err := secrets.SaveRivianSession(ctx, d.Secrets, uid, rivian.Session{
		Email: "demo@rivolt.invalid", UserSessionToken: replaySessionToken, AuthenticatedAt: now().UTC(),
	}); err != nil {
		return fmt.Errorf("demo: session: %w", err)
	}
	cs := d.Charges.For(uid)
	for _, c := range SeedCharges(uid, now()) {
		if err := cs.Upsert(ctx, c); err != nil {
			return fmt.Errorf("demo: seed charge %s: %w", c.ID, err)
		}
	}
	return nil
}

// SeedCharges are the hand-written older sessions that complete the
// cost-source set (the replay stream supplies home_rate and
// network_estimate): Rivian-billed DC fast, user-entered AC, a pending
// (unpriced) AC session and a home-rate home session. They sit 50-60
// days back, before the 45-day replay corpus, so they never overlap a
// replayed session. Times are day-aligned, so reseeding is stable.
func SeedCharges(uid uuid.UUID, now time.Time) []charges.Charge {
	day := now.UTC().Truncate(24 * time.Hour)
	at := func(daysAgo int, hour int) time.Time {
		return day.AddDate(0, 0, -daysAgo).Add(time.Duration(hour) * time.Hour)
	}
	veh := rivian.DemoVehicleID(uid)
	mk := func(id string, start time.Time, dur time.Duration, kwh, peak float64, lat, lon float64, place string) charges.Charge {
		return charges.Charge{
			ID: id, VehicleID: veh, StartedAt: start, EndedAt: start.Add(dur),
			StartSoCPct: 25, EndSoCPct: 25 + kwh/131*100, EnergyAddedKWh: kwh,
			MilesAdded: kwh * 2.3, MaxPowerKW: peak, AvgPowerKW: kwh / dur.Hours(),
			FinalState: "charging_complete", Lat: lat, Lon: lon, Source: "seed",
			Currency: "USD", ActiveSeconds: dur.Seconds(), Place: place,
		}
	}
	billed := mk("demo_seed_billed", at(52, 17), 35*time.Minute, 48, 160, 34.0562, -118.2365, "away")
	billed.Cost, billed.PricePerKWh, billed.CostSource = 27.36, 0.57, "rivian"
	user := mk("demo_seed_user", at(55, 20), 3*time.Hour, 22, 7.6, 34.1478, -118.1445, "away")
	user.Cost, user.PricePerKWh, user.CostSource = 6.60, 0.30, "user"
	pending := mk("demo_seed_pending", at(58, 19), 2*time.Hour+30*time.Minute, 18, 7.2, 34.1394, -118.0447, "away")
	home := mk("demo_seed_home", at(50, 22), 4*time.Hour, 26, 7.6, HomeLat, HomeLon, "home")
	home.Cost, home.PricePerKWh, home.CostSource = round2(26*HomeRate), HomeRate, "home_rate"
	return []charges.Charge{billed, user, pending, home}
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
