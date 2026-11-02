// Package tirelow sends the tire-low alert: an opt-in push when the truck itself reports a tire low (DA's ruling,
// Sprint 8 item 3). It repeats what the truck says and nothing more.
//
//   - Only Rivian's own per-wheel status counts (rivian.TireStatusLow). Never our own pressure floor: a cold night trips
//     a fixed one. A missing or unfamiliar word is no claim either way, so it never alerts and never clears.
//   - A wheel must have been low for SustainedFor: first seen low on a fresh frame at least that long ago, still low on
//     the latest fresh frame, with no fresh "not low" in between (that resets the clock). Frames need not arrive every
//     minute: low after a cold night, woken, driven off and parked again is the common case, and it must fire.
//   - It fires only with the truck in P (stated), never while driving. A parked, awake truck with the owner nearby is
//     exactly the moment to say it.
//   - A wheel's episode ends only after EndAfter of fresh "not low" time, accumulated: a status that flickers at a
//     threshold does not make new episodes, and a gap with no frames never counts as "not low". It is cumulative across
//     awake periods (a gap does not reset it), so a truck awake two hours a day takes days to end an episode; the
//     7-day Cooldown and restarts govern repeats more than this does.
//   - A repeat for the same wheels waits out Cooldown; a wheel that was not in the last alert and goes low inside the
//     cooldown alerts again.
//   - The text is fixed (internal/push.TireLowPayload): the model and the wheel positions. No pressure, no place.
//
// The marker (tire_low_alerts) records only that an alert went out for these wheels, so a restart or a second pod never
// repeats it. The job reads the monitor's latest state and calls nothing on Rivian. That is the merged state: a status
// the truck has not changed since an earlier frame still stands, which is the word the Truck card shows too. It can miss
// one case: if the vehicleState stream drops while Parallax keeps the state's time fresh, an old status can stand; the
// marker and the cooldown limit any repeat, which is acceptable for v1. A pod that does not run the vehicle's monitor does nothing.
package tirelow

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

const (
	// SustainedFor is how long a wheel must have been low (the owner's N). One constant.
	SustainedFor = 30 * time.Minute
	// EndAfter is how much fresh "not low" time, accumulated, ends a wheel's episode.
	EndAfter = 12 * time.Hour
	// Cooldown: no repeat alert for the same wheels within this long.
	Cooldown = 7 * 24 * time.Hour
	// CheckEvery is the timer.
	CheckEvery = time.Minute
	// FreshFrame is how recent a state must be to say anything about a tire, low or not.
	FreshFrame = 10 * time.Minute
	// PruneAfter is how long a marker is kept.
	PruneAfter = 30 * 24 * time.Hour
)

// Positions are the four wheels, as internal/push.JoinWheels knows them.
var Positions = []string{"fl", "fr", "rl", "rr"}

// Status is what the truck's own per-wheel status says about a wheel.
type Status int

const (
	Unknown Status = iota // no word, or one we have no claim for
	Low
	NotLow
)

// Statuses reads each wheel's status from the state.
func Statuses(st *rivian.State) map[string]Status {
	out := map[string]Status{}
	for pos, word := range map[string]string{
		"fl": st.TirePressureStatusFL, "fr": st.TirePressureStatusFR, "rl": st.TirePressureStatusRL, "rr": st.TirePressureStatusRR,
	} {
		switch {
		case rivian.TireStatusLow(word):
			out[pos] = Low
		case rivian.TireStatusNotLow(word):
			out[pos] = NotLow
		}
	}
	return out
}

// Parked says the state states park. Unknown gear is not park.
func Parked(st *rivian.State) bool { return st != nil && st.Gear == "P" }

// Vehicle is a truck whose owner has the alert on.
type Vehicle struct {
	UserID    uuid.UUID
	VehicleID uuid.UUID // the database id
	RivianID  string
	Model     string
}

