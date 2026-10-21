// Package chargers serves the chargers.pmtiles archive from the
// rivolt API: the entire ~10 MB file is fetched once at startup
// (or first request), parsed in-memory, and queries against it run
// fully server-side. This replaces the SPA's per-tile fan-out over
// the same archive — for long corridors that was ~hundreds of HTTPS
// range requests per planner re-render, saturating the browser's
// per-origin socket pool and feeling sluggish even after the upstream
// tile server moved to tmpfs.
//
// Wire path: tiles nginx pod serves /chargers.pmtiles → rivolt
// fetches it once via the in-cluster Service → keeps a *[]byte in
// memory → answers /api/maps/chargers-along with a flat JSON list.
package chargers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/maptile"
	pm "github.com/protomaps/go-pmtiles/pmtiles"
)

// Archive holds the entire chargers PMTiles file in memory plus a
// decoded root directory. Leaf directories are decoded lazily and
// cached in leafDirs. Safe for concurrent readers; the only writer
// is Reload.
type Archive struct {
	url string

	mu       sync.RWMutex
	raw      []byte
	header   pm.HeaderV3
	rootDir  []pm.EntryV3
	leafDirs sync.Map // map[uint64][]pm.EntryV3, key = leaf offset

	loadedAt time.Time
	// etag is the validator of the loaded file, sent as If-None-Match by Refresh.
	etag string
	// stations is the loaded copy's feature count (the shrink guard's baseline);
	// rejectedETag is the last file the guard refused, so Refresh does not
	// download it again every time.
	stations     int
	rejectedETag string
}

// New returns an empty archive; the caller drives Reload to populate
// it (so startup wiring can defer the fetch until the URL is known).
func New(url string) *Archive {
	return &Archive{url: url}
}

// LoadedAt returns the timestamp of the last successful Reload, or
// the zero Time if the archive has not yet been loaded.
func (a *Archive) LoadedAt() time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.loadedAt
}

// Reload fetches the .pmtiles file from a.url, parses the header
// and root directory, and atomically swaps the live state. Existing
// in-flight queries continue to read the previous bytes via their
// captured RLock; the next query sees the new state.
func (a *Archive) Reload(ctx context.Context) error {
	_, err := a.fetch(ctx, false)
	return err
}

// Refresh reloads the archive only when the file behind the URL changed (a
// conditional GET on the ETag of the loaded copy), so a rebuilt archive is picked
// up without a restart. changed is false when the server says 304; a server
// that sends no ETag gets the full file each time (and reports changed). A
// failed fetch leaves the loaded archive in place.
func (a *Archive) Refresh(ctx context.Context) (changed bool, err error) {
	return a.fetch(ctx, true)
}

