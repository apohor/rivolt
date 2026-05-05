// Package elevation looks up altitude (meters above sea level) for
// arbitrary lat/lon coordinates against the Mapzen Terrarium DEM
// (https://github.com/tilezen/joerd/blob/master/docs/formats.md#terrarium).
//
// Terrarium tiles are 256x256 PNGs hosted on AWS Open Data:
//
//	https://s3.amazonaws.com/elevation-tiles-prod/terrarium/{z}/{x}/{y}.png
//
// Each pixel encodes elevation in meters with the formula:
//
//	height = (R * 256 + G + B / 256) - 32768
//
// We use zoom 12 (~9.5 m/pixel at the equator), which is well over
// the resolution we need for cabin-altitude charts and keeps tile
// counts small enough to LRU-cache in process memory.
//
// Hot-path semantics: Lookup is wired into the per-frame recorder
// path. Cache hits are O(microseconds). Cache misses kick off an
// asynchronous fetch and return ok=false; the next sample on the
// same tile (~9 km later, typically the very next frame) hits the
// warm cache. We never block the recorder on a network round trip.
package elevation

import (
	"context"
	"container/list"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	// DefaultZoom strikes the right balance between resolution
	// (~9.5 m/pixel at equator, sub-meter altitude error after
	// bilinear interp) and tile size (a single tile covers a 9-10 km
	// box, so a typical commute fits in 2-4 tiles).
	DefaultZoom = 12

	// DefaultTileURL is Mapzen's public Terrarium endpoint hosted on
	// AWS Open Data. Free, no key, no rate-limit (within reason).
	DefaultTileURL = "https://s3.amazonaws.com/elevation-tiles-prod/terrarium/{z}/{x}/{y}.png"

	// DefaultCacheSize bounds in-memory tile retention. Each tile is
	// 256*256*2 = 128 KB of decoded int16 elevation grid, so 256
	// tiles ~= 32 MB heap. 256 tiles at z=12 covers ~2,400 km of
	// route history -- a personal-scale recorder rarely evicts.
	DefaultCacheSize = 256

	// fetchTimeout caps a single tile fetch (only ever runs in a
	// background goroutine; never blocks Lookup callers).
	fetchTimeout = 8 * time.Second
)

// Resolver answers (lat, lon) -> altitude lookups against a tile
// server, with a bounded LRU of decoded tile grids in front to keep
// the hot path lock-free on warm tiles.
type Resolver struct {
	tileURL  string
	zoom     int
	client   *http.Client
	logger   *slog.Logger
	maxTiles int

	mu       sync.Mutex
	cache    map[tileKey]*list.Element // -> *cacheEntry
	lru      *list.List
	inflight map[tileKey]struct{}
}

type tileKey struct {
	z, x, y int
}

type cacheEntry struct {
	key  tileKey
	grid *tile // nil while a miss is being fetched
}

// tile holds a 256x256 elevation grid in meters. We pre-decode the
// PNG once on fetch so Lookup never touches image/png on the hot
// path.
type tile struct {
	w, h int
	// data[y*w+x] in meters (already RGB-decoded). int16 because
	// Earth's elevation range fits comfortably (-11000 to +9000)
	// and halves heap vs float32.
	data []int16
}

