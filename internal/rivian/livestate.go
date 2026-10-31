package rivian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/fence"
)

// LiveStateStore persists per-vehicle in-flight session accumulators
// across pod restarts and lease handoffs. The recorder upserts the
// snapshot after every WS-driven frame and rehydrates on first contact
// for a vehicle, so a freshly elected lease owner picks up the open
// drive instead of opening a new one and fragmenting the trip.
//
// Save/Load are best-effort: storage failures degrade to fragmentation
// but must never break the recorder hot path. Implementations must be
// safe for concurrent use across vehicles.
type LiveStateStore interface {
	Save(ctx context.Context, vehicleID string, snap LiveStateSnapshot, ttl time.Duration) error
	Load(ctx context.Context, vehicleID string) (LiveStateSnapshot, bool, error)
	Delete(ctx context.Context, vehicleID string) error

	// PutState publishes the vehicle's latest merged live State so a
	// replica that doesn't own the WS lease can serve a complete
	// /api/state (including subscription-only / Parallax-only fields)
	// instead of a REST fallback that omits them. Short-TTL and
	// refreshed by the owner on a timer, so a dead owner's snapshot
	// ages out and readers fall back to REST.
	PutState(ctx context.Context, vehicleID string, st *State, ttl time.Duration) error
	// GetState returns the latest published live State for the vehicle,
	// or (nil, nil) when none exists (the normal case on the owner pod,
	// which never reads its own snapshot).
	GetState(ctx context.Context, vehicleID string) (*State, error)
}

// LiveStateSnapshot is the wire format for a vehicle's open
// drive/charge accumulator. Fields mirror liveSessions / liveDrive /
// liveCharge with explicit JSON shapes so the format stays legible and
// is stable across process versions.
type LiveStateSnapshot struct {
	DriveCounter  int64               `json:"drive_counter"`
	ChargeCounter int64               `json:"charge_counter"`
	Drive         *LiveDriveSnapshot  `json:"drive,omitempty"`
	Charge        *LiveChargeSnapshot `json:"charge,omitempty"`
}

// LiveDriveSnapshot mirrors liveDrive.
type LiveDriveSnapshot struct {
	ID         string    `json:"id"`
	Number     int64     `json:"number"`
	StartedAt  time.Time `json:"started_at"`
	StartSoC   float64   `json:"start_soc"`
	StartOdoMi float64   `json:"start_odo_mi"`
	StartLat   float64   `json:"start_lat"`
	StartLon   float64   `json:"start_lon"`
	MaxSpeed   float64   `json:"max_speed"`
	SumSpeed   float64   `json:"sum_speed"`
	SpeedN     int       `json:"speed_n"`
	EndAt      time.Time `json:"end_at"`
	EndSoC     float64   `json:"end_soc"`
	EndOdoMi   float64   `json:"end_odo_mi"`
	EndLat     float64   `json:"end_lat"`
	EndLon     float64   `json:"end_lon"`
	// The trailer connector's integral over the drive so far (trailer.go), so a restart keeps it.
	TrailerPresentSec float64      `json:"trailer_present_sec,omitempty"`
	TrailerKnownSec   float64      `json:"trailer_known_sec,omitempty"`
	TrailerLast       string       `json:"trailer_last,omitempty"`
	TrailerAt         time.Time    `json:"trailer_at,omitempty"`
	Path              [][2]float64 `json:"path,omitempty"`
	// Distinct location fixes seen so far (recorder.go liveDrive.locFixes), so a handover keeps the count.
	LocFixes int    `json:"loc_fixes,omitempty"`
	LocKey   string `json:"loc_key,omitempty"`
}

// LiveChargeSnapshot mirrors liveCharge.
type LiveChargeSnapshot struct {
	ID         string    `json:"id"`
	Number     int64     `json:"number"`
	StartedAt  time.Time `json:"started_at"`
	StartSoC   float64   `json:"start_soc"`
	Lat        float64   `json:"lat"`
	Lon        float64   `json:"lon"`
	MaxPower   float64   `json:"max_power"`
	EndAt      time.Time `json:"end_at"`
	EndSoC     float64   `json:"end_soc"`
	FinalState string    `json:"final_state"`
	// LastMeaningfulAt anchors the stale-session guard across restarts.
	// Older snapshots omit it; restore falls back to EndAt then.
	LastMeaningfulAt time.Time `json:"last_meaningful_at"`
	// LastProgressAt anchors it for a charge with no plug indicator;
	// zero (older snapshots) restores as LastMeaningfulAt.
	LastProgressAt time.Time `json:"last_progress_at,omitempty"`
	// ActiveSeconds carries the accumulated real charging time across a
	// pod handoff so the session duration doesn't reset when a peer
	// takes over mid-charge.
	ActiveSeconds float64 `json:"active_seconds"`
	// EnergyIntKWh carries the energy integral across a handoff. Without it the peer's integral restarted at 0 and
	// the charge ended with only the energy since the last handoff. Snapshots an older pod wrote lack it (0).
	EnergyIntKWh float64 `json:"energy_int_kwh,omitempty"`
}

