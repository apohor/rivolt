// Package plugreminder sends the "Plug in tonight?" push: an opt-in, once a
// day, when the truck is parked at the saved home place, not plugged in, and
// below the SoC the user set. It is a user-scheduled utility (DA's ruling): it
// goes out between the user's chosen time and 22:00 local, outside the 09-20
// quiet-hours window and the monthly budget, only to accounts whose browser
// stored a time zone, and every send is recorded through internal/engage.
//
// A sweep runs every 10 minutes on every replica; the engage claim on
// (account, plug_in_reminder, local date) makes that safe.
package plugreminder

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/engage"
)

// Kind is the engage kind of this message.
const Kind = "plug_in_reminder"

// Rules (growth's spec, DA's ruling).
const (
	HomeRadiusM    = 150.0
	MaxStateAge    = 15 * time.Minute
	SuppressWindow = 2 * time.Hour
	// WindowEndMinutes is 22:00 local: no reminder at or after it.
	WindowEndMinutes = 22 * 60
	SweepEvery       = 10 * time.Minute
)

// Candidate is an account that may be due a reminder: it has the reminder on
// for a device and a stored zone.
type Candidate struct {
	User         uuid.UUID
	Timezone     string
	ThresholdPct int
	RemindAtMin  int // minutes after local midnight
}

// Snap is a vehicle's latest recorded state.
type Snap struct {
	Vehicle  string
	At       time.Time
	SoCPct   float64
	Lat, Lon float64
	Gear     string
	SpeedMph float64
	Plugged  bool
}

// Source reads what the rules need.
type Source interface {
	// Candidates are the accounts with the reminder on and a stored zone.
	Candidates(ctx context.Context) ([]Candidate, error)
	// Home is the saved home place; ok=false when there is none.
	Home(ctx context.Context, uid uuid.UUID) (lat, lon float64, ok bool, err error)
	// Latest is each of the account's vehicles' newest state recorded since.
	Latest(ctx context.Context, uid uuid.UUID, since time.Time) ([]Snap, error)
	// RecentlyPlugged: any of the account's vehicles was plugged in at any
	// recorded moment since, or a charge started at home since.
	RecentlyPlugged(ctx context.Context, uid uuid.UUID, homeLat, homeLon float64, since time.Time) (bool, error)
}

// Job is the sweep.
type Job struct {
	Source Source
	Engage *engage.Engage
	Now    func() time.Time
	Logger *slog.Logger
	// On reads the live per-device switch: some subscription still has the
	// reminder on.
	On func(ctx context.Context, uid uuid.UUID) (bool, error)
	// Deliver sends the push to the devices that have it on.
	Deliver func(ctx context.Context, uid uuid.UUID, soc, threshold int) error
}

// Stats is what one sweep did.
type Stats struct{ Considered, InWindow, Due, Sent, Suppressed int }

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// Run sweeps now and then every SweepEvery until ctx ends.
func (j *Job) Run(ctx context.Context) {
	tick := func() {
		if _, err := j.Sweep(ctx); err != nil && ctx.Err() == nil && j.Logger != nil {
			j.Logger.Warn("plug-in reminder sweep failed", "err", err.Error())
		}
	}
	tick()
	t := time.NewTicker(SweepEvery)
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

// Decide applies the parked, at-home, unplugged and low-charge rules to one
// vehicle's latest state; ok is true when the vehicle qualifies.
func Decide(s Snap, homeLat, homeLon float64, thresholdPct int, now time.Time) bool {
	if s.At.IsZero() || now.Sub(s.At) > MaxStateAge || s.At.After(now.Add(time.Minute)) {
		return false
	}
	if (s.Gear != "" && s.Gear != "P") || s.SpeedMph > 0 {
		return false
	}
	if s.Plugged {
		return false
	}
	if s.SoCPct <= 0 || s.SoCPct >= float64(thresholdPct) {
		return false
	}
	if s.Lat == 0 && s.Lon == 0 {
		return false
	}
	return distanceM(s.Lat, s.Lon, homeLat, homeLon) <= HomeRadiusM
}

// Sweep evaluates every candidate once.
func (j *Job) Sweep(ctx context.Context) (Stats, error) {
	var st Stats
	cands, err := j.Source.Candidates(ctx)
	if err != nil {
		return st, err
	}
	now := j.now()
	for _, c := range cands {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		st.Considered++
		loc, err := time.LoadLocation(c.Timezone)
		if err != nil {
			continue
		}
		local := now.In(loc)
		mins := local.Hour()*60 + local.Minute()
		if mins < c.RemindAtMin || mins >= WindowEndMinutes {
			continue
		}
		st.InWindow++
		soc, ok := j.dueSoC(ctx, c, now)
		if !ok {
			continue
		}
		st.Due++
		key := local.Format("2006-01-02")
		uid := c.User
		out, err := j.Engage.Send(ctx, engage.Msg{
			User: uid, Kind: Kind, Key: key, Channel: engage.Push, Utility: true,
			Toggle: func(ctx context.Context) (bool, error) { return j.On(ctx, uid) },
		}, func(ctx context.Context) error { return j.Deliver(ctx, uid, int(math.Round(soc)), c.ThresholdPct) })
		if err != nil && j.Logger != nil {
			j.Logger.Warn("plug-in reminder", "outcome", out, "err", err.Error())
		}
		if out == engage.Sent {
			st.Sent++
		}
	}
	return st, nil
}

// dueSoC reports the lowest qualifying SoC among the account's vehicles, unless
// the truck was plugged in (or charged at home) in the last two hours.
func (j *Job) dueSoC(ctx context.Context, c Candidate, now time.Time) (float64, bool) {
	lat, lon, ok, err := j.Source.Home(ctx, c.User)
	if err != nil || !ok {
		return 0, false // no home place saved: never fires
	}
	snaps, err := j.Source.Latest(ctx, c.User, now.Add(-MaxStateAge))
	if err != nil {
		return 0, false
	}
	soc, found := 0.0, false
	for _, s := range snaps {
		if Decide(s, lat, lon, c.ThresholdPct, now) && (!found || s.SoCPct < soc) {
			soc, found = s.SoCPct, true
		}
	}
	if !found {
		return 0, false
	}
	recent, err := j.Source.RecentlyPlugged(ctx, c.User, lat, lon, now.Add(-SuppressWindow))
	if err != nil || recent {
		return 0, false
	}
	return soc, true
}

func distanceM(aLat, aLon, bLat, bLon float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat, dLon := (bLat-aLat)*rad, (bLon-aLon)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(aLat*rad)*math.Cos(bLat*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}
