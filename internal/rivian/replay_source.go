package rivian

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// FrameSource feeds a StateMonitor full-snapshot State frames in place
// of a live Rivian subscription (see NewSourceMonitor). Stream blocks
// until ctx is cancelled, calling emit once per frame in time order.
// catchingUp is true while the source is replaying frames whose time
// has already passed (backfill after a restart or first connect), so
// the monitor can hold back user-facing side effects such as push
// notifications for sessions that "closed" days ago. Stream returns
// ErrVehicleNotFound for a vehicle the source doesn't serve.
type FrameSource interface {
	Stream(ctx context.Context, vehicleID string, emit func(st State, catchingUp bool)) error
}

// ReplayCorpus is a time-ordered store of recorded frames that a demo
// vehicle replays. Times are the corpus's own (recorded) times.
type ReplayCorpus interface {
	// Frames returns the frames in [from, to), oldest first.
	Frames(ctx context.Context, from, to time.Time) ([]State, error)
	// LastAtOrBefore returns the newest frame at or before t, or nil.
	LastAtOrBefore(ctx context.Context, t time.Time) (*State, error)
}

// replayEpoch anchors every install's replay timeline to the same wall
// clock, so all replicas (and a pod after a restart) agree on which
// corpus moment "now" maps to without coordinating.
var replayEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// replayLoopGap is the parked pause inserted between the end of the
// corpus and the start of its next loop, so the wrap reads as an
// overnight park rather than a teleport mid-session.
const replayLoopGap = 6 * time.Hour

const (
	// replayChunk bounds one corpus query while catching up.
	replayChunk = time.Hour
	// replayTick is the poll cadence once caught up to now. Recorded
	// frames are 1–5 s apart while driving and ~30 s parked, so 2 s
	// keeps the live panel moving without hammering Postgres.
	replayTick = 2 * time.Second
	// replayLiveSlack: a chunk ending within this much of now counts as
	// live, not catch-up.
	replayLiveSlack = 2 * time.Minute
)

// replayTimeline maps wall-clock time onto a looping corpus. Loop k
// plays the corpus over wall [epoch+k·period, epoch+k·period+span),
// followed by replayLoopGap of silence. Odometer advances by the
// corpus's driven distance each loop so it never runs backwards.
type replayTimeline struct {
	start, end time.Time // corpus span
	epoch      time.Time
	gap        time.Duration
	odoSpanKm  float64
}

func (t replayTimeline) span() time.Duration   { return t.end.Sub(t.start) }
func (t replayTimeline) period() time.Duration { return t.span() + t.gap }

func (t replayTimeline) loopAt(w time.Time) int64 {
	if !w.After(t.epoch) {
		return 0
	}
	return int64(w.Sub(t.epoch) / t.period())
}

func (t replayTimeline) loopStart(k int64) time.Time {
	return t.epoch.Add(time.Duration(k) * t.period())
}

// replaySegment is one contiguous corpus window and the transform that
// places its frames on the wall clock.
type replaySegment struct {
	from, to time.Time // corpus time, [from, to)
	shift    time.Duration
	loop     int64
}

// segments returns the corpus windows that play during wall [from, to).
func (t replayTimeline) segments(from, to time.Time) []replaySegment {
	var out []replaySegment
	for k := t.loopAt(from); ; k++ {
		ls := t.loopStart(k)
		if !ls.Before(to) {
			break
		}
		a, b := from, to
		if a.Before(ls) {
			a = ls
		}
		if le := ls.Add(t.span()); b.After(le) {
			b = le
		}
		if a.Before(b) {
			out = append(out, replaySegment{
				from:  t.start.Add(a.Sub(ls)),
				to:    t.start.Add(b.Sub(ls)),
				shift: ls.Sub(t.start),
				loop:  k,
			})
		}
	}
	return out
}

// place moves a corpus frame onto the wall clock for segment seg.
func (t replayTimeline) place(st State, seg replaySegment) State {
	st.At = st.At.Add(seg.shift)
	if !st.LocationFixAt.IsZero() {
		st.LocationFixAt = st.LocationFixAt.Add(seg.shift)
	}
	if st.OdometerKm > 0 {
		st.OdometerKm += float64(seg.loop) * t.odoSpanKm
	}
	return st
}

