package settings

import (
	"database/sql"
	"sync"

	"github.com/google/uuid"
)

// Factory hands out per-user *Store instances. The settings store
// is just (db, userID), so a per-user instance is essentially free —
// caching is purely so callers reuse the same pointer across requests
// and don't churn the heap on every handler hit.
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
