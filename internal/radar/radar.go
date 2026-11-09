// Package radar serves live weather radar tiles from NOAA/NWS (single-owner-era "live radar" feature).
//
// The source is the National Weather Service's open GeoServer (opengeo.ncep.noaa.gov): the MRMS (Multi-Radar/Multi-Sensor) base reflectivity
// mosaic, quality controlled, 1 km, a frame about every two minutes and about two hours of history, for CONUS, Alaska, Hawaii, the Caribbean
// and Guam. NWS content is public domain, free for any lawful purpose provided we do not claim it as our own, imply an endorsement or affiliation,
// or modify it and present it as official (weather.gov/disclaimer; the service's GetCapabilities says Fees none, AccessConstraints none).
//
// Users' browsers and phones never call NOAA: they ask this server for tiles, and NOAA sees only this server, with no user, no viewport and no
// account. A tile is cached by frame time (shared by every user, so the upstream load follows the number of distinct tiles in use and the frame
// rate, not the number of users) in process and, where the install has Redis, across replicas, with one fetch per tile per frame across pods.
package radar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// DefaultBaseURL is the NWS GeoServer.
const DefaultBaseURL = "https://opengeo.ncep.noaa.gov/geoserver"

// Tile and cache parameters.
const (
	TileSize      = 256
	MaxZoom       = 10 // deepest zoom the route admits; the client asks up to ClientMaxZoom and the map overzooms past it
	ClientMinZoom = 3  // below this a continent-sized radar picture is useless and the tile count is large
	ClientMaxZoom = 9  // the mosaic is 1 km: finer zooms only resample it
	frameTTL      = 60 * time.Second
	tileTTL       = 3 * time.Minute  // shorter than the 2 h history, longer than one frame interval, so a second user in the same frame hits
	staleAfter    = 30 * time.Minute // a newest frame older than this means the feed is down: no radar rather than a stale picture
	maxFrames     = 12               // the frame list handed to clients: the last two hours at about ten minute spacing
	frameSpacing  = 10 * time.Minute
	maxUpstream   = 512 << 10 // the most a tile answer may carry
	upstreamTO    = 8 * time.Second
	lockTTL       = 10 * time.Second
	localMax      = 512 // tiles kept in process
)

// A region is one MRMS mosaic with the box it covers (lon/lat degrees, from its GetCapabilities).
type region struct {
	name       string
	workspace  string
	layer      string
	w, s, e, n float64
}

var regions = []region{
	{"conus", "conus", "conus_bref_qcd", -130, 20, -60, 55},
	{"alaska", "alaska", "alaska_bref_qcd", -176, 50, -126, 72},
	{"hawaii", "hawaii", "hawaii_bref_qcd", -164, 15, -151, 26},
	{"carib", "carib", "carib_bref_qcd", -90, 10, -60, 25},
	{"guam", "guam", "guam_bref_qcd", 140, 9, 150, 18},
}

// Frames is what a client needs to show "radar as of": the newest frame and the recent ones (oldest first).
type Frames struct {
	Latest time.Time   `json:"-"`
	Recent []time.Time `json:"-"`
}

// Config wires a Service.
type Config struct {
	BaseURL string // the GeoServer; empty is DefaultBaseURL
	HTTP    *http.Client
	Redis   redis.Cmdable // nil: process-local caching only
	Prefix  string        // Redis key prefix (the install's)
	Now     func() time.Time
	// MaxUpstream bounds the concurrent requests to NOAA from this pod (default 6).
	MaxUpstream int
	// UserAgent identifies this server to NOAA (default names Rivolt and a contact).
	UserAgent string
}

// Service fetches, reprojects if it must, and caches radar tiles.
type Service struct {
	cfg        Config
	sem        chan struct{}
	sf         singleflight.Group
	prefer4326 atomic.Int64 // unix nanos until which the 4326 path is used first (the 3857 request failed)

	mu       sync.Mutex
	frames   Frames
	framesAt time.Time
	local    map[string]localTile
}

