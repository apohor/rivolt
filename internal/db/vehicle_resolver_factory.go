package db

import (
	"database/sql"
	"sync"

	"github.com/google/uuid"
)

// VehicleResolverFactory hands out per-user *VehicleResolver
// instances. Each user gets one resolver, cached for the process
// lifetime — the resolver itself caches (rivianID → internal UUID)
// in-memory so the recorder hot path doesn't round-trip Postgres on
// every sample, and per-user instances mean User A's resolver can
// never accidentally upsert a row attributed to User B.
//
// Safe for concurrent use.
type VehicleResolverFactory struct {
	db *sql.DB

	mu    sync.RWMutex
	cache map[uuid.UUID]*VehicleResolver
}

// NewVehicleResolverFactory builds a factory around the shared pool.
// The factory does not own the pool; main.go closes it.
func NewVehicleResolverFactory(db *sql.DB) *VehicleResolverFactory {
	return &VehicleResolverFactory{
		db:    db,
		cache: make(map[uuid.UUID]*VehicleResolver),
	}
}

// For returns the resolver bound to userID, constructing one on
// first sight. Returns nil when userID is the zero UUID.
func (f *VehicleResolverFactory) For(userID uuid.UUID) *VehicleResolver {
	if f == nil || userID == uuid.Nil {
		return nil
	}
	f.mu.RLock()
	r, ok := f.cache[userID]
	f.mu.RUnlock()
	if ok {
		return r
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.cache[userID]; ok {
		return r
	}
	r = NewVehicleResolver(f.db, userID)
	f.cache[userID] = r
	return r
}

// DB exposes the underlying pool for callers that need raw SQL
// against user-scoped tables (e.g. ownership probes). The factory
// retains pool ownership; do not close.
func (f *VehicleResolverFactory) DB() *sql.DB {
	if f == nil {
		return nil
	}
	return f.db
}
