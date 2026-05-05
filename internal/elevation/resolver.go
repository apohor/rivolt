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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	// NOTE: this leaks per-tile coordinates off-LAN; the recorder is
	// opt-in (ELEVATION_ENABLED=1) and operators are expected to
	// either point ELEVATION_TILES_URL at an in-cluster mirror or
	// run with a pre-warmed disk cache so tiles only fetch once.
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
// the hot path lock-free on warm tiles. An optional disk cache
// (cacheDir) survives pod restarts and can be pre-warmed by the
// operator (rsync from a Terrarium dump) for fully-offline operation.
type Resolver struct {
	tileURL  string
	cacheDir string
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

// Config bundles all Resolver knobs. Zero values fall back to
// package defaults; the empty CacheDir disables disk persistence.
type Config struct {
	// TileURL is the Terrarium tile template. Empty -> DefaultTileURL
	// (Mapzen on AWS Open Data, off-LAN).
	TileURL string
	// CacheDir, if non-empty, persists fetched PNGs to disk under
	// {dir}/{z}/{x}/{y}.png. Used both as a read-through cache (avoids
	// re-fetching after a pod restart) and as a self-hosted offline
	// store: an operator can rsync a pre-built Terrarium tile dump
	// here and leave TileURL empty/blackholed for fully-offline runs.
	CacheDir string
	// Zoom: 0 -> DefaultZoom (12).
	Zoom int
	// MaxTiles bounds the in-memory LRU. 0 -> DefaultCacheSize.
	MaxTiles int
	// HTTPClient: nil -> http.Client with 10s timeout.
	HTTPClient *http.Client
	// Logger: nil -> slog.Default().
	Logger *slog.Logger
}

// New constructs a Resolver from a Config. See Config field docs for
// per-field defaults.
func New(cfg Config) *Resolver {
	tileURL := cfg.TileURL
	if tileURL == "" {
		tileURL = DefaultTileURL
	}
	zoom := cfg.Zoom
	if zoom <= 0 || zoom > 15 {
		zoom = DefaultZoom
	}
	maxTiles := cfg.MaxTiles
	if maxTiles <= 0 {
		maxTiles = DefaultCacheSize
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{
		tileURL:  tileURL,
		cacheDir: cfg.CacheDir,
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

// fetchTile resolves a tile via (1) disk cache, then (2) HTTP, and
// stores the decoded result in the in-memory cache. Errors are logged
// and dropped: a missing tile (404 over open ocean, server hiccup,
// disk read error) just means future Lookups for that tile keep
// returning ok=false. The inflight guard prevents duplicate concurrent
// fetches.
func (r *Resolver) fetchTile(key tileKey) {
	defer func() {
		r.mu.Lock()
		delete(r.inflight, key)
		r.mu.Unlock()
	}()

	if t, ok := r.readDisk(key); ok {
		r.store(key, t)
		return
	}

	body, err := r.fetchHTTP(key)
	if err != nil {
		r.logger.Debug("elevation: fetch failed", "key", key, "err", err.Error())
		return
	}
	t, err := decodePNGBytes(body)
	if err != nil {
		r.logger.Debug("elevation: png decode failed", "key", key, "err", err.Error())
		return
	}
	if t == nil {
		// 1x1 placeholder over open ocean etc. -- not worth caching
		// to disk.
		return
	}
	r.writeDisk(key, body)
	r.store(key, t)
}

// fetchHTTP performs the upstream tile GET and returns the raw PNG
// bytes (so we can persist the exact server response to disk before
// decoding).
func (r *Resolver) fetchHTTP(key tileKey) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	url := buildTileURL(r.tileURL, key.z, key.x, key.y)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "image/png")
	req.Header.Set("User-Agent", "rivolt/elevation")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non-200: %d", resp.StatusCode)
	}
	// Cap at 1 MB; a 256x256 Terrarium PNG is ~150 KB.
	const maxBytes = 1 << 20
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}

// readDisk loads a previously-cached tile. Returns ok=false on any
// error (cache disabled, file missing, decode failure) -- callers
// fall back to HTTP. We never propagate disk errors: a corrupt file
// just gets re-fetched.
func (r *Resolver) readDisk(key tileKey) (*tile, bool) {
	if r.cacheDir == "" {
		return nil, false
	}
	path := r.diskPath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			r.logger.Debug("elevation: disk read failed", "path", path, "err", err.Error())
		}
		return nil, false
	}
	t, err := decodePNGBytes(data)
	if err != nil || t == nil {
		// Stale / corrupt file -- delete so the HTTP fallback can
		// repopulate cleanly.
		_ = os.Remove(path)
		return nil, false
	}
	return t, true
}

// writeDisk persists raw PNG bytes to {cacheDir}/{z}/{x}/{y}.png via
// a temp-file-and-rename so a crash can't leave a half-written file
// that readDisk would later treat as poisonous.
func (r *Resolver) writeDisk(key tileKey, data []byte) {
	if r.cacheDir == "" || len(data) == 0 {
		return
	}
	path := r.diskPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.logger.Debug("elevation: mkdir failed", "path", path, "err", err.Error())
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tile-*.png")
	if err != nil {
		r.logger.Debug("elevation: tempfile failed", "path", path, "err", err.Error())
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		r.logger.Debug("elevation: temp write failed", "path", path, "err", err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		r.logger.Debug("elevation: temp close failed", "path", path, "err", err.Error())
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		r.logger.Debug("elevation: rename failed", "path", path, "err", err.Error())
	}
}

func (r *Resolver) diskPath(key tileKey) string {
	return filepath.Join(
		r.cacheDir,
		fmt.Sprintf("%d", key.z),
		fmt.Sprintf("%d", key.x),
		fmt.Sprintf("%d.png", key.y),
	)
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