type localTile struct {
	png []byte
	at  time.Time
}

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: upstreamTO}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxUpstream <= 0 {
		cfg.MaxUpstream = 6
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Rivolt (+https://rivolt.dev; support@rivolt.dev) radar proxy"
	}
	return &Service{cfg: cfg, sem: make(chan struct{}, cfg.MaxUpstream), local: map[string]localTile{}}
}

// ErrUnavailable means the radar feed could not be read or is stale: the caller answers 503 and the client hides the layer for now.
var ErrUnavailable = errors.New("radar: feed unavailable")

var timeDim = regexp.MustCompile(`<Dimension name="time"[^>]*>([^<]+)</Dimension>`)

// Frames returns the newest frame and the recent ones, from the CONUS mosaic's time dimension (the others follow within seconds and are asked for
// by nearest time). Cached for a minute in process and across pods.
func (s *Service) Frames(ctx context.Context) (Frames, error) {
	s.mu.Lock()
	if !s.framesAt.IsZero() && s.cfg.Now().Sub(s.framesAt) < frameTTL {
		f := s.frames
		s.mu.Unlock()
		return f, nil
	}
	s.mu.Unlock()
	v, err, _ := s.sf.Do("frames", func() (any, error) {
		if f, ok := s.sharedFrames(ctx); ok {
			return f, nil
		}
		// One pod reads the capabilities; the others wait for what it stored.
		if !s.lock(ctx, "frames") {
			if f, ok := s.waitFrames(ctx); ok {
				return f, nil
			}
		}
		f, err := s.fetchFrames(ctx)
		if err != nil {
			return Frames{}, err
		}
		s.storeSharedFrames(ctx, f)
		return f, nil
	})
	if err != nil {
		return Frames{}, err
	}
	f := v.(Frames)
	s.mu.Lock()
	s.frames, s.framesAt = f, s.cfg.Now()
	s.mu.Unlock()
	return f, nil
}

func (s *Service) fetchFrames(ctx context.Context) (Frames, error) {
	r := regions[0]
	u := fmt.Sprintf("%s/%s/%s/ows?service=WMS&version=1.3.0&request=GetCapabilities", s.cfg.BaseURL, r.workspace, r.layer)
	body, err := s.get(ctx, u, 256<<10)
	if err != nil {
		return Frames{}, fmt.Errorf("%w: capabilities: %v", ErrUnavailable, err)
	}
	f, err := ParseFrames(body, s.cfg.Now())
	if err != nil {
		return Frames{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return f, nil
}

// ParseFrames reads the time dimension of a WMS GetCapabilities document and returns the newest frame and a thinned recent list. It is an error when
// there is no frame or the newest is older than staleAfter at now.
func ParseFrames(capabilities []byte, now time.Time) (Frames, error) {
	m := timeDim.FindSubmatch(capabilities)
	if m == nil {
		return Frames{}, errors.New("no time dimension")
	}
	var all []time.Time
	for _, p := range strings.Split(string(m[1]), ",") {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(p))
		if err == nil {
			all = append(all, t.UTC())
		}
	}
	if len(all) == 0 {
		return Frames{}, errors.New("no frames")
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Before(all[j]) })
	latest := all[len(all)-1]
	if now.Sub(latest) > staleAfter {
		return Frames{}, fmt.Errorf("newest frame is %s old", now.Sub(latest).Round(time.Minute))
	}
	// Thin to about one frame per ten minutes, newest first, then oldest first.
	recent := []time.Time{latest}
	for i := len(all) - 2; i >= 0 && len(recent) < maxFrames; i-- {
		if recent[len(recent)-1].Sub(all[i]) >= frameSpacing {
			recent = append(recent, all[i])
		}
	}
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	return Frames{Latest: latest, Recent: recent}, nil
}

