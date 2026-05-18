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
	if a.url == "" {
		return errors.New("chargers: no archive URL configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url, nil)
	if err != nil {
		return err
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("fetch chargers archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chargers archive: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	if len(raw) < pm.HeaderV3LenBytes {
		return fmt.Errorf("chargers archive too short: %d bytes", len(raw))
	}
	header, err := pm.DeserializeHeader(raw[:pm.HeaderV3LenBytes])
	if err != nil {
		return fmt.Errorf("parse header: %w", err)
	}
	rootBytes := raw[header.RootOffset : header.RootOffset+header.RootLength]
	rootDir := pm.DeserializeEntries(bytes.NewBuffer(rootBytes), header.InternalCompression)

	a.mu.Lock()
	a.raw = raw
	a.header = header
	a.rootDir = rootDir
	a.leafDirs = sync.Map{}
	a.loadedAt = time.Now()
	a.mu.Unlock()
	return nil
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
		defer gz.Close()
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
	Lat            float64 `json:"lat"`
	Lon            float64 `json:"lon"`
	Name           string  `json:"name,omitempty"`
	IsDCFC         bool    `json:"isDCFC"`
	IsL2           bool    `json:"isL2"`
	MaxPowerKW     float64 `json:"maxPowerKW,omitempty"`
	FacilityType   string  `json:"facilityType,omitempty"`
	EVNetwork      string  `json:"evNetwork,omitempty"`
	EVPricing      string  `json:"evPricing,omitempty"`
	EntityID       string  `json:"entityID,omitempty"`
	DCFCCount      int     `json:"dcfcCount,omitempty"`
	L2Count        int     `json:"l2Count,omitempty"`
	SocketTesla    bool    `json:"socketTeslaSupercharger,omitempty"`
	SocketCCS1     bool    `json:"socketType1Combo,omitempty"`
	SocketChademo  bool    `json:"socketChademo,omitempty"`
}

// Filter matches the SPA's ChargerFilter strings.
type Filter string

const (
	FilterDCFC   Filter = "dcfc"
	FilterL2     Filter = "l2"
	FilterHotels Filter = "hotels"
	FilterAll    Filter = "all"
)

// Hotel facility types from NREL AFDC (also surfaced in the SPA's
// HOTEL_FACILITY_TYPES). Used for the "hotels" filter — must match
// AND have at least one L2 stall.
var hotelFacilityTypes = map[string]bool{
	"HOTEL":             true,
	"MOTEL":             true,
	"BED_AND_BREAKFAST": true,
	"INN":               true,
	"LODGE":             true,
	"RESORT":            true,
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
}

// QueryCorridor returns all chargers passing `filter` within
// `corridorKm` of any non-endpoint point on `path`. Concurrent-safe.
func (a *Archive) QueryCorridor(path [][2]float64, filter Filter, opts QueryCorridorOptions) ([]POI, error) {
	if len(path) < 2 {
		return nil, nil
	}
	corridorKM := opts.CorridorKm
	if corridorKM <= 0 {
		corridorKM = defaultCorridorKM
	}
	trimM := opts.EndpointTrimMeter
	if trimM <= 0 {
		trimM = endpointTrimMeters
	}
	if filter == "" {
		filter = FilterDCFC
	}

	// Bounding box + corridor expansion (degrees).
	minLat, maxLat := path[0][0], path[0][0]
	minLon, maxLon := path[0][1], path[0][1]
	for _, p := range path {
		if p[0] < minLat {
			minLat = p[0]
		}
		if p[0] > maxLat {
			maxLat = p[0]
		}
		if p[1] < minLon {
			minLon = p[1]
		}
		if p[1] > maxLon {
			maxLon = p[1]
		}
	}
	midLat := (minLat + maxLat) / 2
	dLat := corridorKM / 111.32
	dLon := corridorKM / (111.32 * math.Cos(midLat*math.Pi/180))
	minLat -= dLat
	maxLat += dLat
	minLon -= dLon
	maxLon += dLon

	// Tile bounds at z14 (TMS y increases southward).
	txMin := lonToTileX(minLon, chargersZoom)
	txMax := lonToTileX(maxLon, chargersZoom)
	tyMin := latToTileY(maxLat, chargersZoom)
	tyMax := latToTileY(minLat, chargersZoom)

	// Project once for perpendicular-distance + arc-length math.
	proj := projectPath(path)

	type result struct{ pois []POI }
	tasks := make([][2]uint32, 0, (txMax-txMin+1)*(tyMax-tyMin+1))
	for tx := txMin; tx <= txMax; tx++ {
		for ty := tyMin; ty <= tyMax; ty++ {
			tasks = append(tasks, [2]uint32{tx, ty})
		}
	}
	// Bounded-concurrency parallel decode. The tile reads are
	// in-memory; the bottleneck is MVT decoding CPU. Cap at 8 to
	// avoid lock contention on the leafDirs sync.Map.
	const concurrency = 8
	out := make(chan []POI, len(tasks))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, t := range tasks {
		tx, ty := t[0], t[1]
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			pois, err := a.featuresInTile(chargersZoom, tx, ty, filter, proj, trimM, corridorKM*1000, opts)
			if err != nil || len(pois) == 0 {
				out <- nil
				return
			}
			out <- pois
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()

	// Dedup by EntityID; preserve first-seen.
	seen := make(map[string]struct{}, 256)
	pois := make([]POI, 0, 64)
	for chunk := range out {
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
	_ = result{} // unused; kept for future per-tile metrics
	return pois, nil
}

// featuresInTile decodes one MVT tile, projects each feature back
// to WGS84, and returns the subset that passes the filter + corridor
// distance check.
func (a *Archive) featuresInTile(z uint8, x, y uint32, filter Filter, proj projectedPath, trimM, corridorM float64, opts QueryCorridorOptions) ([]POI, error) {
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
			poi.EVNetwork = stringProp(props, "ev_network")
			poi.EVPricing = stringProp(props, "ev_pricing")
			poi.EntityID = stringProp(props, "entity_id")
			if poi.EntityID == "" {
				poi.EntityID = stringProp(props, "id")
			}
			poi.MaxPowerKW = maxPowerKW(props)
			poi.DCFCCount = intProp(props, "dcfc_count")
			poi.L2Count = intProp(props, "l2_count")
			poi.SocketTesla = yesProp(props, "socket:tesla_supercharger")
			poi.SocketCCS1 = yesProp(props, "socket:type1_combo")
			poi.SocketChademo = yesProp(props, "socket:chademo")

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

			if opts.MinPowerKW > 0 && poi.MaxPowerKW > 0 && poi.MaxPowerKW < opts.MinPowerKW {
				continue
			}
			switch filter {
			case FilterDCFC:
				if !poi.IsDCFC {
					continue
				}
			case FilterL2:
				if !poi.IsL2 {
					continue
				}
			case FilterHotels:
				if !hotelFacilityTypes[strNormUpper(poi.FacilityType)] {
					continue
				}
				if !poi.IsL2 {
					continue
				}
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
