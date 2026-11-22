package rivian

// Simulated drive and charge on a replay install (preview): an operator asks for one, and the sample truck plays it through the real
// recorder instead of its scheduled frames for about half an hour, then rejoins the normal timeline.
//
// The request lives in the flags table (row replay_sim, one JSON document) because the click lands on the admin pod and the frames are
// produced by whichever pod holds the recorder lease. A run is just {kind, start}: the frames are rebuilt from the sample corpus each time
// they are needed, so a pod restart or a lease handover mid-run replays the same run, and a new user's backfill includes it.
//
// The loop epoch is untouched: scheduled drives and charges stay where they are, and a request is refused when its window would run into
// one. The full runs start at home; the short ones start wherever the routine has the truck (away, asleep, mid scheduled charge) and
// are refused only on a scheduled drive, with the time it ends. The odometer is the one thing that cannot rejoin by itself (the sim adds miles the schedule does not know about), so every frame
// after a run carries that run's miles. The battery eases back to the schedule's value over two hours after the last run, and a run
// queued behind another begins where that one stopped.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SimKind is what a simulated run plays.
type SimKind string

const (
	SimDrive  SimKind = "drive"
	SimCharge SimKind = "charge"
	// The short variants are kinds of their own, not a flag on the run: a pod that predates them does not know the kind, so it logs and skips the
	// run (the schedule's own frames show) instead of replanning the full-length run inside the short window.
	SimDriveShort  SimKind = "drive_short"
	SimChargeShort SimKind = "charge_short"
)

// base is the kind a short variant is a variant of, and whether it is one.
func (k SimKind) base() (SimKind, bool) {
	switch k {
	case SimDriveShort:
		return SimDrive, true
	case SimChargeShort:
		return SimCharge, true
	}
	return k, false
}

// short is the quick variant of k: the kind itself when it has none.
func (k SimKind) short() SimKind {
	switch k {
	case SimDrive:
		return SimDriveShort
	case SimCharge:
		return SimChargeShort
	}
	return k
}

const (
	// simLead is how far ahead of the request a run starts, so the recorder's feed has read the request before its first frame is due.
	simLead = 15 * time.Second
	// simGuard is the quiet the schedule must keep after a run: the next scheduled drive or charge starts at least this much later.
	simGuard = 10 * time.Minute
	// simKeep is how long a finished run stays in the document: a user whose backfill reaches that far still gets it.
	simKeep = 40 * 24 * time.Hour
	// simEase is how long the battery takes to rejoin the schedule's value after the last run.
	simEase = 2 * time.Hour
	// simRefresh is how often the feed re-reads the document.
	simRefresh = 3 * time.Second
	// simChargeMin is the length of the charging slice.
	simChargeMin = 22
	// simDriveMin is the length of the drive itself.
	simDriveMin = 24
	// simFastKW is the short charge's peak power; with the DC fast model's taper it averages about 99 kW, so simFastGain points of SoC take
	// about four minutes.
	simFastKW = 160.0
	// simFastGain is the SoC points the short charge adds.
	simFastGain = 5.1
)