// Tile returns the radar tile (z, x, y, Web Mercator) for the frame at frameUnix (0: the newest), the frame actually used, and the PNG. A tile
// outside every mosaic is a transparent PNG. A frame that is not one of the recent frames is replaced by the newest, so the time parameter is
// not an open relay into NOAA's history.
func (s *Service) Tile(ctx context.Context, z, x, y int, frameUnix int64) ([]byte, time.Time, error) {
	if z < 0 || z > MaxZoom || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		return nil, time.Time{}, errors.New("radar: tile out of range")
	}
	fr, err := s.Frames(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	frame := fr.Latest
	if frameUnix != 0 {
		for _, t := range fr.Recent {
			if t.Unix() == frameUnix {
				frame = t
				break
			}
		}
	}
	rs := regionsFor(z, x, y)
	if len(rs) == 0 {
		return transparentPNG, frame, nil
	}
	key := fmt.Sprintf("%d:%d:%d:%d", frame.Unix(), z, x, y)
	if b, ok := s.localGet(key); ok {
		return b, frame, nil
	}
	v, err, _ := s.sf.Do("tile:"+key, func() (any, error) {
		if b, ok := s.sharedTile(ctx, key); ok {
			return b, nil
		}
		// One pod fetches a given tile for a given frame; the others wait briefly for it instead of asking NOAA too.
		if lock := s.lock(ctx, key); !lock {
			if b, ok := s.waitShared(ctx, key); ok {
				return b, nil
			}
		}
		b, err := s.fetchTile(ctx, rs, frame, z, x, y)
		if err != nil {
			return nil, err
		}
		s.storeSharedTile(ctx, key, b)
		return b, nil
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	b := v.([]byte)
	s.localPut(key, b)
	return b, frame, nil
}

// regionsFor lists the mosaics whose box meets the tile.
func regionsFor(z, x, y int) []region {
	w, s, e, n := tileLonLat(z, x, y)
	var out []region
	for _, r := range regions {
		if w < r.e && e > r.w && s < r.n && n > r.s {
			out = append(out, r)
		}
	}
	return out
}

// tileLonLat is the tile's box in degrees.
func tileLonLat(z, x, y int) (w, s, e, n float64) {
	nn := float64(int(1) << z)
	w = float64(x)/nn*360 - 180
	e = float64(x+1)/nn*360 - 180
	n = math.Atan(math.Sinh(math.Pi*(1-2*float64(y)/nn))) * 180 / math.Pi
	s = math.Atan(math.Sinh(math.Pi*(1-2*float64(y+1)/nn))) * 180 / math.Pi
	return
}

// tileMercator is the tile's box in Web Mercator metres (EPSG:3857): minx, miny, maxx, maxy.
func tileMercator(z, x, y int) (minx, miny, maxx, maxy float64) {
	const world = 20037508.342789244
	res := 2 * world / float64(int(1)<<z)
	minx = -world + float64(x)*res
	maxx = minx + res
	maxy = world - float64(y)*res
	miny = maxy - res
	return
}

func (s *Service) fetchTile(ctx context.Context, rs []region, frame time.Time, z, x, y int) ([]byte, error) {
	var out *image.NRGBA
	for _, r := range rs {
		img, err := s.fetchRegion(ctx, r, frame, z, x, y)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = img
			continue
		}
		draw.Draw(out, out.Bounds(), img, image.Point{}, draw.Over)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fetchRegion asks NOAA for one mosaic's picture of the tile. The server reprojects to Web Mercator itself when asked (EPSG:3857, which its
// capabilities do not list but it answers); if it ever stops, the native EPSG:4326 picture is requested instead and resampled here row by row.
func (s *Service) fetchRegion(ctx context.Context, r region, frame time.Time, z, x, y int) (*image.NRGBA, error) {
	if s.cfg.Now().UnixNano() >= s.prefer4326.Load() {
		img, err := s.fetchMercator(ctx, r, frame, z, x, y)
		if err == nil {
			return img, nil
		}
		if !errors.Is(err, errNotAnImage) {
			return nil, err
		}
		s.prefer4326.Store(s.cfg.Now().Add(10 * time.Minute).UnixNano())
	}
	return s.fetchNative(ctx, r, frame, z, x, y)
}

var errNotAnImage = errors.New("radar: upstream did not return an image")

func (s *Service) wmsURL(r region, frame time.Time, extra url.Values) string {
	q := url.Values{}
	q.Set("service", "WMS")
	q.Set("request", "GetMap")
	q.Set("layers", r.layer)
	q.Set("styles", "")
	q.Set("format", "image/png")
	q.Set("transparent", "true")
	q.Set("time", frame.UTC().Format("2006-01-02T15:04:05.000Z"))
	for k, v := range extra {
		q[k] = v
	}
	return fmt.Sprintf("%s/%s/%s/ows?%s", s.cfg.BaseURL, r.workspace, r.layer, q.Encode())
}

func (s *Service) fetchMercator(ctx context.Context, r region, frame time.Time, z, x, y int) (*image.NRGBA, error) {
	minx, miny, maxx, maxy := tileMercator(z, x, y)
	u := s.wmsURL(r, frame, url.Values{
		"version": {"1.3.0"}, "crs": {"EPSG:3857"},
		"bbox":  {fmt.Sprintf("%.4f,%.4f,%.4f,%.4f", minx, miny, maxx, maxy)},
		"width": {strconv.Itoa(TileSize)}, "height": {strconv.Itoa(TileSize)},
	})
	body, err := s.get(ctx, u, maxUpstream)
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, errNotAnImage
	}
	if img.Bounds().Dx() != TileSize || img.Bounds().Dy() != TileSize {
		return nil, errNotAnImage
	}
	return toNRGBA(img), nil
}

// nativeHeight is the height of the EPSG:4326 picture requested for resampling: twice the tile, so the row resample (latitude is not linear in
// Mercator) loses little even at the coarse zooms.
const nativeHeight = 2 * TileSize

func (s *Service) fetchNative(ctx context.Context, r region, frame time.Time, z, x, y int) (*image.NRGBA, error) {
	w, south, e, n := tileLonLat(z, x, y)
	u := s.wmsURL(r, frame, url.Values{
		"version": {"1.1.1"}, "srs": {"EPSG:4326"},
		"bbox":  {fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", w, south, e, n)},
		"width": {strconv.Itoa(TileSize)}, "height": {strconv.Itoa(nativeHeight)},
	})
	body, err := s.get(ctx, u, maxUpstream)
	if err != nil {
		return nil, err
	}
	src, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, errNotAnImage
	}
	return ResampleRows(toNRGBA(src), z, y), nil
}

// ResampleRows turns an EPSG:4326 picture of a tile's lon/lat box into the Web Mercator tile: columns map one to one (longitude is linear in both);
// each output row takes the source row at its own latitude. src spans the tile's box top to bottom.
func ResampleRows(src *image.NRGBA, z, y int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, TileSize, TileSize))
	nn := float64(int(1) << z)
	_, south, _, north := tileLonLat(z, 0, y)
	sh := src.Bounds().Dy()
	sw := src.Bounds().Dx()
	for row := 0; row < TileSize; row++ {
		// The row's latitude in Mercator: the tile's y fraction (row centre) back to degrees.
		lat := math.Atan(math.Sinh(math.Pi*(1-2*(float64(y)+(float64(row)+0.5)/TileSize)/nn))) * 180 / math.Pi
		fr := (north - lat) / (north - south)
		sy := int(fr * float64(sh))
		if sy < 0 {
			sy = 0
		}
		if sy >= sh {
			sy = sh - 1
		}
		for col := 0; col < TileSize; col++ {
			sx := col * sw / TileSize
			out.SetNRGBA(col, row, src.NRGBAAt(sx, sy))
		}
	}
	return out
}