// ReplayFeed plays a recorded corpus as if it were a live vehicle,
// time-shifted so the corpus's "now" is the wall clock's now. It is the
// RIVIAN_CLIENT=replay data source: preview gets moving demo vehicles
// through the real recorder without any Rivian connection.
type ReplayFeed struct {
	corpus   ReplayCorpus
	timeline replayTimeline
	// source describes the corpus vehicle; demo vehicles inherit its
	// model/year/pack so energy math matches the recorded frames.
	source   Vehicle
	backfill time.Duration
	// allow, when set, limits which users may connect a demo vehicle.
	// The corpus is a real vehicle's location history; an install with
	// shared sign-in (preview) must not show it to arbitrary accounts.
	allow func(uid uuid.UUID) bool
	// lastRecorded returns the newest persisted sample for a demo
	// vehicle (zero time when none), which is where Stream resumes.
	lastRecorded func(ctx context.Context, vehicleID string) (time.Time, error)
	logger       *slog.Logger
}

// Stream replays frames for vehicleID from where its recording left off
// (or backfill ago, whichever is later) up to now as fast as the
// recorder accepts them, then keeps pace with the wall clock.
func (f *ReplayFeed) Stream(ctx context.Context, vehicleID string, emit func(st State, catchingUp bool)) error {
	cursor := time.Now().Add(-f.backfill)
	if f.lastRecorded != nil {
		last, err := f.lastRecorded(ctx, vehicleID)
		if err != nil {
			return fmt.Errorf("replay: resume point: %w", err)
		}
		// Half-open windows: skip the frame already persisted at last.
		if next := last.Add(time.Microsecond); next.After(cursor) {
			cursor = next
		}
	}
	f.logger.Info("replay stream start", "vehicle", vehicleID, "from", cursor.UTC().Format(time.RFC3339))
	caughtUp := false
	var plug plugTracker
	for ctx.Err() == nil {
		now := time.Now()
		to := cursor.Add(replayChunk)
		if to.After(now) {
			to = now
		}
		if to.After(cursor) {
			frames, err := f.frames(ctx, cursor, to)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				f.logger.Warn("replay frames query failed", "vehicle", vehicleID, "err", err.Error())
				if !sleepCtx(ctx, 10*time.Second) {
					return nil
				}
				continue
			}
			// Per frame, not per chunk: the last backfill chunk ends at
			// now but still holds up to replayChunk of elapsed history.
			for _, st := range frames {
				if ctx.Err() != nil {
					return nil
				}
				plug.apply(&st)
				emit(st, now.Sub(st.At) > replayLiveSlack)
			}
			cursor = to
			if now.Sub(to) <= replayLiveSlack && !caughtUp {
				caughtUp = true
				f.logger.Info("replay stream caught up", "vehicle", vehicleID)
			}
		}
		if time.Since(cursor) < replayChunk && !sleepCtx(ctx, replayTick) {
			return nil
		}
	}
	return nil
}

// frames returns the wall-clock-placed frames for wall [from, to).
func (f *ReplayFeed) frames(ctx context.Context, from, to time.Time) ([]State, error) {
	var out []State
	for _, seg := range f.timeline.segments(from, to) {
		fs, err := f.corpus.Frames(ctx, seg.from, seg.to)
		if err != nil {
			return nil, err
		}
		for _, st := range fs {
			out = append(out, f.timeline.place(st, seg))
		}
	}
	return out, nil
}

// latest returns the frame a live subscription would most recently have
// delivered at wall time w — during the loop gap, the corpus's last.
func (f *ReplayFeed) latest(ctx context.Context, w time.Time) (*State, error) {
	k := f.timeline.loopAt(w)
	ls := f.timeline.loopStart(k)
	c := f.timeline.start.Add(w.Sub(ls))
	if c.After(f.timeline.end) {
		c = f.timeline.end
	}
	st, err := f.corpus.LastAtOrBefore(ctx, c)
	if err != nil || st == nil {
		return nil, err
	}
	placed := f.timeline.place(*st, replaySegment{shift: ls.Sub(f.timeline.start), loop: k})
	return &placed, nil
}

// plugTracker carries cable state across replayed frames. toState can
// only infer "plugged" from unambiguous charging states, so a scheduled
// pause (charging_ready mid-session) read as unplugged and split one
// overnight charge in two. Once a frame shows the cable in, it stays in
// through charging_ready until the car reports it disconnected or
// drives off — what a live chargerStatus would have said.
type plugTracker struct{ plugged bool }