func (a *Archive) fetch(ctx context.Context, conditional bool) (bool, error) {
	if a.url == "" {
		return false, errors.New("chargers: no archive URL configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url, nil)
	if err != nil {
		return false, err
	}
	if conditional {
		a.mu.RLock()
		etag, rejected := a.etag, a.rejectedETag
		a.mu.RUnlock()
		if rejected != "" {
			// The file the guard refused: a cheap HEAD tells whether it is still
			// there, and keeps saying so (an error, so the caller logs it every
			// time) instead of downloading it again or going quiet.
			if cur, err := a.headETag(ctx); err == nil && cur == rejected {
				return false, fmt.Errorf("chargers archive still refused: the file at the URL (ETag %s) is under %.0f%% of the loaded copy's stations; keeping the loaded copy", rejected, shrinkGuardRatio*100)
			}
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return false, fmt.Errorf("fetch chargers archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("chargers archive: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("read archive: %w", err)
	}
	if len(raw) < pm.HeaderV3LenBytes {
		return false, fmt.Errorf("chargers archive too short: %d bytes", len(raw))
	}
	header, err := pm.DeserializeHeader(raw[:pm.HeaderV3LenBytes])
	if err != nil {
		return false, fmt.Errorf("parse header: %w", err)
	}
	rootBytes := raw[header.RootOffset : header.RootOffset+header.RootLength]
	rootDir := pm.DeserializeEntries(bytes.NewBuffer(rootBytes), header.InternalCompression)

	// Shrink guard: a half-written or wrong file at the URL must not replace a
	// good archive. The candidate is counted before anything is swapped.
	cand := &Archive{raw: raw, header: header, rootDir: rootDir}
	n := cand.featureCount()
	a.mu.RLock()
	had := a.stations
	a.mu.RUnlock()
	if had > 0 && float64(n) < shrinkGuardRatio*float64(had) {
		a.mu.Lock()
		a.rejectedETag = resp.Header.Get("ETag")
		a.mu.Unlock()
		return false, fmt.Errorf("chargers archive shrank from %d to %d stations (under %.0f%%); keeping the loaded copy", had, n, shrinkGuardRatio*100)
	}

	a.mu.Lock()
	a.raw = raw
	a.header = header
	a.rootDir = rootDir
	a.leafDirs = sync.Map{}
	a.loadedAt = time.Now()
	a.etag = resp.Header.Get("ETag")
	a.stations = n
	a.rejectedETag = ""
	a.mu.Unlock()
	return true, nil
}

// headETag is the ETag the server reports for the archive URL now.
func (a *Archive) headETag(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, a.url, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chargers archive: HEAD %d", resp.StatusCode)
	}
	return resp.Header.Get("ETag"), nil
}

// shrinkGuardRatio is the least a newly fetched archive may hold, as a share of
// the loaded one's stations, before it is refused.
const shrinkGuardRatio = 0.8

// featureCount is the number of charger features in the archive (a station on a
// tile edge counts per tile; only the ratio between two archives matters).
func (a *Archive) featureCount() int {
	n := 0
	var walk func(es []pm.EntryV3)
	walk = func(es []pm.EntryV3) {
		for _, e := range es {
			if e.RunLength == 0 {
				walk(a.lookupLeaf(a.header.LeafDirectoryOffset+e.Offset, e.Length))
				continue
			}
			for i := uint32(0); i < e.RunLength; i++ {
				z, x, y := pm.IDToZxy(e.TileID + uint64(i))
				pois, err := a.tilePOIs(z, x, y)
				if err == nil {
					n += len(pois)
				}
			}
		}
	}
	walk(a.rootDir)
	return n
}

// tileBytes returns the decompressed MVT bytes for (z,x,y) or nil
// when the archive has no data for that tile. Walks the root + leaf
// directories. The returned slice is a new buffer; safe to retain.
func (a *Archive) tileBytes(z uint8, x, y uint32) ([]byte, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.raw) == 0 {
		return nil, errors.New("chargers archive not loaded")
	}
	id := pm.ZxyToID(z, x, y)
	entry, ok := pm.FindTile(a.rootDir, id)
	if !ok {
		return nil, nil
	}
	// RunLength == 0 means "this entry points at a leaf directory."
	// Walk into it.
	if entry.RunLength == 0 {
		leafOffset := a.header.LeafDirectoryOffset + entry.Offset
		leaf := a.lookupLeaf(leafOffset, entry.Length)
		entry, ok = pm.FindTile(leaf, id)
		if !ok || entry.RunLength == 0 {
			return nil, nil
		}
	}
	start := a.header.TileDataOffset + entry.Offset
	end := start + uint64(entry.Length)
	if end > uint64(len(a.raw)) {
		return nil, fmt.Errorf("tile range %d..%d outside archive (%d bytes)", start, end, len(a.raw))
	}
	raw := a.raw[start:end]
	if a.header.TileCompression == pm.Gzip {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("tile gunzip: %w", err)
		}
		defer func() { _ = gz.Close() }() // a gzip READER: nothing to flush
		return io.ReadAll(gz)
	}
	// Copy so callers can safely retain.
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, nil
}

func (a *Archive) lookupLeaf(offset uint64, length uint32) []pm.EntryV3 {
	if v, ok := a.leafDirs.Load(offset); ok {
		return v.([]pm.EntryV3)
	}
	end := offset + uint64(length)
	if end > uint64(len(a.raw)) {
		return nil
	}
	entries := pm.DeserializeEntries(bytes.NewBuffer(a.raw[offset:end]), a.header.InternalCompression)
	a.leafDirs.Store(offset, entries)
	return entries
}

// POI is one charging-station feature as the SPA expects it.
// Fields mirror the SPA's POI type in web/src/lib/poi.ts so the
// renderer code is unchanged. Names are camelCase on the JSON wire
// to match the existing TS type.
type POI struct {
	Lat           float64 `json:"lat"`
	Lon           float64 `json:"lon"`
	Name          string  `json:"name,omitempty"`
	IsDCFC        bool    `json:"isDCFC"`
	IsL2          bool    `json:"isL2"`
	MaxPowerKW    float64 `json:"maxPowerKW,omitempty"`
	FacilityType  string  `json:"facilityType,omitempty"`
	EVNetwork     string  `json:"evNetwork,omitempty"`
	EVPricing     string  `json:"evPricing,omitempty"`
	EntityID      string  `json:"entityID,omitempty"`
	DCFCCount     int     `json:"dcfcCount,omitempty"`
	L2Count       int     `json:"l2Count,omitempty"`
	SocketTesla   bool    `json:"socketTeslaSupercharger,omitempty"`
	SocketCCS1    bool    `json:"socketType1Combo,omitempty"`
	SocketChademo bool    `json:"socketChademo,omitempty"`
	// SocketJ1772 and SocketNEMA1450 are the AC connectors the listing flags
	// (the archive's socket:type1 and socket:nema_1450).
	SocketJ1772    bool `json:"socketJ1772,omitempty"`
	SocketNEMA1450 bool `json:"socketNema1450,omitempty"`
	// The campground fields come from the Recreation.gov (RIDB) step of the
	// archive build; zero on every NREL-only station. HookupAmps is the
	// highest electric hookup of any campsite, ElectricSites of Sites the
	// number with power. AsOf is the export's date ("2026-10-20").
	HookupAmps    int    `json:"hookupAmps,omitempty"`
	ElectricSites int    `json:"electricSites,omitempty"`
	Sites         int    `json:"sites,omitempty"`
	Reservable    bool   `json:"reservable,omitempty"`
	Source        string `json:"source,omitempty"`
	// RIDBID is the Recreation.gov FacilityID of the campground, set on a
	// station that came from, or was merged with, a RIDB record.
	RIDBID string `json:"ridbID,omitempty"`
	AsOf          string `json:"asof,omitempty"`
}

// Filter matches the SPA's ChargerFilter strings.
type Filter string

const (
	FilterDCFC   Filter = "dcfc"
	FilterL2     Filter = "l2"
	FilterHotels Filter = "hotels"
	FilterCamp   Filter = "camp"
	FilterAll    Filter = "all"
)

// Hotel facility types from NREL AFDC (also surfaced in the SPA's
// HOTEL_FACILITY_TYPES). Used for the "hotels" filter — must match
// AND have at least one L2 stall.
var hotelFacilityTypes = map[string]bool{
	"HOTEL":             true,
	"MOTEL":             true,
	"B_AND_B":           true, // what NREL writes
	"BED_AND_BREAKFAST": true,
	"INN":               true,
	"LODGE":             true,
	"RESORT":            true,
}

// campFacilityTypes are the NREL facility types of a campground; a station the
// Recreation.gov step added carries CAMPGROUND too.
var campFacilityTypes = map[string]bool{
	"CAMPGROUND": true,
	"RV_PARK":    true,
}

// IsCampground reports whether poi is a campground or RV park with a way to
// plug in: a listed hookup (HookupAmps), a Level 2 stall or a NEMA 14-50.
func (poi POI) IsCampground() bool {
	if !campFacilityTypes[strNormUpper(poi.FacilityType)] {
		return false
	}
	return poi.HookupAmps > 0 || poi.IsL2 || poi.SocketNEMA1450
}

// chargersZoom is the zoom level the build pipeline emits in
// chargers.pmtiles. Hard-coded — must match
// apps/maps/tiles/manifests/chargers.yaml.
const chargersZoom uint8 = 14

// defaultCorridorKM is the half-width of the search band around the
// route — 20 mi, matching the SPA's CORRIDOR_KM = 32.2 km.
const defaultCorridorKM = 32.2

// endpointTrimMeters trims the route at both endpoints by this much
// to drop the metro-cluster of chargers that project onto the last
// few segments. Mirrors the SPA's ENDPOINT_TRIM_M (20 mi).
const endpointTrimMeters = 32_000.0

// QueryCorridorOptions tunes the corridor scan; zero values pick the
// SPA defaults.
type QueryCorridorOptions struct {
	CorridorKm        float64
	EndpointTrimMeter float64
	MinPowerKW        float64
	// If true, includes destinations that project onto the route's
	// endpoint segment. Off by default (matches SPA behaviour).
	IncludeEndpoints bool
	// Context, when set, ends the query early once it is done (a client that
	// went away, a request timeout).
	Context context.Context
}

// Limits on a corridor query, so one request cannot ask for a continent of tiles.
const (
	// MaxPathPoints is the longest route polyline accepted.
	MaxPathPoints = 5000
	// MaxCorridorKm is the widest search band accepted (half-width).
	MaxCorridorKm = 50.0
	// maxLatitude is the Web Mercator limit; a latitude beyond it is clamped.
	maxLatitude = 85.05
	// maxQueryTiles bounds the tile box a query may span (a coast-to-coast route
	// is about 1.2 million tiles at this zoom).
	maxQueryTiles = 4_000_000
)

var (
	// ErrBadPath is a path with a coordinate that is not a number.
	ErrBadPath = errors.New("chargers: path has a coordinate that is not a number")
	// ErrQueryTooLarge is a path with too many points or spanning too many tiles.
	ErrQueryTooLarge = errors.New("chargers: query is too large")
)

// corridorBox is a validated query: the corridor half-width (km, clamped) and the
// tile box to read.
type corridorBox struct {
	corridorKM                 float64
	txMin, txMax, tyMin, tyMax uint32
}

// planCorridor validates and clamps a query before anything is read: coordinates
// are clamped to the map (latitude +-85.05, longitude +-180), the corridor to
// MaxCorridorKm, and the path to MaxPathPoints; a box of more than maxQueryTiles
// tiles is refused. It does not touch the archive. The returned path is a
// clamped copy.
func planCorridor(path [][2]float64, corridorKM float64) ([][2]float64, corridorBox, error) {
	if len(path) > MaxPathPoints {
		return nil, corridorBox{}, ErrQueryTooLarge
	}
	if corridorKM <= 0 {
		corridorKM = defaultCorridorKM
	}
	corridorKM = math.Min(corridorKM, MaxCorridorKm)
	clamped := make([][2]float64, len(path))
	for i, p := range path {
		if math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
			return nil, corridorBox{}, ErrBadPath
		}
		clamped[i] = [2]float64{math.Max(-maxLatitude, math.Min(maxLatitude, p[0])), math.Max(-180, math.Min(180, p[1]))}
	}
	minLat, maxLat := clamped[0][0], clamped[0][0]
	minLon, maxLon := clamped[0][1], clamped[0][1]
	for _, p := range clamped {
		minLat, maxLat = math.Min(minLat, p[0]), math.Max(maxLat, p[0])
		minLon, maxLon = math.Min(minLon, p[1]), math.Max(maxLon, p[1])
	}
	midLat := (minLat + maxLat) / 2
	dLat := corridorKM / 111.32
	dLon := corridorKM / (111.32 * math.Cos(midLat*math.Pi/180))
	minLat = math.Max(-maxLatitude, minLat-dLat)
	maxLat = math.Min(maxLatitude, maxLat+dLat)
	minLon = math.Max(-180, minLon-dLon)
	maxLon = math.Min(180, maxLon+dLon)

	// Tile bounds at z14 (TMS y increases southward).
	box := corridorBox{
		corridorKM: corridorKM,
		txMin:      lonToTileX(minLon, chargersZoom), txMax: lonToTileX(maxLon, chargersZoom),
		tyMin: latToTileY(maxLat, chargersZoom), tyMax: latToTileY(minLat, chargersZoom),
	}
	if box.txMax < box.txMin || box.tyMax < box.tyMin {
		return nil, corridorBox{}, ErrBadPath
	}
	if tiles := uint64(box.txMax-box.txMin+1) * uint64(box.tyMax-box.tyMin+1); tiles > maxQueryTiles {
		return nil, corridorBox{}, ErrQueryTooLarge
	}
	return clamped, box, nil
}