func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok {
		return n
	}
	out := image.NewNRGBA(img.Bounds())
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

// get makes one bounded request to NOAA. The request carries no cookie, no user and no referrer; the concurrency is capped per pod.
func (s *Service) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, upstreamTO)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	req.Header.Set("Accept", "image/png,text/xml,*/*;q=0.5")
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: answer over %d bytes", ErrUnavailable, limit)
	}
	return b, nil
}

// transparentPNG is what a tile outside every mosaic is.
var transparentPNG = func() []byte {
	var buf bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, TileSize, TileSize))
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}()

// ---- caches

func (s *Service) localGet(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.local[key]
	if !ok || s.cfg.Now().Sub(t.at) > tileTTL {
		return nil, false
	}
	return t.png, true
}

func (s *Service) localPut(key string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	if len(s.local) >= localMax {
		for k, t := range s.local { // drop the expired, and if none is, the oldest
			if now.Sub(t.at) > tileTTL {
				delete(s.local, k)
			}
		}
		if len(s.local) >= localMax {
			var oldK string
			var oldT time.Time
			for k, t := range s.local {
				if oldK == "" || t.at.Before(oldT) {
					oldK, oldT = k, t.at
				}
			}
			delete(s.local, oldK)
		}
	}
	s.local[key] = localTile{png: b, at: now}
}

