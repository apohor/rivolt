package api

import (
	"sync"
	"time"
)

// ttlCache is a small per-process cache of computed values: entries live
// for ttl, at most max are kept, and concurrent loads of one key share a
// single computation (so expiry under load costs one query, not N).
// Errors are never cached. When full, expired entries are dropped first;
// if none are, the new value is returned without being stored.
type ttlCache[V any] struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu       sync.Mutex
	entries  map[string]ttlEntry[V]
	inflight map[string]*ttlCall[V]
}

type ttlEntry[V any] struct {
	v       V
	expires time.Time
}

type ttlCall[V any] struct {
	done chan struct{}
	v    V
	err  error
}

func newTTLCache[V any](ttl time.Duration, max int) *ttlCache[V] {
	return &ttlCache[V]{
		ttl: ttl, max: max, now: time.Now,
		entries: map[string]ttlEntry[V]{}, inflight: map[string]*ttlCall[V]{},
	}
}

// get returns the cached value for key, or runs load once for all
// concurrent callers and caches a successful result.
func (c *ttlCache[V]) get(key string, load func() (V, error)) (V, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.v, nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.v, call.err
	}
	call := &ttlCall[V]{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	call.v, call.err = load()

	c.mu.Lock()
	delete(c.inflight, key)
	if call.err == nil {
		now := c.now()
		if len(c.entries) >= c.max {
			for k, e := range c.entries {
				if !now.Before(e.expires) {
					delete(c.entries, k)
				}
			}
		}
		if _, exists := c.entries[key]; exists || len(c.entries) < c.max {
			c.entries[key] = ttlEntry[V]{v: call.v, expires: now.Add(c.ttl)}
		}
	}
	c.mu.Unlock()
	close(call.done)
	return call.v, call.err
}
