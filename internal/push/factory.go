package push

import (
	"database/sql"
	"sync"

	"github.com/google/uuid"
)

// Factory hands out per-user *Store instances. push.Store wraps
// (db, userID); the per-user split is for routing, not for
// per-user-VAPID — the VAPID keypair is global (push_vapid.id = 1)
// and any *Store can read it.
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

// DB exposes the shared pool. Used by main.go to load/generate the
// global VAPID keypair, which lives at push_vapid.id=1 and isn't
// scoped to any user.
func (f *Factory) DB() *sql.DB {
	if f == nil {
		return nil
	}
	return f.db
}