// Source reads what the job needs.
type Source interface {
	// Vehicles are the trucks of accounts with the alert on for some device; with all set (the dry run), every enabled
	// account's truck.
	Vehicles(ctx context.Context, all bool) ([]Vehicle, error)
	// Latest is the monitor's newest state for the vehicle and when it was last updated; nil when this pod does not run
	// that vehicle's monitor.
	Latest(uid uuid.UUID, rivianID string) (*rivian.State, time.Time)
	// Claim records an alert for these wheels (sorted positions) and reports whether this call was the one: false when
	// the same episode was already claimed or a recent alert already named all of these wheels.
	Claim(ctx context.Context, uid, vehicleID uuid.UUID, newest time.Time, wheels []string) (bool, error)
	// Prune deletes markers older than the cutoff.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// wheel is one wheel's clocks.
type wheel struct {
	lowSince  time.Time     // first seen low on a fresh frame, since the last fresh "not low"; zero when not low
	notLowFor time.Duration // fresh "not low" time accumulated since it was last low (gaps add nothing and do not reset it)
	lastObs   time.Time     // the last tick that saw this wheel's status on a fresh frame
	word      string        // the low word it was last seen with (for the dry-run counter's label)
	alerted   bool          // named in an alert (or a recent one covered it) in this episode
}

// Job is the timer.
type Job struct {
	Source Source
	// Deliver sends the push to the account's devices that have the alert on.
	Deliver func(ctx context.Context, uid uuid.UUID, model string, wheels []string) error
	// Live turns the real push on (a server flag, off by default). Off, the whole path runs for every account's truck but
	// no push, no marker and no opt-in is involved: each episode that would have sent one is counted by WouldSend instead
	// (the class label only: the low word) and logged. Counted per episode, in memory: a restart can count one again.
	Live      bool
	WouldSend func(class string)
	Now       func() time.Time
	Logger    *slog.Logger

	// wheels: each vehicle's wheel clocks. In memory: a restart starts the count again (a late alert, never a duplicate
	// one: the marker stops that).
	wheels    map[uuid.UUID]map[string]*wheel
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
	if j.wheels == nil {
		j.wheels = map[uuid.UUID]map[string]*wheel{}
	}
	now := j.now()
	vs, err := j.Source.Vehicles(ctx, !j.Live)
	if err != nil {
		j.log("tire-low: candidates failed", "err", err.Error())
		return
	}
	seen := map[uuid.UUID]bool{}
	for _, v := range vs {
		seen[v.VehicleID] = true
		j.check(ctx, v, now)
	}
	for id := range j.wheels { // a vehicle whose owner switched the alert off, or who left
		if !seen[id] {
			delete(j.wheels, id)
		}
	}
	if now.Sub(j.lastPrune) >= time.Hour {
		j.lastPrune = now
		if _, err := j.Source.Prune(ctx, now.Add(-PruneAfter)); err != nil {
			j.log("tire-low: prune failed", "err", err.Error())
		}
	}
}

func (j *Job) check(ctx context.Context, v Vehicle, now time.Time) {
	st, at := j.Source.Latest(v.UserID, v.RivianID)
	if st == nil || at.IsZero() || now.Sub(at) > FreshFrame {
		return // not this pod's, or nothing fresh: a stale state says nothing, low or not, and leaves the clocks alone
	}
	ws := j.wheels[v.VehicleID]
	if ws == nil {
		ws = map[string]*wheel{}
		j.wheels[v.VehicleID] = ws
	}
	words := map[string]string{"fl": st.TirePressureStatusFL, "fr": st.TirePressureStatusFR, "rl": st.TirePressureStatusRL, "rr": st.TirePressureStatusRR}
	for pos, s := range Statuses(st) {
		w := ws[pos]
		if w == nil {
			w = &wheel{}
			ws[pos] = w
		}
		gap := now.Sub(w.lastObs)
		w.lastObs = now
		switch s {
		case Low:
			w.word = rivian.TireLowWord(words[pos])
			if w.lowSince.IsZero() {
				w.lowSince = now
			}
			w.notLowFor = 0
		case NotLow:
			w.lowSince = time.Time{} // a fresh "not low" restarts the low clock
			if !w.lastObs.IsZero() && gap > 0 && gap <= 2*CheckEvery {
				w.notLowFor += gap // only back-to-back fresh ticks count; a gap with no frames never does
			}
			if w.notLowFor >= EndAfter {
				delete(ws, pos) // the episode is over
			}
		}
	}
	if !Parked(st) {
		return
	}
	var sustained []string
	var newest time.Time
	fresh := false
	for _, pos := range Positions {
		w := ws[pos]
		if w == nil || w.lowSince.IsZero() || now.Sub(w.lowSince) < SustainedFor {
			continue
		}
		sustained = append(sustained, pos)
		if w.lowSince.After(newest) {
			newest = w.lowSince
		}
		if !w.alerted {
			fresh = true
		}
	}
	if !fresh {
		return // everything sustained was already alerted (or covered by a recent alert) in this episode
	}
	sort.Strings(sustained)
	for _, pos := range sustained {
		ws[pos].alerted = true // won or not, do not claim again every tick
	}
	if !j.Live {
		class := ws[sustained[0]].word
		if j.WouldSend != nil {
			j.WouldSend(class)
		}
		j.log("tire-low: would send (dry run)", "user_id", v.UserID.String(), "wheels", strings.Join(sustained, ","), "class", class)
		return
	}
	won, err := j.Source.Claim(ctx, v.UserID, v.VehicleID, newest, sustained)
	if err != nil {
		for _, pos := range sustained {
			ws[pos].alerted = false // try again next tick
		}
		j.log("tire-low: claim failed", "err", err.Error())
		return
	}
	if !won {
		return // another pod, an earlier run, or a recent alert for these wheels already covers it
	}
	if err := j.Deliver(ctx, v.UserID, v.Model, sustained); err != nil {
		j.log("tire-low: no alert delivered", "user_id", v.UserID.String(), "err", err.Error())
		return
	}
	j.log("tire-low: alert sent", "user_id", v.UserID.String(), "wheels", strings.Join(sustained, ","))
}

func (j *Job) log(msg string, args ...any) {
	if j.Logger != nil {
		j.Logger.Info(msg, args...)
	}
}