// SimRun is one requested run, in wall-clock time.
type SimRun struct {
	ID    string    `json:"id"`
	Kind  SimKind   `json:"kind"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// AddedMi is the odometer the run adds: every frame at or after End carries it.
	AddedMi float64 `json:"added_mi"`
	// FromSoC is the SoC points the run starts above (or below) the schedule's battery: what the runs chained before it left. AddedSoC is
	// what this run moves it by. A run booked to begin when the one before it ends (a queued pair) picks up where that one stopped; when the
	// chain ends the battery eases back to the schedule's value over simEase, so there is no step and nothing drifts over days.
	FromSoC  float64 `json:"from_soc,omitempty"`
	AddedSoC float64 `json:"added_soc,omitempty"`
	// EndSoC is the battery the run itself ends at (the fast slice may end above the car's limit, which the ease hands back smoothly).
	EndSoC float64 `json:"end_soc,omitempty"`
}

// SimState is the stored document.
type SimState struct {
	Runs []SimRun `json:"runs"`
	// BaseMi is the miles of runs that aged out of Runs.
	BaseMi float64 `json:"base_mi,omitempty"`
}

// easedSoC is the SoC offset a frame at t carries from the runs before it: where the last run left the battery (relative to the schedule's),
// easing linearly to zero over simEase so the battery rejoins the schedule without a step and without drifting over days. A run that begins
// as the last one ends (the queued pair) starts from the full offset.
func (s SimState) easedSoC(t time.Time) float64 {
	last := s.lastRunBefore(t)
	if last == nil {
		return 0
	}
	age := t.Sub(last.End)
	if age >= simEase {
		return 0
	}
	return (last.FromSoC + last.AddedSoC) * (1 - float64(age)/float64(simEase))
}

// lastRunBefore is the run that ended last at or before t, nil when none did.
func (s SimState) lastRunBefore(t time.Time) *SimRun {
	var last *SimRun
	for i := range s.Runs {
		if r := &s.Runs[i]; !r.End.After(t) && (last == nil || r.End.After(last.End)) {
			last = r
		}
	}
	return last
}

// easedBattery is a schedule frame's battery with the ease applied, held at the car's charge limit while the ease lasts. A run that itself
// ended above the limit (a fast slice on a truck already near it) gives that overshoot back along the same line as the offset, so the cap
// does not step either.
func (s SimState) easedBattery(t time.Time, sched, limit float64) float64 {
	d := s.easedSoC(t)
	if d == 0 || sched <= 0 {
		return sched
	}
	pct := math.Min(100, math.Max(0, sched+d))
	ceil := math.Max(limit, sched)
	if last := s.lastRunBefore(t); last != nil && last.EndSoC > limit {
		ceil += (last.EndSoC - limit) * (1 - float64(t.Sub(last.End))/float64(simEase))
	}
	return math.Min(pct, ceil)
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
type SimRefusal struct {
	Reason string
	// FreeAt is when the truck is free for the request (the end of the scheduled drive in the way), zero when it is not a matter of time.
	FreeAt time.Time
}

func (e *SimRefusal) Error() string { return e.Reason }

// simShortErrand is the short drive's errand: Jackie Robinson Park, 3.5 road miles there and back, about 4.5 minutes at the cruise speed.
// Its two legs to and from SampleHome have road shapes (simLegs, sample_routes_gen.go).
var simShortErrand = samplePoint{34.1636, -118.1500}

// simLegs are the legs the sim drives beyond the corpus's own, for the route generator: the short errand, there and back.
func simLegs() [][2]samplePoint {
	return [][2]samplePoint{{SampleHome, simShortErrand}, {simShortErrand, SampleHome}}
}

// simSpec is what differs between a run's full and short variants. The short drive is a nearer errand at the full drive's cruise speed, so
// speed, distance, time and energy agree in either. The short charge is a DC fast slice at a power a real stall gives, not the home
// charger's 7.6 kW for a few minutes (that moves the battery half a percent and draws a flat line); the energy, the power curve and the pack
// temperature follow the same DC fast model as the corpus's own fast stops. The clock is never sped up.
type simSpec struct {
	errand     samplePoint
	errandName string
	wake       time.Duration // awake and parked before the run starts moving or charging
	chargeKW   float64       // the charger's peak
	chargeGain float64       // SoC points a charge adds
	fast       bool          // the charge is a DC fast one
	driveTail  time.Duration // parked after the drive
	doneTail   time.Duration // plugged in at 0 kW after an AC charge
	cableTail  time.Duration // cable out
}

func specFor(short bool) simSpec {
	if short {
		return simSpec{errand: simShortErrand, errandName: "Jackie Robinson Park", wake: 30 * time.Second, chargeKW: simFastKW, chargeGain: simFastGain, fast: true,
			driveTail: 30 * time.Second, cableTail: 30 * time.Second}
	}
	return simSpec{errand: sampleErrand[0], errandName: "Santa Anita Park", wake: time.Minute, chargeKW: sampleL2KW, chargeGain: sampleL2KW * simChargeMin / 60 / samplePackKWh * 100,
		driveTail: 3 * time.Minute, doneTail: 4 * time.Minute, cableTail: time.Minute}
}

// simPlan is a run's frames-to-be, in corpus time.
type simPlan struct {
	segs    []sampleSeg
	shift   time.Duration // corpus -> wall
	loop    int64
	end     time.Time // wall
	addedMi float64
	endSoC  float64
	addSoC  float64 // SoC points the run's end sits from the schedule's battery at that moment, beyond fromSoC
	// The drive's places: where it starts and ends, and where it turns.
	origin, errand         samplePoint
	originName, errandName string
	// road is the road out to errand when the drive follows a stored shape the corpus has no leg for (an away start's out-and-back); nil when
	// sampleRoad finds the legs between origin and errand.
	road []samplePoint
}

// planSim builds the run that starts at wall time start, or says why it cannot.
func planSim(kind SimKind, start time.Time) (simPlan, error) { return planSimFrom(kind, start, 0) }

// planSimFrom is planSim for a run that begins fromSoC points away from the schedule's battery (the run chained before it).
func planSimFrom(kind SimKind, start time.Time, fromSoC float64) (simPlan, error) {
	kind, short := kind.base()
	sp := specFor(short)
	c, tl := sampleShared()
	k := tl.loopAt(start)
	shift := tl.loopStart(k).Sub(tl.start)
	cs := start.Add(-shift) // corpus time
	if cs.Before(tl.start) || !cs.Before(tl.end) {
		return simPlan{}, &SimRefusal{Reason: "the sample truck's day is wrapping around; try again in a few minutes"}
	}
	i := sort.Search(len(c.segs), func(i int) bool { return c.segs[i].end.After(cs) })
	if i >= len(c.segs) {
		return simPlan{}, &SimRefusal{Reason: "the sample truck's day is wrapping around; try again in a few minutes"}
	}
	cur := c.segs[i]
	origin := cur.path[0]
	atHome := haversineMeters(origin.Lat, origin.Lon, SampleHome.Lat, SampleHome.Lon) <= 300
	if short {
		// A short run is taken wherever the routine has the truck, except on the road: a moving truck cannot be handed to a run without a jump.
		if cur.kind == segDrive {
			return simPlan{}, &SimRefusal{Reason: fmt.Sprintf("the sample truck is on a scheduled drive until %s; try again after", cur.end.Add(shift).UTC().Format("15:04Z")), FreeAt: cur.end.Add(shift)}
		}
	} else {
		if cur.kind != segParked {
			return simPlan{}, &SimRefusal{Reason: "the sample truck is driving or charging on its schedule; try again when it is parked"}
		}
		if !atHome {
			return simPlan{}, &SimRefusal{Reason: "the sample truck is away from home; try again when it is back"}
		}
	}
	// The routine's next drive after this stretch. A scheduled charge inside the window does not refuse the run: the run pre-empts it, as it
	// does one under way, and the battery rejoins the schedule's.
	next, nextEnd, driveLeft := tl.end, tl.end, false
	for _, s := range c.segs[i+1:] {
		if s.kind == segDrive {
			next, nextEnd, driveLeft = s.start, s.end, true
			break
		}
	}
	soc := scheduleSoC(c, cs)
	errand, errandName, originName, back := sp.errand, sp.errandName, "Sample home", origin
	var road []samplePoint
	if atHome {
		back = SampleHome
	} else {
		errand, errandName, originName = awayErrand(origin), "Sample stop", "Sample start"
		if out, turn, ok := awayRoad(origin); ok {
			errand, road = turn, out
		}
	}
	b := &sampleBuilder{rng: nil, t: cs, soc: math.Min(100, math.Max(0, soc+fromSoC)), odo: cur.odoA, at: origin, limit: cur.limit, plugged: false, userPriced: -1}
	wake := sampleSeg{kind: segParked, start: cs, end: cs.Add(sp.wake), step: sampleAwakeStep, socA: b.soc, socB: b.soc,
		odoA: b.odo, odoB: b.odo, path: []samplePoint{b.at}, power: "ready"}
	b.add(wake)
	switch kind {
	case SimDrive:
		if b.soc < 25 {
			return simPlan{}, &SimRefusal{Reason: "the sample truck's battery is too low for a drive"}
		}
		// The cruise speed is the full drive's whatever the variant: the full route's miles over its 24 minutes.
		_, fullMiles := sampleRoad([]samplePoint{SampleHome, specFor(false).errand, SampleHome})
		cruise := fullMiles / (float64(simDriveMin) / 60)
		if road != nil {
			b.driveRoad(cruise, road, origin)
		} else {
			b.drive(cruise, errand, back)
		}
		b.parkSim(sp.driveTail, "ready")
	case SimCharge:
		if b.soc+sp.chargeGain > 100 {
			return simPlan{}, &SimRefusal{Reason: "the sample truck's battery is full"}
		}
		b.charge(b.soc+sp.chargeGain, sp.chargeKW, sp.fast)
		if sp.fast {
			// Wherever the slice happens it is a Rivian-billed fast charge, so the live feed reports a session with it (the Live card waits
			// for that session; a charge at a place that is no sample station would otherwise stay on "waiting for first frame").
			ch := &b.segs[len(b.segs)-1]
			if ch.kind == segCharge && ch.billedRate == 0 {
				for _, st := range sampleDCFCStations {
					if st.billed > 0 {
						ch.billedRate, ch.billedBy = st.billed, st.billedBy
						break
					}
				}
			}
		}
		if !sp.fast {
			b.parkSim(sp.doneTail, "ready") // plugged in at 0 kW (charging complete)
			b.plugged = false
		}
		b.parkSim(sp.cableTail, "ready") // cable out
	default:
		return simPlan{}, &SimRefusal{Reason: "unknown run: " + string(kind)}
	}
	if b.t.Add(simGuard).After(next) {
		if !driveLeft {
			// No scheduled drive is left: what is near is the end of the replayed day, where the routine starts over.
			return simPlan{}, &SimRefusal{Reason: fmt.Sprintf("the sample truck's day wraps around at %s, too soon after a run would end; try again after it", next.Add(shift).UTC().Format("15:04Z")), FreeAt: next.Add(shift)}
		}
		return simPlan{}, &SimRefusal{Reason: fmt.Sprintf("a scheduled drive starts at %s, too soon after a run would end; try again after it ends at %s", next.Add(shift).UTC().Format("15:04Z"), nextEnd.Add(shift).UTC().Format("15:04Z")), FreeAt: nextEnd.Add(shift)}
	}
	return simPlan{segs: b.segs, shift: shift, loop: k, end: b.t.Add(shift), addedMi: b.odo - cur.odoA, endSoC: b.soc, addSoC: b.soc - scheduleSoC(c, b.t) - fromSoC, origin: origin, errand: errand, originName: originName, errandName: errandName, road: road}, nil
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

// SimOpts are how a run is asked for.
type SimOpts struct {
	// Short asks for the quick variant (simSpec).
	Short bool
	// Queue books the run to begin when the run under way ends, instead of refusing while one is. One run may be waiting, no more.
	Queue bool
}

// Start books a run of kind that begins a moment from now, or (opts.Queue) when the run under way ends.
func (c *SimControl) Start(ctx context.Context, kind SimKind, opts SimOpts) (SimRun, error) {
	var run SimRun
	err := c.store.Update(ctx, func(st SimState) (SimState, error) {
		if opts.Short {
			kind = kind.short()
		}
		now := c.now()
		start := now.Add(simLead).Truncate(sampleAwakeStep).Add(sampleAwakeStep)
		unfinished := 0
		for _, r := range st.Runs {
			if r.End.After(now) {
				unfinished++
				if opts.Queue && r.End.After(start) {
					start = r.End // runs are booked in order, so the last unfinished one sets it
				}
			}
		}
		if unfinished > 0 && (!opts.Queue || unfinished > 1) {
			return st, ErrSimBusy
		}
		// The offset is taken from the battery the frames show, which the charge limit holds, not from the uncapped offset.
		from := st.easedSoC(start)
		if soc, limit, ok := scheduleBatteryAt(start); ok {
			from = st.easedBattery(start, soc, limit) - soc
		}
		p, err := planSimFrom(kind, start, from)
		if err != nil {
			return st, err
		}
		run = SimRun{ID: uuid.NewString(), Kind: kind, Start: start, End: p.end, AddedMi: round2(p.addedMi), FromSoC: from, AddedSoC: p.addSoC, EndSoC: round2(p.endSoC)}
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
		if pct := doc.easedBattery(st.At, st.BatteryLevelPct, st.ChargeTargetPct); pct != st.BatteryLevelPct {
			st.DistanceToEmpty *= pct / st.BatteryLevelPct
			st.BatteryLevelPct = pct
		}
		out = append(out, st)
	}
	for _, r := range doc.Runs {
		if !r.Start.Before(to) || !r.End.After(from) {
			continue
		}
		p, err := planSimFrom(r.Kind, r.Start, r.FromSoC)
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
	defer func() { _ = tx.Rollback() }()
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

// simAwayLegMiles is the road one way of a short drive that starts away from home, so the out-and-back is about the short errand's 3.5 miles.
const simAwayLegMiles = 1.8

// awayErrand is the turn of a short drive that starts away from a place with no stored road out of it: a point due north that makes
// the out-and-back about as long as the short errand's road miles (straight legs, at the road ratio). The only straight drive left.
func awayErrand(from samplePoint) samplePoint {
	return samplePoint{Lat: from.Lat + simAwayLegMiles/sampleRoadRatio/69.0, Lon: from.Lon}
}

// awayRoad is the road a short drive takes from a sample place away from home: the first simAwayLegMiles of the shortest stored road shape
// that leaves the place (a prefix of a real road, driven out and back). ok is false where no stored shape leaves from, or none is long
// enough, and the drive is awayErrand's straight line.
func awayRoad(from samplePoint) (out []samplePoint, turn samplePoint, ok bool) {
	prefix := fmt.Sprintf("%.6f,%.6f>", from.Lat, from.Lon)
	best := ""
	bestMi := math.MaxFloat64
	for k, enc := range sampleRouteShapes {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		mi := pathMiles(decodeSamplePolyline(enc))
		if mi >= simAwayLegMiles && (mi < bestMi || mi == bestMi && k < best) {
			best, bestMi = k, mi
		}
	}
	if best == "" {
		return nil, samplePoint{}, false
	}
	pts := decodeSamplePolyline(sampleRouteShapes[best])
	pts[0] = from
	out = []samplePoint{from}
	left := simAwayLegMiles
	for i := 1; i < len(pts); i++ {
		d := haversineMeters(pts[i-1].Lat, pts[i-1].Lon, pts[i].Lat, pts[i].Lon) / 1609.344
		if d >= left {
			g := left / d
			turn = samplePoint{pts[i-1].Lat + (pts[i].Lat-pts[i-1].Lat)*g, pts[i-1].Lon + (pts[i].Lon-pts[i-1].Lon)*g}
			return append(out, turn), turn, true
		}
		out = append(out, pts[i])
		left -= d
	}
	return nil, samplePoint{}, false
}

// scheduleBatteryAt is the routine's battery and charge limit at wall time t (ok false at the wrap).
func scheduleBatteryAt(t time.Time) (soc, limit float64, ok bool) {
	c, tl := sampleShared()
	cs := t.Add(-(tl.loopStart(tl.loopAt(t)).Sub(tl.start)))
	i := sort.Search(len(c.segs), func(i int) bool { return c.segs[i].end.After(cs) })
	if cs.Before(tl.start) || i >= len(c.segs) {
		return 0, 0, false
	}
	return scheduleSoC(c, cs), c.segs[i].limit, true
}

// scheduleSoC is the routine's battery at corpus time t, so the offset a run leaves is measured against what the schedule says at the
// run's end (a scheduled charge goes on under a run that pre-empts it) and the routine rejoins without a step.
func scheduleSoC(c *SampleCorpus, t time.Time) float64 {
	i := sort.Search(len(c.segs), func(i int) bool { return c.segs[i].end.After(t) })
	if i >= len(c.segs) {
		i = len(c.segs) - 1
	}
	s := c.segs[i]
	if n := s.frames(); s.kind == segDrive && n > 0 {
		return s.frameAt(max(0, min(n-1, int(t.Sub(s.start)/s.step)))).BatteryPct
	}
	f := math.Min(1, math.Max(0, float64(t.Sub(s.start))/float64(s.end.Sub(s.start))))
	if s.kind == segCharge && s.dcfc {
		f = 1 - math.Pow(1-f, 1.6)
	}
	return s.socA + (s.socB-s.socA)*f
}
