package samples

import (
	"database/sql"
	"sync"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// Factory hands out per-user *Store instances. See drives.Factory
// for the rationale.
type Factory struct {
	db        *sql.DB
	sys       *sql.DB // the system pool for partition creation (Store.sys); nil means db
	resolvers *db.VehicleResolverFactory

	mu    sync.RWMutex
	cache map[uuid.UUID]*Store
}

// NewFactory wraps the shared pool + resolver factory.
func NewFactory(d *sql.DB, resolvers *db.VehicleResolverFactory) *Factory {
	return &Factory{
		db:        d,
		resolvers: resolvers,
		cache:     make(map[uuid.UUID]*Store),
	}
}

// NewFactoryWithSystem is NewFactory for request handling under row-level security: d is the request pool and sys the owner-role
// pool the stores use to create a month's partition.
func NewFactoryWithSystem(d, sys *sql.DB, resolvers *db.VehicleResolverFactory) *Factory {
	f := NewFactory(d, resolvers)
	f.sys = sys
	return f
}

// For returns the *Store for userID, constructing one on first sight.
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
	s, err := openStore(f.db, f.sys, userID, resolver)
	if err != nil {
		return nil
	}
	f.cache[userID] = s
	return s
}
