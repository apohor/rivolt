package rivian

// Simulated drive and charge on a replay install (preview): an operator asks for one, and the sample truck plays it through the real
// recorder instead of its scheduled frames for about half an hour, then rejoins the normal timeline.
//
// The request lives in the flags table (row replay_sim, one JSON document) because the click lands on the admin pod and the frames are
// produced by whichever pod holds the recorder lease. A run is just {kind, start}: the frames are rebuilt from the sample corpus each time
// they are needed, so a pod restart or a lease handover mid-run replays the same run, and a new user's backfill includes it.
//
// The loop epoch is untouched: scheduled drives and charges stay where they are, and a request is refused when its window would run into
// one. The odometer is the one thing that cannot rejoin by itself (the sim adds miles the schedule does not know about), so every frame
// after a run carries that run's miles. The battery steps back to the schedule's value when a run ends.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SimKind is what a simulated run plays.
type SimKind string

const (
	SimDrive  SimKind = "drive"
	SimCharge SimKind = "charge"
)

const (
	// simLead is how far ahead of the request a run starts, so the recorder's feed has read the request before its first frame is due.
	simLead = 15 * time.Second
	// simGuard is the quiet the schedule must keep after a run: the next scheduled drive or charge starts at least this much later.
	simGuard = 10 * time.Minute
	// simKeep is how long a finished run stays in the document: a user whose backfill reaches that far still gets it.
	simKeep = 40 * 24 * time.Hour
	// simRefresh is how often the feed re-reads the document.
	simRefresh = 3 * time.Second
	// simChargeMin is the length of the charging slice.
	simChargeMin = 22
	// simDriveMin is the length of the drive itself.
	simDriveMin = 24
)