// QueryCorridor returns all chargers passing `filter` within
// `corridorKm` of any non-endpoint point on `path`. Concurrent-safe. The query is
// validated first (see planCorridor); ErrBadPath and ErrQueryTooLarge say why it
// was refused, and a done opts.Context stops it early with the context's error.
func (a *Archive) QueryCorridor(path [][2]float64, filter Filter, opts QueryCorridorOptions) ([]POI, error) {
	if len(path) < 2 {
		return nil, nil
	}
	trimM := opts.EndpointTrimMeter
	if trimM <= 0 {
		trimM = endpointTrimMeters
	}
	if filter == "" {
		filter = FilterDCFC
	}
	path, box, err := planCorridor(path, opts.CorridorKm)
	if err != nil {
		return nil, err
	}
	corridorKM := box.corridorKM
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// Project once for perpendicular-distance + arc-length math.
	proj := projectPath(path)

	// Bounded-concurrency parallel decode. The tile reads are
	// in-memory; the bottleneck is MVT decoding CPU. Cap at 8 to
	// avoid lock contention on the leafDirs sync.Map. Tiles are walked, not
	// listed: the box can be large, the work in flight never is.
	const concurrency = 8
	sem := make(chan struct{}, concurrency)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		chunks [][]POI
	)
walk:
	for tx := box.txMin; tx <= box.txMax; tx++ {
		for ty := box.tyMin; ty <= box.tyMax; ty++ {
			if ctx.Err() != nil {
				break walk // a done context wins over a free slot
			}
			select {
			case <-ctx.Done():
				break walk
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go func(tx, ty uint32) {
				defer wg.Done()
				defer func() { <-sem }()
				pois, err := a.featuresInTile(chargersZoom, tx, ty, filter, proj, trimM, corridorKM*1000, opts)
				if err != nil || len(pois) == 0 {
					return
				}
				mu.Lock()
				chunks = append(chunks, pois)
				mu.Unlock()
			}(tx, ty)
		}
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Dedup by EntityID; preserve first-seen.
	seen := make(map[string]struct{}, 256)
	pois := make([]POI, 0, 64)
	for _, chunk := range chunks {
		for _, p := range chunk {
			key := p.EntityID
			if key == "" {
				// Fall back to coordinate-rounded key.
				key = fmt.Sprintf("%.5f,%.5f", p.Lat, p.Lon)
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			pois = append(pois, p)
		}
	}
	// Sort by lon then lat so the output is deterministic for
	// caching and easier to diff in tests.
	sort.SliceStable(pois, func(i, j int) bool {
		if pois[i].Lon != pois[j].Lon {
			return pois[i].Lon < pois[j].Lon
		}
		return pois[i].Lat < pois[j].Lat
	})
	return pois, nil
}

// featuresInTile decodes one MVT tile, projects each feature back
// to WGS84, and returns the subset that passes the filter + corridor
// distance check.
func (a *Archive) featuresInTile(z uint8, x, y uint32, filter Filter, proj projectedPath, trimM, corridorM float64, opts QueryCorridorOptions) ([]POI, error) {
	all, err := a.tilePOIs(z, x, y)
	if err != nil {
		return nil, err
	}
	pois := make([]POI, 0, 8)
	for _, poi := range all {
		if opts.MinPowerKW > 0 && poi.MaxPowerKW > 0 && poi.MaxPowerKW < opts.MinPowerKW {
			continue
		}
		if !poi.matches(filter) {
			continue
		}
		// Corridor distance + endpoint trim. +Inf = endpoint-
		// projection-rejected; any larger-than-corridor value =
		// outside the band. Tile bbox is wider than the corridor
		// by design (slippy-map tile grid doesn't align with the
		// corridor band), so this is the real filter.
		perp := proj.perpDistanceM(poi.Lat, poi.Lon, trimM, !opts.IncludeEndpoints)
		if perp == math.Inf(1) || perp > corridorM {
			continue
		}
		pois = append(pois, poi)
	}
	return pois, nil
}

// matches reports whether poi passes filter.
func (poi POI) matches(filter Filter) bool {
	switch filter {
	case FilterDCFC:
		return poi.IsDCFC
	case FilterL2:
		return poi.IsL2
	case FilterHotels:
		return hotelFacilityTypes[strNormUpper(poi.FacilityType)] && poi.IsL2
	case FilterCamp:
		return poi.IsCampground()
	}
	return true
}

// Nearest returns the closest charger passing filter within maxM
// meters of (lat, lon). Used to attribute a recorded charging session
// to the station (and network) it happened at. Searches the point's
// tile and its eight neighbours, so maxM must stay well under a z14
// tile (~2.4 km at the equator). Concurrent-safe.
func (a *Archive) Nearest(lat, lon, maxM float64, filter Filter) (POI, bool, error) {
	near, err := a.Within(lat, lon, maxM, filter)
	if err != nil || len(near) == 0 {
		return POI{}, false, err
	}
	return near[0].POI, true, nil
}

// NearPOI is a charger with its distance from the query point.
type NearPOI struct {
	POI
	DistM float64
}

// Within returns every charger passing filter within maxM meters of
// (lat, lon), nearest first: the whole site, so a caller can tell a
// co-located one (a Supercharger beside RAN) from a single network.
// Same tile search and maxM limit as Nearest. Concurrent-safe.
func (a *Archive) Within(lat, lon, maxM float64, filter Filter) ([]NearPOI, error) {
	cx, cy := lonToTileX(lon, chargersZoom), latToTileY(lat, chargersZoom)
	var out []NearPOI
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			x, y := int64(cx)+int64(dx), int64(cy)+int64(dy)
			if x < 0 || y < 0 {
				continue
			}
			pois, err := a.tilePOIs(chargersZoom, uint32(x), uint32(y))
			if err != nil {
				return nil, err
			}
			for _, p := range pois {
				if !p.matches(filter) {
					continue
				}
				if d := haversineM(lat, lon, p.Lat, p.Lon); d <= maxM {
					out = append(out, NearPOI{POI: p, DistM: d})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DistM < out[j].DistM })
	return out, nil
}

func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

// tilePOIs decodes every charger feature in one tile.
func (a *Archive) tilePOIs(z uint8, x, y uint32) ([]POI, error) {
	raw, err := a.tileBytes(z, x, y)
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	layers, err := mvt.Unmarshal(raw)
	if err != nil {
		return nil, fmt.Errorf("mvt unmarshal z%d/%d/%d: %w", z, x, y, err)
	}
	tile := maptile.New(x, y, maptile.Zoom(z))
	for _, l := range layers {
		l.ProjectToWGS84(tile)
	}
	pois := make([]POI, 0, 8)
	for _, layer := range layers {
		// chargers.pmtiles emits a single "chargers" layer. Tolerate
		// other names so a future archive variant doesn't break the
		// reader; the build pipeline is the source of truth.
		if layer.Name != "chargers" {
			continue
		}
		for _, f := range layer.Features {
			pt, ok := pointCoord(f.Geometry)
			if !ok {
				continue
			}
			poi := POI{Lat: pt[1], Lon: pt[0]}
			props := f.Properties
			poi.Name = stringProp(props, "name")
			poi.FacilityType = stringProp(props, "facility_type")
			// The tile build (rivolt-infra maps/tiles chargers.yaml)
			// renames NREL's ev_network → network, ev_pricing → fee and
			// id → "@id"; the NREL names stay as fallbacks.
			poi.EVNetwork = firstStringProp(props, "network", "ev_network")
			poi.EVPricing = firstStringProp(props, "fee", "ev_pricing")
			poi.EntityID = firstStringProp(props, "@id", "entity_id", "id")
			poi.MaxPowerKW = maxPowerKW(props)
			poi.DCFCCount = intProp(props, "dcfc_count")
			poi.L2Count = intProp(props, "l2_count")
			poi.SocketTesla = yesProp(props, "socket:tesla_supercharger")
			poi.SocketCCS1 = yesProp(props, "socket:type1_combo")
			poi.SocketChademo = yesProp(props, "socket:chademo")
			poi.SocketJ1772 = yesProp(props, "socket:type1")
			poi.SocketNEMA1450 = yesProp(props, "socket:nema_1450")
			poi.HookupAmps = intProp(props, "hookup_amps")
			poi.ElectricSites = intProp(props, "electric_sites")
			poi.Sites = intProp(props, "sites")
			poi.Reservable = yesProp(props, "reservable")
			poi.Source = stringProp(props, "source")
			poi.RIDBID = firstStringProp(props, "ridb_id")
			poi.AsOf = stringProp(props, "asof")

			// DCFC/L2 inference: prefer explicit counts when emitted,
			// otherwise infer from unambiguous DC connectors or
			// known max power.
			if poi.DCFCCount > 0 {
				poi.IsDCFC = true
			} else if poi.SocketCCS1 || poi.SocketChademo {
				poi.IsDCFC = true
			} else if poi.MaxPowerKW >= 50 {
				poi.IsDCFC = true
			}
			if poi.L2Count > 0 {
				poi.IsL2 = true
			} else if !poi.IsDCFC {
				poi.IsL2 = true
			}
			pois = append(pois, poi)
		}
	}
	return pois, nil
}

// --- helpers below ---

// pointCoord extracts (lon, lat) from a Point geometry. The MVT
// decoder returns concrete orb.Geometry values; chargers features
// are always points, but tolerate the multi-point case by taking
// the first vertex.
func pointCoord(g orb.Geometry) ([2]float64, bool) {
	switch pt := g.(type) {
	case orb.Point:
		return [2]float64{pt[0], pt[1]}, true
	case orb.MultiPoint:
		if len(pt) > 0 {
			return [2]float64{pt[0][0], pt[0][1]}, true
		}
	}
	return [2]float64{}, false
}

// firstStringProp returns the first non-empty string among keys.
func firstStringProp(props map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := stringProp(props, k); s != "" {
			return s
		}
	}
	return ""
}

func stringProp(props map[string]any, key string) string {
	if v, ok := props[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func intProp(props map[string]any, key string) int {
	if v, ok := props[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		case uint32:
			return int(n)
		case uint64:
			return int(n)
		}
	}
	return 0
}

func yesProp(props map[string]any, key string) bool {
	if v, ok := props[key]; ok {
		if s, ok := v.(string); ok {
			return s == "yes" || s == "true" || s == "1"
		}
	}
	return false
}

func maxPowerKW(props map[string]any) float64 {
	for _, k := range []string{"max_power_kw", "ev_dc_fast_kw"} {
		if v, ok := props[k]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case int:
				return float64(n)
			case int64:
				return float64(n)
			case uint32:
				return float64(n)
			case uint64:
				return float64(n)
			}
		}
	}
	return 0
}

