package weather

import (
	"context"
	"sync"
	"time"
)

// MemCache is a small in-process LRU for FetchHour results, keyed by
// (coarsened lat, coarsened lon, UTC hour). Trip-planner plans
// frequently revisit the same geographic hour-buckets across
// successive replan-on-edit cycles; this collapses those into a
// single upstream call.
//
// Entries expire after TTL — relevant for the forecast endpoint,
// whose values shift as the underlying ECMWF run refreshes. Capacity
// is a soft cap: when exceeded the cache evicts the oldest entries
// on the next insert (sorted by insertedAt). The drive-weather
// `Cache` type is unrelated — that one is Postgres-backed and keyed
// per-drive.
//
// Headwind is NOT cached. The upstream snapshot's wind speed + dir
// are the expensive bits to fetch; headwind is recomputed on every
// read from the caller's trip bearing so two plans heading different
// directions through the same hour-bucket share the same row.
//
// Safe for concurrent use; nil receiver is a no-op (FetchHourCached
// just calls through to the client).
type MemCache struct {
	mu      sync.Mutex
	entries map[memKey]memEntry
	ttl     time.Duration
	max     int
}

type memKey struct {
	lat, lon float64
	hour     time.Time
}

type memEntry struct {
	snap      *Snapshot
	insertedAt time.Time
}

// NewMemCache builds a MemCache with the given TTL and capacity.
// A zero or negative ttl falls back to 15 minutes (the natural
// freshness of Open-Meteo forecast data — ECMWF reruns roughly
// quarter-hourly). A zero or negative max falls back to 1024.
func NewMemCache(ttl time.Duration, max int) *MemCache {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if max <= 0 {
		max = 1024
	}
	return &MemCache{
		entries: make(map[memKey]memEntry),
		ttl:     ttl,
		max:     max,
	}
}

// FetchHourCached returns the snapshot for (lat, lon, at), preferring
// the cache. On miss it calls client.FetchHour and inserts. Headwind
// is recomputed from the cached wind fields on every read using
// tripBearingDeg, so callers with different bearings can share the
// same cached upstream row.
func (m *MemCache) FetchHourCached(
	ctx context.Context,
	client *Client,
	lat, lon float64,
	at time.Time,
	tripBearingDeg float64,
	hasBearing bool,
) (*Snapshot, time.Time, error) {
	clat, clon := Coarsen(lat, lon)
	hour := at.UTC().Truncate(time.Hour)
	key := memKey{lat: clat, lon: clon, hour: hour}

	if m != nil {
		if snap, ok := m.get(key); ok {
			return withHeadwind(snap, tripBearingDeg, hasBearing), hour, nil
		}
	}
	snap, gotHour, err := client.FetchHour(ctx, lat, lon, at, tripBearingDeg, hasBearing)
	if err != nil {
		return nil, gotHour, err
	}
	if m != nil && snap != nil {
		// Cache a copy with headwind cleared so subsequent reads with
		// a different bearing recompute correctly. A nil snap (which
		// FetchHour shouldn't return on a non-error path, but defend
		// anyway) is skipped.
		bare := *snap
		bare.HeadwindKPH = 0
		bare.HasHeadwind = false
		m.put(key, &bare)
	}
	return snap, gotHour, nil
}

func (m *MemCache) get(k memKey) (*Snapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[k]
	if !ok {
		return nil, false
	}
	if time.Since(e.insertedAt) > m.ttl {
		delete(m.entries, k)
		return nil, false
	}
	// Return a copy so the caller can mutate (HeadwindKPH) without
	// corrupting the cache.
	cp := *e.snap
	return &cp, true
}

func (m *MemCache) put(k memKey, s *Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) >= m.max {
		// Evict the oldest entry. With max=1024 and trip planner
		// access patterns this is amortised O(1); upgrading to a
		// real LRU costs complexity that doesn't pay off here.
		var oldestKey memKey
		var oldestAt time.Time
		first := true
		for k, e := range m.entries {
			if first || e.insertedAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = e.insertedAt
				first = false
			}
		}
		delete(m.entries, oldestKey)
	}
	m.entries[k] = memEntry{snap: s, insertedAt: time.Now()}
}

// Len returns the number of live entries. Exposed for tests + the
// /metrics endpoint if anyone wants to wire a gauge.
func (m *MemCache) Len() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// withHeadwind returns a copy of s with HeadwindKPH filled in for the
// given bearing. A snap with no wind data stays as-is. The function
// guards against a nil snap.
func withHeadwind(s *Snapshot, bearing float64, hasBearing bool) *Snapshot {
	if s == nil {
		return nil
	}
	cp := *s
	if hasBearing && cp.HasWind {
		cp.HeadwindKPH = Headwind(cp.WindKPH, cp.WindDirDeg, bearing)
		cp.HasHeadwind = true
	}
	return &cp
}
