package api

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// demoResponseCache keeps the 200 body of a heavy anonymous read for a
// short TTL, keyed by path and query, so a burst of identical requests
// reaches the database at most once per window. Concurrent requests for
// a key that is being computed wait for that one computation instead of
// starting their own (so expiry under load costs one query, not N). It
// holds at most max entries; when full a new key is served uncached
// rather than evicting.
type demoResponseCache struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu       sync.Mutex
	entries  map[string]demoCacheEntry
	inflight map[string]*demoCacheCall
}

type demoCacheEntry struct {
	at     time.Time
	header http.Header
	body   []byte
}

// demoCacheCall is one in-progress computation other requests wait on.
type demoCacheCall struct {
	done chan struct{}
	ent  *demoCacheEntry // nil when the result was not cacheable
}

func newDemoResponseCache(ttl time.Duration, max int) *demoResponseCache {
	return &demoResponseCache{
		ttl: ttl, max: max, now: time.Now,
		entries: map[string]demoCacheEntry{}, inflight: map[string]*demoCacheCall{},
	}
}

func (c *demoResponseCache) replay(w http.ResponseWriter, e demoCacheEntry) {
	for k, v := range e.header {
		w.Header()[k] = v
	}
	w.Header().Set("X-Rivolt-Demo-Cache", "hit")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(e.body)
}

func (c *demoResponseCache) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		key := r.URL.Path + "?" + r.URL.RawQuery
		c.mu.Lock()
		if e, ok := c.entries[key]; ok && c.now().Sub(e.at) < c.ttl {
			c.mu.Unlock()
			c.replay(w, e)
			return
		}
		if call, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			select {
			case <-call.done:
			case <-r.Context().Done():
				return
			}
			if call.ent != nil {
				c.replay(w, *call.ent)
				return
			}
			next.ServeHTTP(w, r) // the leader's result wasn't cacheable
			return
		}
		call := &demoCacheCall{done: make(chan struct{})}
		c.inflight[key] = call
		c.mu.Unlock()

		rec := &demoCacheRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
			close(call.done)
		}()
		next.ServeHTTP(rec, r)
		if rec.status != http.StatusOK || rec.over {
			return
		}
		h := http.Header{}
		for _, k := range []string{"Content-Type", demoHeader} {
			if v := w.Header().Values(k); len(v) > 0 {
				h[k] = v
			}
		}
		e := demoCacheEntry{at: c.now(), header: h, body: rec.buf.Bytes()}
		c.mu.Lock()
		if _, exists := c.entries[key]; exists || len(c.entries) < c.max {
			c.entries[key] = e
		}
		c.mu.Unlock()
		call.ent = &e
	})
}

// demoCacheMaxBody bounds what is kept per entry.
const demoCacheMaxBody = 1 << 20

type demoCacheRecorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
	over   bool
}

func (r *demoCacheRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *demoCacheRecorder) Write(b []byte) (int, error) {
	if r.buf.Len()+len(b) > demoCacheMaxBody {
		r.over = true
	} else {
		r.buf.Write(b)
	}
	return r.ResponseWriter.Write(b)
}
