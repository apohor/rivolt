// Package leftopen sends the left-open alert: an opt-in push when a door, the frunk, the liftgate, the tailgate or the
// tonneau has stayed open for OpenFor after the truck was parked and left (DA's ruling, Sprint 8 item 4).
//
// The rule is a "walked away" proxy built only from state we already receive; nothing here calls Rivian, wakes the
// truck or asks it anything. Unknown inputs mean no alert: a missed alert is acceptable, one sent for lack of
// knowledge is not. A truck that sleeps with the frunk open stops sending frames, so a timer (not the arrival of a
// frame) drives the check, reading the monitor's latest state.
//
//   - Closures: only what the state positively says is open (a door, the frunk, the liftgate, the tailgate, the
//     tonneau). Windows are not part of v1.
//   - An episode starts when something is first seen open on a fresh state, and ends when everything is closed.
//   - The alert fires once per episode, after OpenFor, when the truck is in P (stated, not assumed), not charging,
//     its power state is known, and it is asleep or standby, or awake but locked on a fresh state.
//   - The text is fixed (internal/push.LeftOpenPayload): the model, what is open, how long. No place.
//
// The marker (left_open_alerts) records only that a vehicle's episode was alerted, so a restart or a second pod never
// repeats it. A pod that does not run the vehicle's monitor sees no state for it and does nothing.
package leftopen

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// OpenFor is how long something must stay open (the owner's N). One constant: changing it is a one-line change.
const OpenFor = 15 * time.Minute

const (
	// CheckEvery is the timer.
	CheckEvery = time.Minute
	// FreshFrame is how recent a state must be to start an episode, and to vouch for an awake, locked truck.
	FreshFrame = 10 * time.Minute
	// MaxAge: a state older than this (a sleeping truck's last frame) no longer says anything.
	MaxAge = 12 * time.Hour
	// PruneAfter is how long a marker is kept.
	PruneAfter = 30 * 24 * time.Hour
	// Cooldown: no second alert for a vehicle within this long of its last.
	Cooldown = 2 * time.Hour
)

// OpenClosures names what the state positively reports open, as the kinds internal/push.JoinClosures knows. A state
// with no real data (nothing has been read yet) reports nothing.
func OpenClosures(st *rivian.State) []string {
	if st == nil || (st.BatteryLevelPct <= 0 && st.OdometerKm <= 0) {
		return nil
	}
	var kinds []string
	if !st.FrunkClosed {
		kinds = append(kinds, "frunk")
	}
	if !st.DoorsClosed {
		kinds = append(kinds, "door")
	}
	if !st.LiftgateClosed {
		kinds = append(kinds, "liftgate")
	}
	if !st.TailgateClosed {
		kinds = append(kinds, "tailgate")
	}
	if !st.TonneauClosed {
		kinds = append(kinds, "tonneau")
	}
	return kinds
}

// WalkedAway says whether the state shows the truck parked and left: in P (stated), not charging, with a known
// power state that is asleep or standby, or awake but locked on a fresh state. at is when the state was last updated.
func WalkedAway(st *rivian.State, at, now time.Time) bool {
	if st == nil || at.IsZero() {
		return false
	}
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	if age > MaxAge || st.Gear != "P" {
		return false
	}
	if cs := strings.ToLower(st.ChargerState); strings.HasPrefix(cs, "charging") && cs != "charging_complete" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(st.PowerState)) {
	case "sleep", "standby":
		return true
	case "": // unknown: no alert
		return false
	default: // awake: only a fresh, locked state vouches for it
		return st.Locked && age <= FreshFrame
	}
}

// Vehicle is a truck whose owner has the alert on.
type Vehicle struct {
	UserID    uuid.UUID
	VehicleID uuid.UUID // the database id
	RivianID  string
	Model     string
}

