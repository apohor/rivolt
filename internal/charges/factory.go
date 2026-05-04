package charges

import (
	"database/sql"
	"sync"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// Factory hands out per-user *Store instances. See drives.Factory
// for the rationale; this is the same shape, scoped to the charges
// table.
type Factory struct {
	db        *sql.DB
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
	s, err := OpenStore(f.db, userID, resolver)
	if err != nil {
		return nil
	}
	f.cache[userID] = s
	return s
}
