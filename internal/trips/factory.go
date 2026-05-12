package trips

import (
	"database/sql"
	"sync"

	"github.com/google/uuid"
)

// Factory hands out per-user *Store instances over a shared pool.
// Mirrors the push.Factory shape — the cache is just a routing
// convenience, the isolation lives in the (user_id = $1) predicates
// inside Store.
type Factory struct {
	db *sql.DB

	mu    sync.RWMutex
	cache map[uuid.UUID]*Store
}

// NewFactory wraps the shared pool.
func NewFactory(d *sql.DB) *Factory {
	return &Factory{
		db:    d,
		cache: make(map[uuid.UUID]*Store),
	}
}

// For returns the *Store for userID, constructing one on first sight.
// Returns nil when the factory or userID is zero; handlers should
// treat that as "not authenticated, refuse the request".
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
	s, err := OpenStore(f.db, userID)
	if err != nil {
		return nil
	}
	f.cache[userID] = s
	return s
}