// LiveStateTTL is how long a snapshot lingers in Redis after the last
// upsert. Sized to ~2× liveDriveMaxGap so a graceful pod-handoff
// window comfortably covers WS reconnect cadence; the recorder's own
// stale-session guard at liveDriveMaxGap will reject any rehydrated
// state whose endAt is clearly out of date.
const LiveStateTTL = 2 * time.Hour

// LiveStateSnapshotTTL bounds how long a published live State snapshot
// lives in the shared store after the last refresh. Much shorter than
// LiveStateTTL because a snapshot is a live-view convenience, not
// durable session state: when the owning pod dies, the snapshot should
// age out within a couple of publish intervals so peer replicas fall
// back to REST rather than serving a stale frame indefinitely.
const LiveStateSnapshotTTL = 90 * time.Second

// snapshot freezes the current accumulator into a wire-safe value.
// Caller holds sessMu.
func (s *liveSessions) snapshot() LiveStateSnapshot {
	snap := LiveStateSnapshot{
		DriveCounter:  s.driveCounter,
		ChargeCounter: s.chargeCounter,
	}
	if s.drive != nil {
		d := s.drive
		var path [][2]float64
		if len(d.path) > 0 {
			path = make([][2]float64, len(d.path))
			copy(path, d.path)
		}
		snap.Drive = &LiveDriveSnapshot{
			ID: d.id, Number: d.number, StartedAt: d.startedAt,
			StartSoC: d.startSoC, StartOdoMi: d.startOdoMi,
			StartLat: d.startLat, StartLon: d.startLon,
			MaxSpeed: d.maxSpeed, SumSpeed: d.sumSpeed, SpeedN: d.speedN,
			EndAt: d.endAt, EndSoC: d.endSoC, EndOdoMi: d.endOdoMi,
			EndLat: d.endLat, EndLon: d.endLon,
			TrailerPresentSec: d.trailer.present.Seconds(), TrailerKnownSec: d.trailer.known.Seconds(),
			TrailerLast: d.trailer.last, TrailerAt: d.trailer.lastAt,
			Path: path, LocFixes: d.locFixes, LocKey: d.locKey,
		}
	}
	if s.charge != nil {
		c := s.charge
		snap.Charge = &LiveChargeSnapshot{
			ID: c.id, Number: c.number, StartedAt: c.startedAt,
			StartSoC: c.startSoC, Lat: c.lat, Lon: c.lon,
			MaxPower: c.maxPower,
			EndAt:    c.endAt, EndSoC: c.endSoC, FinalState: c.finalState,
			LastMeaningfulAt: c.lastMeaningfulAt,
			LastProgressAt:   c.lastProgressAt,
			ActiveSeconds:    c.activeSeconds,
			EnergyIntKWh:     c.energyIntKWh,
		}
	}
	return snap
}

// liveSessionsFromSnapshot rebuilds the in-memory accumulator from a
// stored snapshot. Returns a non-nil *liveSessions even on a zero
// snapshot so callers can use it directly.
func liveSessionsFromSnapshot(snap LiveStateSnapshot) *liveSessions {
	s := &liveSessions{
		driveCounter:  snap.DriveCounter,
		chargeCounter: snap.ChargeCounter,
	}
	if snap.Drive != nil {
		d := snap.Drive
		var path [][2]float64
		if len(d.Path) > 0 {
			path = make([][2]float64, len(d.Path))
			copy(path, d.Path)
		}
		s.drive = &liveDrive{
			id: d.ID, number: d.Number, startedAt: d.StartedAt,
			startSoC: d.StartSoC, startOdoMi: d.StartOdoMi,
			startLat: d.StartLat, startLon: d.StartLon,
			maxSpeed: d.MaxSpeed, sumSpeed: d.SumSpeed, speedN: d.SpeedN,
			endAt: d.EndAt, endSoC: d.EndSoC, endOdoMi: d.EndOdoMi,
			endLat: d.EndLat, endLon: d.EndLon,
			trailer: trailerAcc{
				present: time.Duration(d.TrailerPresentSec * float64(time.Second)),
				known:   time.Duration(d.TrailerKnownSec * float64(time.Second)),
				last:    d.TrailerLast, lastAt: d.TrailerAt,
			},
			path: path, locFixes: d.LocFixes, locKey: d.LocKey,
		}
	}
	if snap.Charge != nil {
		c := snap.Charge
		lma := c.LastMeaningfulAt
		if lma.IsZero() {
			lma = c.EndAt
		}
		lpa := c.LastProgressAt
		if lpa.IsZero() {
			lpa = lma
		}
		s.charge = &liveCharge{
			id: c.ID, number: c.Number, startedAt: c.StartedAt,
			startSoC: c.StartSoC, lat: c.Lat, lon: c.Lon,
			maxPower: c.MaxPower,
			endAt:    c.EndAt, endSoC: c.EndSoC, finalState: c.FinalState,
			lastMeaningfulAt: lma,
			lastProgressAt:   lpa,
			activeSeconds:    c.ActiveSeconds,
			energyIntKWh:     c.EnergyIntKWh,
			// The progress point is where this pod picks up.
			progressKWh:       c.EnergyIntKWh,
			progressActiveSec: c.ActiveSeconds,
		}
	}
	return s
}