func strNormUpper(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		out[i] = c
	}
	return string(out)
}

// --- projection / corridor math (lifted from web/src/lib/poi.ts) ---

type projectedPath struct {
	xs      []float64
	ys      []float64
	cumLen  []float64 // meters, cumulative arc length
	totalLn float64
	cosRef  float64 // path-centroid cos(lat); shared for candidate points
}

// Web-Mercator-ish meter-projection: convert lat/lon to local meters
// using a flat-earth approximation around the path centroid. Good
// for routes up to a few thousand km. Same approach as the SPA.
const earthRadiusM = 6371008.8

func projectPath(path [][2]float64) projectedPath {
	pp := projectedPath{
		xs:     make([]float64, len(path)),
		ys:     make([]float64, len(path)),
		cumLen: make([]float64, len(path)),
	}
	if len(path) == 0 {
		return pp
	}
	// Reference latitude for the flat-earth scale factor.
	var sumLat float64
	for _, p := range path {
		sumLat += p[0]
	}
	refLat := sumLat / float64(len(path))
	cosRef := math.Cos(refLat * math.Pi / 180)
	for i, p := range path {
		pp.xs[i] = p[1] * cosRef * (earthRadiusM * math.Pi / 180)
		pp.ys[i] = p[0] * (earthRadiusM * math.Pi / 180)
	}
	for i := 1; i < len(path); i++ {
		dx := pp.xs[i] - pp.xs[i-1]
		dy := pp.ys[i] - pp.ys[i-1]
		pp.cumLen[i] = pp.cumLen[i-1] + math.Sqrt(dx*dx+dy*dy)
	}
	pp.totalLn = pp.cumLen[len(pp.cumLen)-1]
	pp.cosRef = cosRef
	return pp
}

