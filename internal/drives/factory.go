package drives

import (
	"database/sql"
	"sync"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// Factory hands out per-user *Store instances. Each Store is a thin
// wrapper around (db, userID, vehicleResolver) — building one per
// request is cheap, but the Factory caches them anyway so the
// vehicle-resolver in-memory cache stays warm across calls.
//
// Safe for concurrent use.
type Factory struct {
	db        *sql.DB
	resolvers *db.VehicleResolverFactory

	mu    sync.RWMutex
	cache map[uuid.UUID]*Store
}

// NewFactory wraps the shared pool + resolver factory. main.go owns
// the pool; the factory does not close it.
func NewFactory(d *sql.DB, resolvers *db.VehicleResolverFactory) *Factory {
	return &Factory{
		db:        d,
		resolvers: resolvers,
		cache:     make(map[uuid.UUID]*Store),
	}
}

// For returns the *Store for userID, constructing one on first
// sight. Returns nil when userID is the zero UUID.
func (f *Factory) For(userID uuid.UUID) *Store {
	if f == nil || userID == uuid.Nil {
		return nil
	}
	f.mu.RLock()
	s, ok := f.cache[userID]
	f.mu.RUnlock()
	if ok {
		return s
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.cache[userID]; ok {
		return s
	}
	resolver := f.resolvers.For(userID)
	s, err := OpenStore(f.db, userID, resolver)
	if err != nil {
		// OpenStore only errors on nil-arg paths, all of which we
		// guard against above. Falling back to nil keeps the
		// per-user surface defensive without papering over a real
		// mis-wiring in main.go.
		return nil
	}
	f.cache[userID] = s
	return s
}