// RedisLiveStateStore is the production LiveStateStore. Keys are
// scoped by user so the same Redis cluster is shared across tenants
// without cross-tenant reads.
type RedisLiveStateStore struct {
	rdb       *redis.Client
	keyPrefix string
	userID    string
}

// NewRedisLiveStateStore binds rdb to the given userID. Pass the same
// *redis.Client constructed at boot for the rate limiter. keyPrefix
// is the install-wide Redis prefix (typically "rivolt" for prod,
// "rivolt-preview" for the preview env) — the livestate suffix is
// appended internally so two environments sharing one Redis still
// own disjoint keyspaces.
func NewRedisLiveStateStore(rdb *redis.Client, userID, keyPrefix string) *RedisLiveStateStore {
	if keyPrefix == "" {
		keyPrefix = "rivolt"
	}
	return &RedisLiveStateStore{
		rdb:       rdb,
		keyPrefix: keyPrefix + ":livestate",
		userID:    userID,
	}
}

func (s *RedisLiveStateStore) key(vehicleID string) string {
	return fmt.Sprintf("%s:%s:%s", s.keyPrefix, s.userID, vehicleID)
}

// Save serialises snap and writes it under the per-user/vehicle key
// with an expiry. A zero ttl falls back to LiveStateTTL.
func (s *RedisLiveStateStore) Save(ctx context.Context, vehicleID string, snap LiveStateSnapshot, ttl time.Duration) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = LiveStateTTL
	}
	blob, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("encode livestate: %w", err)
	}
	return s.write(ctx, vehicleID, s.key(vehicleID), blob, ttl)
}

// epochTTL keeps a vehicle's highest-seen lease epoch beyond the longest data TTL, so a stale
// owner's write is still refused after the snapshot it would overwrite has expired.
const epochTTL = 3 * time.Hour

// epochKey holds the highest group-lease epoch that has written this vehicle's snapshots.
//
// Cluster note: the CAS scripts touch this key and the data key together, which needs one hash slot.
// The data keys carry no hash tag today, and giving them one would orphan every existing snapshot
// (rehydration after a deploy would find nothing) and change the purger's patterns, so the layout is
// left as it is. This Valkey is standalone; a move to a cluster must retag the livestate keys
// (<prefix>:livestate:{<uid>:<vehicle>}) in the same change, with a migration of the live ones.
func (s *RedisLiveStateStore) epochKey(vehicleID string) string { return s.key(vehicleID) + ":epoch" }

// writeScript writes a value only if the stored epoch is not above the writer's (a Lua CAS, so the
// check and the write are one atomic step in Redis): KEYS[1] the epoch key, KEYS[2] the data key;
// ARGV[1] the writer's epoch, ARGV[2] the value, ARGV[3] the data TTL in ms, ARGV[4] the epoch TTL
// in ms. Returns 1 when written, 0 when a newer owner has written.
//
// Needs a hash tag {...} on both keys if Valkey is ever clustered (see epochKey).
var writeScript = redis.NewScript(`
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local mine = tonumber(ARGV[1])
if cur > mine then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[4])
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
return 1`)

// deleteScript is writeScript for a delete (the same hash-tag note applies).
var deleteScript = redis.NewScript(`
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local mine = tonumber(ARGV[1])
if cur > mine then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('DEL', KEYS[2])
return 1`)