// perpDistanceM returns the minimum perpendicular distance from
// (lat, lon) to the route in meters, or +Inf when the closest
// projection falls within `trimM` of either endpoint and trim is
// enabled.
func (pp projectedPath) perpDistanceM(lat, lon, trimM float64, applyTrim bool) float64 {
	if len(pp.xs) < 2 {
		return math.Inf(1)
	}
	// Project the candidate point with the SAME flat-earth scale the
	// path used (pp.cosRef = cos of path-centroid lat). Using the
	// candidate's own latitude here gives a different coordinate
	// system, so points ~30 deg north of the path centroid landed
	// well inside the corridor even when they were hundreds of km
	// off-route.
	px := lon * pp.cosRef * (earthRadiusM * math.Pi / 180)
	py := lat * (earthRadiusM * math.Pi / 180)
	best := math.Inf(1)
	var bestAlong float64
	bestEndpoint := false
	lastSeg := len(pp.xs) - 2
	for i := 1; i < len(pp.xs); i++ {
		ax := pp.xs[i-1]
		ay := pp.ys[i-1]
		bx := pp.xs[i]
		by := pp.ys[i]
		dx := bx - ax
		dy := by - ay
		segLenSq := dx*dx + dy*dy
		if segLenSq == 0 {
			continue
		}
		t := ((px-ax)*dx + (py-ay)*dy) / segLenSq
		endpoint := false
		if t < 0 {
			t = 0
			if i-1 == 0 {
				endpoint = true
			}
		} else if t > 1 {
			t = 1
			if i-1 == lastSeg {
				endpoint = true
			}
		}
		cx := ax + t*dx
		cy := ay + t*dy
		ddx := px - cx
		ddy := py - cy
		d2 := ddx*ddx + ddy*ddy
		if d2 < best {
			best = d2
			segLen := math.Sqrt(segLenSq)
			bestAlong = pp.cumLen[i-1] + t*segLen
			bestEndpoint = endpoint
		}
	}
	if bestEndpoint {
		return math.Inf(1)
	}
	if applyTrim {
		if bestAlong < trimM {
			return math.Inf(1)
		}
		if pp.totalLn-bestAlong < trimM {
			return math.Inf(1)
		}
	}
	return math.Sqrt(best)
}

// lonToTileX / latToTileY: standard slippy-map TMS conversion.
func lonToTileX(lon float64, z uint8) uint32 {
	return uint32(math.Floor((lon + 180) / 360 * float64(int(1)<<z)))
}

func latToTileY(lat float64, z uint8) uint32 {
	r := lat * math.Pi / 180
	return uint32(math.Floor((1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * float64(int(1)<<z)))
}

// --- compile-time guard: tile_id helpers use protobuf's
// PutUvarint for entries; nothing else here uses encoding/binary,
// so a stray import becomes an "imported and not used" failure.
// Reference it via _ so vet doesn't complain.
var _ = binary.PutUvarint
