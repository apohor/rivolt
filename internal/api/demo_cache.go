package api

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// demoResponseCache keeps the 200 body of a heavy anonymous read for a
// short TTL, keyed by path and query, so a burst of identical requests
// reaches the database at most once per window. It holds at most max
// entries (the demo serves one vehicle, so a handful at most); when full
// an unknown key is served uncached rather than evicting.
type demoResponseCache struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu      sync.Mutex
	entries map[string]demoCacheEntry
}

type demoCacheEntry struct {
	at     time.Time
	header http.Header
	body   []byte
}

func newDemoResponseCache(ttl time.Duration, max int) *demoResponseCache {
	return &demoResponseCache{ttl: ttl, max: max, now: time.Now, entries: map[string]demoCacheEntry{}}
}

func (c *demoResponseCache) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		key := r.URL.Path + "?" + r.URL.RawQuery
		c.mu.Lock()
		e, ok := c.entries[key]
		c.mu.Unlock()
		if ok && c.now().Sub(e.at) < c.ttl {
			for k, v := range e.header {
				w.Header()[k] = v
			}
			w.Header().Set("X-Rivolt-Demo-Cache", "hit")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(e.body)
			return
		}
		rec := &demoCacheRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status != http.StatusOK || rec.over {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, exists := c.entries[key]; exists || len(c.entries) < c.max {
			h := http.Header{}
			for _, k := range []string{"Content-Type", demoHeader} {
				if v := w.Header().Values(k); len(v) > 0 {
					h[k] = v
				}
			}
			c.entries[key] = demoCacheEntry{at: c.now(), header: h, body: rec.buf.Bytes()}
		}
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