// write sets key to blob. With a fence token in the context (the owner under a group lease) it is
// the epoch CAS: a pod whose epoch is below the highest written is refused with fence.ErrFenced, so
// a paused or partitioned old owner cannot overwrite the new owner's snapshot. Without a token it is
// the plain SET, as before.
func (s *RedisLiveStateStore) write(ctx context.Context, vehicleID, key string, blob []byte, ttl time.Duration) error {
	tok, ok := fence.From(ctx)
	if !ok {
		return s.rdb.Set(ctx, key, blob, ttl).Err()
	}
	n, err := writeScript.Run(ctx, s.rdb, []string{s.epochKey(vehicleID), key},
		tok.Epoch, blob, ttl.Milliseconds(), epochTTL.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return fence.ErrFenced
	}
	return nil
}

// Load reads the snapshot for vehicleID. The bool is false (with nil
// error) when no snapshot exists — the normal first-contact case.
func (s *RedisLiveStateStore) Load(ctx context.Context, vehicleID string) (LiveStateSnapshot, bool, error) {
	if s == nil || s.rdb == nil {
		return LiveStateSnapshot{}, false, nil
	}
	raw, err := s.rdb.Get(ctx, s.key(vehicleID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return LiveStateSnapshot{}, false, nil
		}
		return LiveStateSnapshot{}, false, fmt.Errorf("get livestate: %w", err)
	}
	var snap LiveStateSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return LiveStateSnapshot{}, false, fmt.Errorf("decode livestate: %w", err)
	}
	return snap, true, nil
}

// Delete drops the snapshot for vehicleID. Used after a normal
// session close so a future P → D transition can't accidentally
// rehydrate a closed drive on top of the new one.
func (s *RedisLiveStateStore) Delete(ctx context.Context, vehicleID string) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	tok, ok := fence.From(ctx)
	if !ok {
		return s.rdb.Del(ctx, s.key(vehicleID)).Err()
	}
	n, err := deleteScript.Run(ctx, s.rdb, []string{s.epochKey(vehicleID), s.key(vehicleID)}, tok.Epoch, epochTTL.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return fence.ErrFenced
	}
	return nil
}

// stateKey scopes the live-State snapshot under a ":state" suffix so it
// never collides with the accumulator snapshot at s.key(vehicleID).
func (s *RedisLiveStateStore) stateKey(vehicleID string) string {
	return s.key(vehicleID) + ":state"
}

// PutState serialises st and writes it under the per-user/vehicle state
// key with a short expiry. A zero ttl falls back to LiveStateSnapshotTTL.
func (s *RedisLiveStateStore) PutState(ctx context.Context, vehicleID string, st *State, ttl time.Duration) error {
	if s == nil || s.rdb == nil || st == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = LiveStateSnapshotTTL
	}
	blob, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode live state: %w", err)
	}
	return s.write(ctx, vehicleID, s.stateKey(vehicleID), blob, ttl)
}

// GetState reads the published live State for vehicleID. Returns
// (nil, nil) when no snapshot exists (owner hasn't published, or it aged
// out) so callers cleanly fall through to their REST path.
func (s *RedisLiveStateStore) GetState(ctx context.Context, vehicleID string) (*State, error) {
	if s == nil || s.rdb == nil {
		return nil, nil
	}
	raw, err := s.rdb.Get(ctx, s.stateKey(vehicleID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("get live state: %w", err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decode live state: %w", err)
	}
	return &st, nil
}

// memLiveStateStore is a test fake. Concurrency-safe.
type memLiveStateStore struct {
	mu     sync.Mutex
	items  map[string]LiveStateSnapshot
	states map[string]State
}

func newMemLiveStateStore() *memLiveStateStore {
	return &memLiveStateStore{
		items:  map[string]LiveStateSnapshot{},
		states: map[string]State{},
	}
}

func (s *memLiveStateStore) PutState(_ context.Context, vehicleID string, st *State, _ time.Duration) error {
	if st == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[vehicleID] = *st
	return nil
}

func (s *memLiveStateStore) GetState(_ context.Context, vehicleID string) (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[vehicleID]
	if !ok {
		return nil, nil
	}
	cp := st
	return &cp, nil
}

func (s *memLiveStateStore) Save(_ context.Context, vehicleID string, snap LiveStateSnapshot, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[vehicleID] = snap
	return nil
}

func (s *memLiveStateStore) Load(_ context.Context, vehicleID string) (LiveStateSnapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.items[vehicleID]
	return snap, ok, nil
}

func (s *memLiveStateStore) Delete(_ context.Context, vehicleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, vehicleID)
	return nil
}