// Source reads what the job needs.
type Source interface {
	// Vehicles are the trucks of accounts with the alert on for some device.
	Vehicles(ctx context.Context) ([]Vehicle, error)
	// Latest is the monitor's newest state for the vehicle and when it was last updated; nil when this pod does not
	// run that vehicle's monitor.
	Latest(uid uuid.UUID, rivianID string) (*rivian.State, time.Time)
	// Claim records the episode as alerted and reports whether this call was the one (false: already alerted).
	Claim(ctx context.Context, uid, vehicleID uuid.UUID, episodeStart time.Time) (bool, error)
	// Prune deletes markers older than the cutoff.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// Job is the timer.
type Job struct {
	Source Source
	// Deliver sends the push to the account's devices that have the alert on.
	Deliver func(ctx context.Context, uid uuid.UUID, model string, kinds []string, minutes int) error
	Now     func() time.Time
	Logger  *slog.Logger

	// episodes: when each vehicle's current open episode was first seen. In memory: a restart starts the count again
	// (a late alert, never a duplicate one: the marker stops that).
	episodes map[uuid.UUID]time.Time
	// alerted: the episode start already alerted in this process, so the marker is not claimed every tick.
	alerted   map[uuid.UUID]time.Time
	lastPrune time.Time
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// Run ticks every CheckEvery until ctx ends.
func (j *Job) Run(ctx context.Context) {
	t := time.NewTicker(CheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.Tick(ctx)
		}
	}
}

// Tick checks every candidate vehicle once.
func (j *Job) Tick(ctx context.Context) {
	if j.episodes == nil {
		j.episodes = map[uuid.UUID]time.Time{}
		j.alerted = map[uuid.UUID]time.Time{}
	}
	now := j.now()
	vs, err := j.Source.Vehicles(ctx)
	if err != nil {
		j.log("left-open: candidates failed", "err", err.Error())
		return
	}
	seen := map[uuid.UUID]bool{}
	for _, v := range vs {
		seen[v.VehicleID] = true
		j.check(ctx, v, now)
	}
	for id := range j.episodes { // a vehicle whose owner switched the alert off, or who left
		if !seen[id] {
			delete(j.episodes, id)
			delete(j.alerted, id)
		}
	}
	if now.Sub(j.lastPrune) >= time.Hour {
		j.lastPrune = now
		if _, err := j.Source.Prune(ctx, now.Add(-PruneAfter)); err != nil {
			j.log("left-open: prune failed", "err", err.Error())
		}
	}
}

func (j *Job) check(ctx context.Context, v Vehicle, now time.Time) {
	st, at := j.Source.Latest(v.UserID, v.RivianID)
	if st == nil || at.IsZero() {
		return // this pod does not run the vehicle, or nothing is known: leave any episode as it is
	}
	kinds := OpenClosures(st)
	if len(kinds) == 0 {
		delete(j.episodes, v.VehicleID) // everything is closed (or nothing is known open): the episode is over
		delete(j.alerted, v.VehicleID)
		return
	}
	start, in := j.episodes[v.VehicleID]
	if !in {
		if now.Sub(at) > FreshFrame {
			return // an episode starts on a fresh state, never on an old one
		}
		j.episodes[v.VehicleID] = now
		return
	}
	if now.Sub(start) < OpenFor || !WalkedAway(st, at, now) {
		return
	}
	if a, ok := j.alerted[v.VehicleID]; ok && a.Equal(start) {
		return
	}
	j.alerted[v.VehicleID] = start
	won, err := j.Source.Claim(ctx, v.UserID, v.VehicleID, start)
	if err != nil {
		delete(j.alerted, v.VehicleID) // try again next tick
		j.log("left-open: claim failed", "err", err.Error())
		return
	}
	if !won {
		return // another pod, or an earlier run of this one, already alerted this episode
	}
	if err := j.Deliver(ctx, v.UserID, v.Model, kinds, int(OpenFor/time.Minute)); err != nil {
		j.log("left-open: no alert delivered", "user_id", v.UserID.String(), "err", err.Error())
		return
	}
	j.log("left-open: alert sent", "user_id", v.UserID.String())
}

func (j *Job) log(msg string, args ...any) {
	if j.Logger != nil {
		j.Logger.Info(msg, args...)
	}
}