// New constructs a Resolver. Pass nil for httpClient to use a
// 10-second-timeout default. Pass empty tileURL to use Mapzen's
// public endpoint. Zero maxTiles falls back to DefaultCacheSize.
func New(tileURL string, zoom, maxTiles int, httpClient *http.Client, logger *slog.Logger) *Resolver {
	if tileURL == "" {
		tileURL = DefaultTileURL
	}
	if zoom <= 0 || zoom > 15 {
		zoom = DefaultZoom
	}
	if maxTiles <= 0 {
		maxTiles = DefaultCacheSize
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{
		tileURL:  tileURL,
		zoom:     zoom,
		client:   httpClient,
		logger:   logger,
		maxTiles: maxTiles,
		cache:    make(map[tileKey]*list.Element),
		lru:      list.New(),
		inflight: make(map[tileKey]struct{}),
	}
}

// Lookup returns the bilinearly-interpolated elevation at (lat, lon)
// in meters, or ok=false when the tile is not yet cached. On miss we
// asynchronously fetch the tile so subsequent lookups warm up; the
// caller (the recorder) must NOT block waiting -- elevation is
// best-effort and the column is nullable.
//
// Returns ok=false (without scheduling a fetch) for invalid lat/lon
// (NaN, out-of-range) or for the (0, 0) "no fix" sentinel the
// recorder uses when GPS is unavailable.
func (r *Resolver) Lookup(lat, lon float64) (float64, bool) {
	if !validLatLon(lat, lon) {
		return 0, false
	}
	z := r.zoom
	xtile, ytile, fx, fy := tileCoords(lat, lon, z)
	key := tileKey{z: z, x: xtile, y: ytile}

	r.mu.Lock()
	if elem, ok := r.cache[key]; ok {
		r.lru.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		grid := entry.grid
		r.mu.Unlock()
		if grid == nil {
			// Fetch is in flight from a previous miss; don't queue
			// another. Caller treats as "no data yet".
			return 0, false
		}
		return grid.sample(fx, fy), true
	}
	// Miss -- claim the slot, kick off a background fetch.
	if _, busy := r.inflight[key]; !busy {
		r.inflight[key] = struct{}{}
		go r.fetchTile(key)
	}
	r.mu.Unlock()
	return 0, false
}

// fetchTile downloads the PNG, decodes it, and stores the result in
// the cache. Errors are logged and dropped: a missing tile (404 over
// open ocean, or a server hiccup) just means future Lookups for that
// tile keep returning ok=false. The inflight guard prevents
// duplicate concurrent fetches of the same tile.
func (r *Resolver) fetchTile(key tileKey) {
	defer func() {
		r.mu.Lock()
		delete(r.inflight, key)
		r.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	url := buildTileURL(r.tileURL, key.z, key.x, key.y)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		r.logger.Debug("elevation: build request failed", "url", url, "err", err.Error())
		return
	}
	req.Header.Set("Accept", "image/png")
	req.Header.Set("User-Agent", "rivolt/elevation")

	resp, err := r.client.Do(req)
	if err != nil {
		r.logger.Debug("elevation: fetch failed", "url", url, "err", err.Error())
		return
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		r.logger.Debug("elevation: non-200", "url", url, "status", resp.StatusCode)
		return
	}

	img, err := png.Decode(resp.Body)
	if err != nil {
		r.logger.Debug("elevation: png decode failed", "url", url, "err", err.Error())
		return
	}
	t := decodeTerrarium(img)
	if t == nil {
		return
	}
	r.store(key, t)
}

// store inserts/updates the cache entry and enforces the LRU bound.
func (r *Resolver) store(key tileKey, t *tile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if elem, ok := r.cache[key]; ok {
		entry := elem.Value.(*cacheEntry)
		entry.grid = t
		r.lru.MoveToFront(elem)
		return
	}
	entry := &cacheEntry{key: key, grid: t}
	elem := r.lru.PushFront(entry)
	r.cache[key] = elem
	for r.lru.Len() > r.maxTiles {
		oldest := r.lru.Back()
		if oldest == nil {
			break
		}
		r.lru.Remove(oldest)
		delete(r.cache, oldest.Value.(*cacheEntry).key)
	}
}

func buildTileURL(tmpl string, z, x, y int) string {
	url := tmpl
	url = replaceAll(url, "{z}", fmt.Sprint(z))
	url = replaceAll(url, "{x}", fmt.Sprint(x))
	url = replaceAll(url, "{y}", fmt.Sprint(y))
	return url
}

// replaceAll is strings.ReplaceAll inlined to avoid the strings
// import dependency on this hot-ish helper. Keeps the package
// dep-free beyond the standard library.
func replaceAll(s, old, new string) string {
	out := make([]byte, 0, len(s))
	i := 0
	for i < len(s) {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			out = append(out, new...)
			i += len(old)
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}
