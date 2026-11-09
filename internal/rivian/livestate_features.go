package rivian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// The takeover critical path (zero-impact deploys, G1b) used to wait for Rivian's SupportedFeatures call before the first subscribe, once per
// vehicle. What a truck supports almost never changes, so the pod that probes it leaves the answer in the shared store; the next owner reads
// it, subscribes at once and refreshes it in the background. The store is optional on a LiveStateStore (the type assertion below), so a store
// without it behaves as before.

// featuresTTL is how long a probed capability map is trusted for the subscribe decision. It is refreshed in the background on every start, so
// this only bounds how long a map no pod has refreshed survives.
const featuresTTL = 24 * time.Hour

// featuresRefreshAge is how old a stored map must be before a start that used it asks Rivian again in the background. A fresh map makes no
// call at all, so a roll does not become one SupportedFeatures call per vehicle (the G7 burst) moved off the critical path.
const featuresRefreshAge = 6 * time.Hour

// storedFeatures is what sits under the key: the map and when a pod read it from Rivian.
type storedFeatures struct {
	At    time.Time         `json:"at"`
	Feats map[string]string `json:"f"`
}

// FeaturesStore keeps a vehicle's SupportedFeatures answer between pods, with the time Rivian was asked.
type FeaturesStore interface {
	GetFeatures(ctx context.Context, vehicleID string) (feats map[string]string, readAt time.Time, found bool, err error)
	PutFeatures(ctx context.Context, vehicleID string, feats map[string]string, ttl time.Duration) error
}

func (s *RedisLiveStateStore) featuresKey(vehicleID string) string {
	return s.key(vehicleID) + ":features"
}

func (s *RedisLiveStateStore) GetFeatures(ctx context.Context, vehicleID string) (map[string]string, time.Time, bool, error) {
	if s == nil || s.rdb == nil {
		return nil, time.Time{}, false, nil
	}
	raw, err := s.rdb.Get(ctx, s.featuresKey(vehicleID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("get features: %w", err)
	}
	var st storedFeatures
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, time.Time{}, false, fmt.Errorf("decode features: %w", err)
	}
	return st.Feats, st.At, true, nil
}

// PutFeatures is a plain SET, not the epoch CAS the snapshots use: it is a fact about the truck, any pod that read it from Rivian writes the
// same thing, and a stale write is replaced by the next start's refresh.
func (s *RedisLiveStateStore) PutFeatures(ctx context.Context, vehicleID string, feats map[string]string, ttl time.Duration) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	blob, err := json.Marshal(storedFeatures{At: time.Now().UTC(), Feats: feats})
	if err != nil {
		return fmt.Errorf("encode features: %w", err)
	}
	return s.rdb.Set(ctx, s.featuresKey(vehicleID), blob, ttl).Err()
}

func (g *GatedLiveStateStore) GetFeatures(ctx context.Context, vehicleID string) (map[string]string, time.Time, bool, error) {
	fs, ok := g.inner.(FeaturesStore)
	if !ok || !g.usable() {
		return nil, time.Time{}, false, nil
	}
	var feats map[string]string
	var at time.Time
	var found bool
	err := g.call(ctx, func(c context.Context) error {
		var e error
		feats, at, found, e = fs.GetFeatures(c, vehicleID)
		return e
	})
	if err != nil {
		return nil, time.Time{}, false, err
	}
	return feats, at, found, nil
}

func (g *GatedLiveStateStore) PutFeatures(ctx context.Context, vehicleID string, feats map[string]string, ttl time.Duration) error {
	fs, ok := g.inner.(FeaturesStore)
	if !ok || !g.usable() {
		return nil
	}
	return g.call(ctx, func(c context.Context) error { return fs.PutFeatures(c, vehicleID, feats, ttl) })
}

func (s *memLiveStateStore) GetFeatures(_ context.Context, vehicleID string) (map[string]string, time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.feats[vehicleID]
	if !ok {
		return nil, time.Time{}, false, nil
	}
	cp := make(map[string]string, len(f.m))
	for k, v := range f.m {
		cp[k] = v
	}
	return cp, f.at, true, nil
}

func (s *memLiveStateStore) PutFeatures(_ context.Context, vehicleID string, feats map[string]string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.feats == nil {
		s.feats = map[string]memFeatures{}
	}
	cp := make(map[string]string, len(feats))
	for k, v := range feats {
		cp[k] = v
	}
	s.feats[vehicleID] = memFeatures{m: cp, at: time.Now()}
	return nil
}

type memFeatures struct {
	m  map[string]string
	at time.Time
}