func (p *plugTracker) apply(st *State) {
	switch {
	case isDrivingGear(st.Gear), strings.EqualFold(st.ChargerState, "charger_disconnected"):
		p.plugged = false
	case isPluggedCS(st.ChargerStatus):
		p.plugged = true
	case p.plugged && strings.EqualFold(st.ChargerState, "charging_ready"):
		st.ChargerStatus = "chrgr_sts_connected_charging"
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// replayColumns are the vehicle_state columns a replayFrame decodes,
// named to match its JSON tags so row_to_json drops straight in.
const replayColumns = `at, shift_state, battery_level_pct, odometer_mi, range_mi,
	speed_mph, lat, lon, charging_state, charger_power_kw, power_state,
	location_fix_at, drive_mode, charge_limit_pct, inside_temp_c,
	outside_temp_c, tire_pressure_min_bar, pack_temp_avg_c, pack_temp_max_c,
	pack_temp_min_c`

// dbCorpus reads one vehicle's recorded vehicle_state rows.
type dbCorpus struct {
	db        *sql.DB
	vehicleID uuid.UUID // vehicles.id of the corpus vehicle
}

func (c *dbCorpus) Frames(ctx context.Context, from, to time.Time) ([]State, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT row_to_json(t) FROM (
			SELECT `+replayColumns+` FROM vehicle_state
			WHERE vehicle_id = $1 AND at >= $2 AND at < $3
			ORDER BY at
		) t`, c.vehicleID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []State
	for rows.Next() {
		st, err := scanReplayRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (c *dbCorpus) LastAtOrBefore(ctx context.Context, t time.Time) (*State, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT row_to_json(t) FROM (
			SELECT `+replayColumns+` FROM vehicle_state
			WHERE vehicle_id = $1 AND at <= $2
			ORDER BY at DESC LIMIT 1
		) t`, c.vehicleID, t)
	st, err := scanReplayRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func scanReplayRow(r interface{ Scan(...any) error }) (State, error) {
	var raw []byte
	if err := r.Scan(&raw); err != nil {
		return State{}, err
	}
	var f replayFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return State{}, err
	}
	return f.toState(), nil
}

// NewDBReplayFeed builds a feed that replays the recorded vehicle_state
// of the vehicle whose Rivian id is sourceVehicleID, restricted to
// [from, to) when either is non-zero. Demo vehicles resume from their own
// latest sample in the same database, falling back to backfill ago.
func NewDBReplayFeed(ctx context.Context, pool *sql.DB, sourceVehicleID string, from, to time.Time, backfill time.Duration, logger *slog.Logger) (*ReplayFeed, error) {
	if pool == nil {
		return nil, errors.New("replay: no database")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if to.IsZero() {
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	var (
		vid               uuid.UUID
		src               Vehicle
		vin, name, model  sql.NullString
		year              sql.NullInt64
		pack              sql.NullFloat64
		first, last       sql.NullTime
		odoFirst, odoLast sql.NullFloat64
	)
	err := pool.QueryRowContext(ctx, `
		SELECT id, vin, display_name, model, model_year, pack_kwh
		FROM vehicles WHERE rivian_vehicle_id = $1
		ORDER BY created_at LIMIT 1`, sourceVehicleID).
		Scan(&vid, &vin, &name, &model, &year, &pack)
	if err != nil {
		return nil, fmt.Errorf("replay: source vehicle %q: %w", sourceVehicleID, err)
	}
	src = Vehicle{ID: sourceVehicleID, VIN: vin.String, Name: name.String,
		Model: model.String, ModelYear: int(year.Int64), PackKWh: pack.Float64}
	err = pool.QueryRowContext(ctx, `
		SELECT min(at), max(at), min(odometer_mi) FILTER (WHERE odometer_mi > 0),
		       max(odometer_mi)
		FROM vehicle_state WHERE vehicle_id = $1 AND at >= $2 AND at < $3`,
		vid, from, to).Scan(&first, &last, &odoFirst, &odoLast)
	if err != nil {
		return nil, fmt.Errorf("replay: corpus span: %w", err)
	}
	if !first.Valid || !last.Valid || !last.Time.After(first.Time) {
		return nil, fmt.Errorf("replay: no recorded frames for %q in range", sourceVehicleID)
	}
	f := &ReplayFeed{
		corpus: &dbCorpus{db: pool, vehicleID: vid},
		timeline: replayTimeline{
			start:     first.Time,
			end:       last.Time,
			epoch:     replayEpoch,
			gap:       replayLoopGap,
			odoSpanKm: (odoLast.Float64 - odoFirst.Float64) / kmToMi,
		},
		source:   src,
		backfill: backfill,
		logger:   logger,
	}
	f.lastRecorded = func(ctx context.Context, vehicleID string) (time.Time, error) {
		var at sql.NullTime
		err := pool.QueryRowContext(ctx, `
			SELECT max(vs.at) FROM vehicle_state vs
			JOIN vehicles v ON v.id = vs.vehicle_id
			WHERE v.rivian_vehicle_id = $1`, vehicleID).Scan(&at)
		return at.Time, err
	}
	logger.Info("replay corpus loaded",
		"source_vehicle", sourceVehicleID,
		"from", first.Time.UTC().Format(time.RFC3339),
		"to", last.Time.UTC().Format(time.RFC3339),
		"loop", f.timeline.period().Round(time.Hour).String())
	return f, nil
}

// SetAllowedUsers restricts demo vehicles to the given users. nil
// (the default) allows everyone.
func (f *ReplayFeed) SetAllowedUsers(allow func(uid uuid.UUID) bool) { f.allow = allow }

// ErrReplayNotAllowed is returned by ReplayClient.Login for a user the
// install hasn't allowed to connect a demo vehicle.
var ErrReplayNotAllowed = errors.New("replay: demo vehicle not enabled for this account")

// replaySessionToken marks a Session as a replay login. Restore accepts
// nothing else, so a real Rivian session left in user_secrets can never
// switch a demo account on (nor a demo session pass for a real one).
const replaySessionToken = "replay-session"

// DemoVehicleID is the rivian_vehicle_id of uid's demo vehicle. Unique
// per user: leases and owner resolution key on it install-wide.
func DemoVehicleID(uid uuid.UUID) string { return "demo-" + uid.String() }

// ReplayClient is the RIVIAN_CLIENT=replay Account: any sign-in succeeds
// and yields one demo vehicle per user that streams the shared corpus
// through the recorder. It never opens a network connection.
type ReplayClient struct {
	feed    *ReplayFeed
	vehicle Vehicle
	allowed bool
	// planner answers trip plans (fixtures, then the synthetic planner);
	// nil means this install plans nothing.
	planner TripPlanner

	mu              sync.Mutex
	email           string
	authenticatedAt time.Time
	authReady       chan struct{}
}

// NewReplayClient returns uid's logged-out replay account.
func NewReplayClient(feed *ReplayFeed, uid uuid.UUID) *ReplayClient {
	model := feed.source.Model
	if model == "" {
		model = "R1S"
	}
	hex := strings.ToUpper(strings.ReplaceAll(uid.String(), "-", ""))
	return &ReplayClient{
		feed: feed,
		vehicle: Vehicle{
			ID:        DemoVehicleID(uid),
			VIN:       "DEMO" + hex[:13],
			Name:      "Demo " + model,
			Model:     model,
			ModelYear: feed.source.ModelYear,
			Make:      "Rivian",
			PackKWh:   feed.source.PackKWh,
		},
		allowed:   feed.allow == nil || feed.allow(uid),
		authReady: make(chan struct{}),
	}
}

// Login accepts any non-empty email and password; there is no MFA leg.
func (c *ReplayClient) Login(_ context.Context, cr Credentials) error {
	if !c.allowed {
		return ErrReplayNotAllowed
	}
	email := strings.TrimSpace(cr.Email)
	if email == "" || cr.Password == "" {
		return errors.New("replay: email and password required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.email = email
	c.authenticatedAt = time.Now().UTC()
	c.signalAuthReadyLocked()
	return nil
}

// Logout clears the session.
func (c *ReplayClient) Logout() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.email = ""
	c.authenticatedAt = time.Time{}
	c.authReady = make(chan struct{})
}

func (c *ReplayClient) Authenticated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.email != ""
}

func (c *ReplayClient) MFAPending() bool { return false }

func (c *ReplayClient) Email() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.email
}

func (c *ReplayClient) NeedsReauth() (bool, string) { return false, "" }

func (c *ReplayClient) Snapshot() Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.email == "" {
		return Session{}
	}
	return Session{Email: c.email, UserSessionToken: replaySessionToken, AuthenticatedAt: c.authenticatedAt}
}

// Restore re-authenticates from a replay Snapshot and ignores any other
// session (notably a real Rivian one persisted before the install
// switched to replay mode).
func (c *ReplayClient) Restore(s Session) {
	if s.UserSessionToken != replaySessionToken || !c.allowed {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.email = s.Email
	c.authenticatedAt = s.AuthenticatedAt
	c.signalAuthReadyLocked()
}

func (c *ReplayClient) AuthReady() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authReady
}

func (c *ReplayClient) signalAuthReadyLocked() {
	select {
	case <-c.authReady:
	default:
		close(c.authReady)
	}
}

// Vehicles returns the user's demo vehicle.
func (c *ReplayClient) Vehicles(context.Context) ([]Vehicle, error) {
	if !c.Authenticated() {
		return nil, ErrNotAuthenticated
	}
	return []Vehicle{c.vehicle}, nil
}

// State returns what the demo vehicle is "reporting" right now — the
// REST-fallback read for a replica that doesn't own its lease.
func (c *ReplayClient) State(ctx context.Context, vehicleID string) (*State, error) {
	if !c.Authenticated() {
		return nil, ErrNotAuthenticated
	}
	if vehicleID != c.vehicle.ID {
		return nil, ErrVehicleNotFound
	}
	st, err := c.feed.latest(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, ErrVehicleNotFound
	}
	st.VehicleID = vehicleID
	return st, nil
}

// Stream implements FrameSource for the demo vehicle only. Any other id
// — e.g. a real vehicle the same user owns, including the corpus vehicle
// itself — gets ErrVehicleNotFound, so replay can never write into it.
func (c *ReplayClient) Stream(ctx context.Context, vehicleID string, emit func(st State, catchingUp bool)) error {
	if vehicleID != c.vehicle.ID {
		return ErrVehicleNotFound
	}
	return c.feed.Stream(ctx, vehicleID, emit)
}

var (
	_ Account     = (*ReplayClient)(nil)
	_ Client      = (*ReplayClient)(nil)
	_ FrameSource = (*ReplayClient)(nil)
)

// replayAccountRegistry is the RIVIAN_CLIENT=replay registry: one
// *ReplayClient per user over a shared feed.
type replayAccountRegistry struct {
	feed    *ReplayFeed
	planner TripPlanner

	mu    sync.RWMutex
	users map[uuid.UUID]*ReplayClient
}

// NewReplayAccountRegistry returns a registry handing each user a
// ReplayClient over feed.
func NewReplayAccountRegistry(feed *ReplayFeed, planner TripPlanner) AccountRegistry {
	return &replayAccountRegistry{feed: feed, planner: planner, users: make(map[uuid.UUID]*ReplayClient)}
}

func (r *replayAccountRegistry) For(uid uuid.UUID) Account {
	r.mu.RLock()
	c, ok := r.users[uid]
	r.mu.RUnlock()
	if ok {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.users[uid]; ok {
		return c
	}
	c = NewReplayClient(r.feed, uid)
	c.planner = r.planner
	r.users[uid] = c
	return c
}

func (r *replayAccountRegistry) Loaded() []uuid.UUID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]uuid.UUID, 0, len(r.users))
	for uid := range r.users {
		out = append(out, uid)
	}
	return out
}

// ErrNoPlanner means this replay install has no trip planner configured.
var ErrNoPlanner = errors.New("rivian: no trip planner on this install")

// PlanTrip implements TripPlanner for the replay client from its planner: no
// network, no Rivian. Without one it answers ErrNoPlanner.
func (c *ReplayClient) PlanTrip(ctx context.Context, in PlanTripInput) (*TripPlan, error) {
	if c.planner == nil {
		return nil, ErrNoPlanner
	}
	return c.planner.PlanTrip(ctx, in)
}

var _ TripPlanner = (*ReplayClient)(nil)

// FirstPlanner tries each planner in order and returns the first answer that
// is not ErrNoFixture (so a stored plan is used when one matches and the next
// planner otherwise). Any other error ends the search.
type FirstPlanner []TripPlanner

// PlanTrip implements TripPlanner.
func (f FirstPlanner) PlanTrip(ctx context.Context, in PlanTripInput) (*TripPlan, error) {
	for _, p := range f {
		if p == nil {
			continue
		}
		plan, err := p.PlanTrip(ctx, in)
		if errors.Is(err, ErrNoFixture) {
			continue
		}
		return plan, err
	}
	return nil, ErrNoFixture
}