func (s *Service) rkey(parts ...string) string {
	p := s.cfg.Prefix
	if p == "" {
		p = "rivolt"
	}
	return p + ":radar:" + strings.Join(parts, ":")
}

const redisOp = 300 * time.Millisecond

func (s *Service) sharedTile(ctx context.Context, key string) ([]byte, bool) {
	if s.cfg.Redis == nil {
		return nil, false
	}
	c, cancel := context.WithTimeout(ctx, redisOp)
	defer cancel()
	b, err := s.cfg.Redis.Get(c, s.rkey("tile", key)).Bytes()
	return b, err == nil && len(b) > 0
}

func (s *Service) storeSharedTile(ctx context.Context, key string, b []byte) {
	if s.cfg.Redis == nil {
		return
	}
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisOp)
	defer cancel()
	_ = s.cfg.Redis.Set(c, s.rkey("tile", key), b, tileTTL).Err()
}

// lock reports whether this pod won the right to fetch the tile. Without Redis, or when Redis cannot answer, every pod fetches (fail open).
func (s *Service) lock(ctx context.Context, key string) bool {
	if s.cfg.Redis == nil {
		return true
	}
	c, cancel := context.WithTimeout(ctx, redisOp)
	defer cancel()
	ok, err := s.cfg.Redis.SetNX(c, s.rkey("lock", key), "1", lockTTL).Result()
	return err != nil || ok
}

// waitShared polls for another pod's fetch of the tile for a short time.
func (s *Service) waitShared(ctx context.Context, key string) ([]byte, bool) {
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-deadline.C:
			return nil, false
		case <-tick.C:
			if b, ok := s.sharedTile(ctx, key); ok {
				return b, true
			}
		}
	}
}

func (s *Service) waitFrames(ctx context.Context) (Frames, bool) {
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return Frames{}, false
		case <-deadline.C:
			return Frames{}, false
		case <-tick.C:
			if f, ok := s.sharedFrames(ctx); ok {
				return f, true
			}
		}
	}
}

func (s *Service) sharedFrames(ctx context.Context) (Frames, bool) {
	if s.cfg.Redis == nil {
		return Frames{}, false
	}
	c, cancel := context.WithTimeout(ctx, redisOp)
	defer cancel()
	v, err := s.cfg.Redis.Get(c, s.rkey("frames")).Result()
	if err != nil {
		return Frames{}, false
	}
	var unix []int64
	for _, p := range strings.Split(v, ",") {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return Frames{}, false
		}
		unix = append(unix, n)
	}
	if len(unix) == 0 {
		return Frames{}, false
	}
	f := Frames{}
	for _, n := range unix {
		f.Recent = append(f.Recent, time.Unix(n, 0).UTC())
	}
	f.Latest = f.Recent[len(f.Recent)-1]
	if s.cfg.Now().Sub(f.Latest) > staleAfter {
		return Frames{}, false
	}
	return f, true
}

func (s *Service) storeSharedFrames(ctx context.Context, f Frames) {
	if s.cfg.Redis == nil || len(f.Recent) == 0 {
		return
	}
	parts := make([]string, len(f.Recent))
	for i, t := range f.Recent {
		parts[i] = strconv.FormatInt(t.Unix(), 10)
	}
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisOp)
	defer cancel()
	_ = s.cfg.Redis.Set(c, s.rkey("frames"), strings.Join(parts, ","), frameTTL).Err()
}
