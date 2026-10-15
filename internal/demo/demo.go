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
	// Earlier versions seeded a hand-written billed and user-priced
	// session; the corpus now provides both (the user price lands on the
	// Vegas hotel L2), so the stale rows go.
	for _, id := range []string{"demo_seed_billed", "demo_seed_user"} {
		if _, err := cs.DeleteByExternalID(ctx, id); err != nil {
			return fmt.Errorf("demo: remove stale seed %s: %w", id, err)
		}
	}
	for _, c := range SeedCharges(uid, now()) {
		if err := cs.Upsert(ctx, c); err != nil {
			return fmt.Errorf("demo: seed charge %s: %w", c.ID, err)
		}
	}
	return nil
}

// SeedCharges are the hand-written older sessions that complete the
// cost-source set (the replay stream supplies home_rate,
// network_estimate, the Rivian-billed DC fast and, via
// ApplySampleUserPrices, the user-priced hotel L2): a pending (unpriced)
// AC session and a home-rate home session. They sit 50-60
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
	pending := mk("demo_seed_pending", at(58, 19), 2*time.Hour+30*time.Minute, 18, 7.2, 34.1394, -118.0447, "away")
	home := mk("demo_seed_home", at(50, 22), 4*time.Hour, 26, 7.6, HomeLat, HomeLon, "home")
	home.Cost, home.PricePerKWh, home.CostSource = round2(26*HomeRate), HomeRate, "home_rate"
	return []charges.Charge{pending, home}
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// pricer is the slice of charges.Store the price pass writes through.
type pricer interface {
	UpdatePricing(ctx context.Context, externalID string, cost float64, currency string, pricePerKWh float64) (int64, error)
}

// ApplySampleUserPrices gives the demo truck's hand-priced session (the
// Las Vegas hotel L2, a flat $15 in the sample corpus) its user price
// after replay has recorded it. The recorder cannot invent a price only
// a person enters, so this is the demo's stand-in for that person. It
// uses the same store path as PATCH /charges/{id}/pricing, which stamps
// cost_source = user.
//
// Only CLOSED charges with no cost source are touched: an open charge is
// still being upserted by the recorder, which would overwrite the price.
// Idempotent; returns how many rows it priced. The corpus loops, so the
// caller runs it again periodically.
func ApplySampleUserPrices(ctx context.Context, d *sql.DB, store pricer) (int, error) {
	if !Enabled() {
		return 0, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT external_id, started_at FROM charges
		 WHERE user_id = $1 AND cost_source IS NULL
		   AND NOT (final_state LIKE 'charging\_%' AND final_state NOT IN ('charging_complete', 'charging_station_err'))`,
		UserID())
	if err != nil {
		return 0, err
	}
	var cands []PriceCandidate
	for rows.Next() {
		var c PriceCandidate
		if err := rows.Scan(&c.ID, &c.StartedAt); err != nil {
			rows.Close()
			return 0, err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	return PriceCandidates(ctx, cands, store)
}

// PriceCandidate is a closed, unpriced demo charge.
type PriceCandidate struct {
	ID        string
	StartedAt time.Time
}

// PriceCandidates prices the candidates the sample corpus says a person
// priced by hand, and returns how many it priced.
func PriceCandidates(ctx context.Context, cands []PriceCandidate, store pricer) (int, error) {
	n := 0
	for _, c := range cands {
		cost, cur, ok := rivian.SampleUserPrice(c.StartedAt)
		if !ok {
			continue
		}
		if _, err := store.UpdatePricing(ctx, c.ID, cost, cur, 0); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RunPricing applies the sample user prices now and then every interval
// until ctx ends (replay backfill and corpus loops bring new sessions).
func RunPricing(ctx context.Context, d *sql.DB, store pricer, every time.Duration, log func(msg string, args ...any)) {
	tick := func() {
		if n, err := ApplySampleUserPrices(ctx, d, store); err != nil {
			log("demo: sample user price pass failed", "err", err.Error())
		} else if n > 0 {
			log("demo: sample user prices applied", "rows", n)
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
