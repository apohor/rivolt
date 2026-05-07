package rivian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
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
	ID         string       `json:"id"`
	Number     int64        `json:"number"`
	StartedAt  time.Time    `json:"started_at"`
	StartSoC   float64      `json:"start_soc"`
	StartOdoMi float64      `json:"start_odo_mi"`
	StartLat   float64      `json:"start_lat"`
	StartLon   float64      `json:"start_lon"`
	MaxSpeed   float64      `json:"max_speed"`
	SumSpeed   float64      `json:"sum_speed"`
	SpeedN     int          `json:"speed_n"`
	EndAt      time.Time    `json:"end_at"`
	EndSoC     float64      `json:"end_soc"`
	EndOdoMi   float64      `json:"end_odo_mi"`
	EndLat     float64      `json:"end_lat"`
	EndLon     float64      `json:"end_lon"`
	Path       [][2]float64 `json:"path,omitempty"`
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
}

// LiveStateTTL is how long a snapshot lingers in Redis after the last
// upsert. Sized to ~2× liveDriveMaxGap so a graceful pod-handoff
// window comfortably covers WS reconnect cadence; the recorder's own
// stale-session guard at liveDriveMaxGap will reject any rehydrated
// state whose endAt is clearly out of date.
const LiveStateTTL = 2 * time.Hour

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
			Path: path,
		}
	}
	if s.charge != nil {
		c := s.charge
		snap.Charge = &LiveChargeSnapshot{
			ID: c.id, Number: c.number, StartedAt: c.startedAt,
			StartSoC: c.startSoC, Lat: c.lat, Lon: c.lon,
			MaxPower: c.maxPower,
			EndAt:    c.endAt, EndSoC: c.endSoC, FinalState: c.finalState,
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
			path: path,
		}
	}
	if snap.Charge != nil {
		c := snap.Charge
		s.charge = &liveCharge{
			id: c.ID, number: c.Number, startedAt: c.StartedAt,
			startSoC: c.StartSoC, lat: c.Lat, lon: c.Lon,
			maxPower: c.MaxPower,
			endAt:    c.EndAt, endSoC: c.EndSoC, finalState: c.FinalState,
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
// *redis.Client constructed at boot for the rate limiter.
func NewRedisLiveStateStore(rdb *redis.Client, userID string) *RedisLiveStateStore {
	return &RedisLiveStateStore{
		rdb:       rdb,
		keyPrefix: "rivolt:livestate",
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
	return s.rdb.Set(ctx, s.key(vehicleID), blob, ttl).Err()
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
	return s.rdb.Del(ctx, s.key(vehicleID)).Err()
}

// memLiveStateStore is a test fake. Concurrency-safe.
type memLiveStateStore struct {
	mu    sync.Mutex
	items map[string]LiveStateSnapshot
}

func newMemLiveStateStore() *memLiveStateStore {
	return &memLiveStateStore{items: map[string]LiveStateSnapshot{}}
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
