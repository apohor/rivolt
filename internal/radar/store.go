package radar

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// MaxCycleAge is how old a cycle may be (by its nominal time) before its hours are treated as absent. Extended cycles come every 6 h and
	// appear about 3 h after their nominal time, so 9 h keeps one cycle usable through the whole gap until the next.
	MaxCycleAge = 9 * time.Hour
	// DefaultCacheBytes caps the decoded grids a pod holds (about 22 hours of CONUS).
	DefaultCacheBytes = 48 << 20

	manifestTTL = time.Minute
)

type manifest struct {
	Cycles []struct {
		Cycle     string    `json:"cycle"`
		CycleTime time.Time `json:"cycle_time"`
		Hours     []int     `json:"hours"`
	} `json:"cycles"`
	Path string `json:"path"`
}

// Store fetches forecast hours from the in-cluster radar service and keeps decoded grids in a size-capped LRU.
type Store struct {
	base  string
	hc    *http.Client
	now   func() time.Time
	limit int

	mu      sync.Mutex
	man     *manifest
	manAt   time.Time
	size    int
	lru     *list.List // of *entry, front = most recent
	entries map[string]*list.Element
	loading map[string]*load
}

type entry struct {
	key string
	g   *Grid
}

type load struct {
	done chan struct{}
	g    *Grid
	err  error
}

// NewStore returns a store reading from baseURL (for example http://radar.radar.svc), or nil when baseURL is empty (radar off).
func NewStore(baseURL string, hc *http.Client) *Store {
	if baseURL == "" {
		return nil
	}
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	return &Store{base: strings.TrimRight(baseURL, "/"), hc: hc, now: time.Now, limit: DefaultCacheBytes,
		lru: list.New(), entries: map[string]*list.Element{}, loading: map[string]*load{}}
}

func (s *Store) get(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("radar: %s: %s", path, resp.Status)
	}
	return resp.Body, nil
}

func (s *Store) manifest(ctx context.Context) (*manifest, error) {
	s.mu.Lock()
	if s.man != nil && s.now().Sub(s.manAt) < manifestTTL {
		m := s.man
		s.mu.Unlock()
		return m, nil
	}
	s.mu.Unlock()
	body, err := s.get(ctx, "/latest.json")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var m manifest
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("radar: manifest: %w", err)
	}
	s.mu.Lock()
	s.man, s.manAt = &m, s.now()
	s.mu.Unlock()
	return &m, nil
}

// grid returns the decoded grid for a cycle and forecast hour, from cache or the service. Concurrent callers for one key share a fetch.
func (s *Store) grid(ctx context.Context, cycle string, fh int) (*Grid, error) {
	key := fmt.Sprintf("%s/%03d", cycle, fh)
	s.mu.Lock()
	if el, ok := s.entries[key]; ok {
		s.lru.MoveToFront(el)
		g := el.Value.(*entry).g
		s.mu.Unlock()
		return g, nil
	}
	if l, ok := s.loading[key]; ok {
		s.mu.Unlock()
		select {
		case <-l.done:
			return l.g, l.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	l := &load{done: make(chan struct{})}
	s.loading[key] = l
	s.mu.Unlock()

	l.g, l.err = s.fetch(ctx, cycle, fh)

	s.mu.Lock()
	delete(s.loading, key)
	if l.err == nil {
		s.size += l.g.Bytes()
		s.entries[key] = s.lru.PushFront(&entry{key: key, g: l.g})
		for s.size > s.limit && s.lru.Len() > 1 {
			old := s.lru.Back()
			e := old.Value.(*entry)
			s.lru.Remove(old)
			delete(s.entries, e.key)
			s.size -= e.g.Bytes()
		}
	}
	s.mu.Unlock()
	close(l.done)
	return l.g, l.err
}

func (s *Store) fetch(ctx context.Context, cycle string, fh int) (*Grid, error) {
	body, err := s.get(ctx, fmt.Sprintf("/conus/%s/f%03d.bin.gz", cycle, fh))
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return Decode(body)
}

// CachedBytes is the decoded size currently held, for a gauge.
func (s *Store) CachedBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}