// SimRun is one requested run, in wall-clock time.
type SimRun struct {
	ID    string    `json:"id"`
	Kind  SimKind   `json:"kind"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// AddedMi is the odometer the run adds: every frame at or after End carries it.
	AddedMi float64 `json:"added_mi"`
}

// SimState is the stored document.
type SimState struct {
	Runs []SimRun `json:"runs"`
	// BaseMi is the miles of runs that aged out of Runs.
	BaseMi float64 `json:"base_mi,omitempty"`
}

// offsetMi is the odometer a frame at t carries, not counting run skip (the run t belongs to; its own frames add the earlier ones).
func (s SimState) offsetMi(t time.Time, skip string) float64 {
	mi := s.BaseMi
	for _, r := range s.Runs {
		if r.ID != skip && !r.End.After(t) {
			mi += r.AddedMi
		}
	}
	return mi
}

// SimStore is where the document lives.
type SimStore interface {
	// Load returns the document (empty when none).
	Load(ctx context.Context) (SimState, error)
	// Update runs fn on the document under a lock and stores what it returns; an error from fn stores nothing.
	Update(ctx context.Context, fn func(SimState) (SimState, error)) error
}

// ErrSimBusy is a request made while an earlier run has not finished.
var ErrSimBusy = errors.New("a simulated run is already under way")

// SimRefusal is a request the sample truck cannot take right now; Reason is for the operator.
type SimRefusal struct{ Reason string }

func (e *SimRefusal) Error() string { return e.Reason }

// simPlan is a run's frames-to-be, in corpus time.
type simPlan struct {
	segs    []sampleSeg
	shift   time.Duration // corpus -> wall
	loop    int64
	end     time.Time // wall
	addedMi float64
}

// planSim builds the run that starts at wall time start, or says why it cannot.
func planSim(kind SimKind, start time.Time) (simPlan, error) {
	c, tl := sampleShared()
	k := tl.loopAt(start)
	shift := tl.loopStart(k).Sub(tl.start)
	cs := start.Add(-shift) // corpus time
	if cs.Before(tl.start) || !cs.Before(tl.end) {
		return simPlan{}, &SimRefusal{"the sample truck's day is wrapping around; try again in a few minutes"}
	}
	i := sort.Search(len(c.segs), func(i int) bool { return c.segs[i].end.After(cs) })
	if i >= len(c.segs) || c.segs[i].kind != segParked {
		return simPlan{}, &SimRefusal{"the sample truck is driving or charging on its schedule; try again when it is parked"}
	}
	park := c.segs[i]
	if haversineMeters(park.path[0].Lat, park.path[0].Lon, SampleHome.Lat, SampleHome.Lon) > 300 {
		return simPlan{}, &SimRefusal{"the sample truck is away from home; try again when it is back"}
	}
	next := tl.end
	for _, s := range c.segs[i:] {
		if s.kind != segParked {
			next = s.start
			break
		}
	}
	b := &sampleBuilder{rng: nil, t: cs, soc: park.socA, odo: park.odoA, at: park.path[0], limit: park.limit, plugged: false, userPriced: -1}
	wake := sampleSeg{kind: segParked, start: cs, end: cs.Add(time.Minute), step: sampleAwakeStep, socA: b.soc, socB: b.soc,
		odoA: b.odo, odoB: b.odo, path: []samplePoint{b.at}, power: "ready"}
	b.add(wake)
	switch kind {
	case SimDrive:
		if b.soc < 25 {
			return simPlan{}, &SimRefusal{"the sample truck's battery is too low for a drive"}
		}
		way := []samplePoint{b.at, sampleErrand[0], b.at}
		_, miles := sampleRoad(way)
		cruise := miles / (float64(simDriveMin) / 60)
		b.drive(cruise, sampleErrand[0], SampleHome)
		b.parkSim(3*time.Minute, "ready")
	case SimCharge:
		gain := sampleL2KW * float64(simChargeMin) / 60 / samplePackKWh * 100
		if b.soc+gain > 100 {
			return simPlan{}, &SimRefusal{"the sample truck's battery is full"}
		}
		b.charge(b.soc+gain, sampleL2KW, false)
		b.parkSim(4*time.Minute, "ready") // plugged in at 0 kW (charging complete)
		b.plugged = false
		b.parkSim(time.Minute, "ready") // cable out
	default:
		return simPlan{}, &SimRefusal{"unknown run: " + string(kind)}
	}
	if b.t.Add(simGuard).After(next) {
		return simPlan{}, &SimRefusal{"a scheduled drive or charge starts too soon after a run would end; try again later"}
	}
	return simPlan{segs: b.segs, shift: shift, loop: k, end: b.t.Add(shift), addedMi: b.odo - park.odoA}, nil
}

// parkSim adds a parked stretch of d at the builder's place.
func (b *sampleBuilder) parkSim(d time.Duration, power string) {
	b.add(sampleSeg{kind: segParked, start: b.t, end: b.t.Add(d), step: sampleAwakeStep, socA: b.soc, socB: b.soc,
		odoA: b.odo, odoB: b.odo, path: []samplePoint{b.at}, plugged: b.plugged, power: power})
}

// SimControlsEnabled is the admin site's switch for the buttons: asked for (RIVOLT_SIM_CONTROLS) and a live Rivian client forbidden outright
// (RIVOLT_RIVIAN_LIVE_FORBIDDEN). Unset or false in either is off, which is what prod's admin site has.
func SimControlsEnabled(getenv func(string) string) bool {
	return ParseBoolEnv(getenv("RIVOLT_SIM_CONTROLS")) && ParseBoolEnv(getenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN"))
}

// SimControl takes requests (the admin side).
type SimControl struct {
	store SimStore
	now   func() time.Time
}

// NewSimControl builds the control over store.
func NewSimControl(store SimStore) *SimControl { return &SimControl{store: store, now: time.Now} }

// Start books a run of kind that begins a moment from now.
func (c *SimControl) Start(ctx context.Context, kind SimKind) (SimRun, error) {
	var run SimRun
	err := c.store.Update(ctx, func(st SimState) (SimState, error) {
		now := c.now()
		for _, r := range st.Runs {
			if r.End.After(now) {
				return st, ErrSimBusy
			}
		}
		start := now.Add(simLead).Truncate(sampleAwakeStep).Add(sampleAwakeStep)
		p, err := planSim(kind, start)
		if err != nil {
			return st, err
		}
		run = SimRun{ID: uuid.NewString(), Kind: kind, Start: start, End: p.end, AddedMi: round2(p.addedMi)}
		st.Runs = append(st.Runs, run)
		kept := st.Runs[:0]
		for _, r := range st.Runs {
			if now.Sub(r.End) > simKeep {
				st.BaseMi += r.AddedMi
				continue
			}
			kept = append(kept, r)
		}
		st.Runs = kept
		return st, nil
	})
	return run, err
}

// Active returns the run that has not finished, if any.
func (c *SimControl) Active(ctx context.Context) (*SimRun, error) {
	st, err := c.store.Load(ctx)
	if err != nil {
		return nil, err
	}
	now := c.now()
	for _, r := range st.Runs {
		if r.End.After(now) {
			r := r
			return &r, nil
		}
	}
	return nil, nil
}

// simSource is the feed's cached view of the document.
type simSource struct {
	store SimStore
	mu    sync.Mutex
	at    time.Time
	state SimState
}

func (s *simSource) get(ctx context.Context) SimState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.at) < simRefresh {
		return s.state
	}
	if st, err := s.store.Load(ctx); err == nil {
		s.state = st
	}
	s.at = time.Now()
	return s.state
}

// SetSimStore makes the feed play simulated runs from store. Only the generated sample truck has runs to play.
func (f *ReplayFeed) SetSimStore(store SimStore) { f.sim = &simSource{store: store} }

// SetSimFrameHook registers fn to see every simulated frame of a run, to add what the sample corpus does not carry (a destination and
// trip progress for the simulated drive).
func (f *ReplayFeed) SetSimFrameHook(fn func(run SimRun, st *State)) { f.simHook = fn }

// withSim swaps the schedule's frames inside each run's window for the run's own, and carries the runs' miles on later frames.
func (f *ReplayFeed) withSim(ctx context.Context, from, to time.Time, in []State) []State {
	doc := f.sim.get(ctx)
	if len(doc.Runs) == 0 && doc.BaseMi == 0 {
		return in
	}
	out := in[:0:0]
	for _, st := range in {
		inRun := false
		for _, r := range doc.Runs {
			if !st.At.Before(r.Start) && st.At.Before(r.End) {
				inRun = true
				break
			}
		}
		if inRun {
			continue
		}
		if st.OdometerKm > 0 {
			st.OdometerKm += doc.offsetMi(st.At, "") / kmToMi
		}
		out = append(out, st)
	}
	for _, r := range doc.Runs {
		if !r.Start.Before(to) || !r.End.After(from) {
			continue
		}
		p, err := planSim(r.Kind, r.Start)
		if err != nil {
			f.logger.Warn("replay sim: run cannot be rebuilt", "run", r.ID, "err", err.Error())
			continue
		}
		off := doc.offsetMi(r.Start, r.ID) / kmToMi
		for _, seg := range p.segs {
			for n := seg.frames(); n > 0; n-- {
				fr := seg.frameAt(n - 1)
				at := fr.At.Add(p.shift)
				if at.Before(from) || !at.Before(to) || at.Before(r.Start) || !at.Before(r.End) {
					continue
				}
				st := f.timeline.place(fr.toState(), replaySegment{shift: p.shift, loop: p.loop})
				if st.OdometerKm > 0 {
					st.OdometerKm += off
				}
				if f.simHook != nil {
					f.simHook(r, &st)
				}
				out = append(out, st)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// flagsSimStore keeps the document in the flags table.
type flagsSimStore struct{ db *sql.DB }

// simFlagName is the flags row that holds the document.
const simFlagName = "replay_sim"

// NewFlagsSimStore is the SimStore over the flags table of db (the system pool).
func NewFlagsSimStore(db *sql.DB) SimStore { return flagsSimStore{db: db} }

func (s flagsSimStore) Load(ctx context.Context) (SimState, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM flags WHERE name = $1`, simFlagName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return SimState{}, nil
	}
	if err != nil {
		return SimState{}, fmt.Errorf("replay sim: load: %w", err)
	}
	var st SimState
	if err := json.Unmarshal(raw, &st); err != nil {
		return SimState{}, fmt.Errorf("replay sim: decode: %w", err)
	}
	return st, nil
}

func (s flagsSimStore) Update(ctx context.Context, fn func(SimState) (SimState, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replay sim: begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO flags (name, value, updated_by) VALUES ($1, '{}'::jsonb, 'replay_sim') ON CONFLICT (name) DO NOTHING`, simFlagName); err != nil {
		return fmt.Errorf("replay sim: seed: %w", err)
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT value FROM flags WHERE name = $1 FOR UPDATE`, simFlagName).Scan(&raw); err != nil {
		return fmt.Errorf("replay sim: lock: %w", err)
	}
	var st SimState
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("replay sim: decode: %w", err)
	}
	next, err := fn(st)
	if err != nil {
		return err
	}
	out, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("replay sim: encode: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE flags SET value = $2, updated_at = now(), updated_by = 'replay_sim' WHERE name = $1`, simFlagName, out); err != nil {
		return fmt.Errorf("replay sim: store: %w", err)
	}
	return tx.Commit()
}
