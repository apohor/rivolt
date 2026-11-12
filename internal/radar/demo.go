package radar

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The signed-out demo's radar. A visitor with no account may see the radar over the sample trips, and nothing else: the tiles are a fixed set (the
// tiles that meet the sample trips' routes, at a fixed zoom range), fetched from NOAA by a warming loop in the server, never on a visitor's request.
// A request reads the cache only: a tile that is not there yet is an empty tile, so the visitors cannot make the server spend an upstream call, a
// render or a CPU slice beyond a Redis read. Nothing about the visitor is used: no cookie, no account, no query beyond the frame time.
const (
	// DemoMinZoom and DemoMaxZoom are the zooms the demo serves; the map overzooms past DemoMaxZoom.
	DemoMinZoom = 4
	DemoMaxZoom = 7
	// DemoMaxTiles caps the warmed set (all zooms), so a sample trip added later cannot make the warming loop a load.
	DemoMaxTiles = 160
	// demoPadDeg widens each sample route's box so the tiles beside the route are there when the map is panned a little.
	demoPadDeg    = 0.75
	demoTTL       = 45 * time.Minute // a warmed frame outlives a missed round or two; a frame older than staleAfter is not served
	demoWarmEvery = 5 * time.Minute
	demoFrameRead = 15 * time.Second // how long a pod keeps the warmed frame it read
	demoTileMax   = 64 << 10
)

// Box is a longitude/latitude box in degrees.
type Box struct{ W, S, E, N float64 }

type demoTile struct{ z, x, y int }

// Demo serves and warms the demo radar.
type Demo struct {
	svc     *Service
	tiles   []demoTile
	set     map[demoTile]struct{}
	observe func(result string)
	log     *slog.Logger

	mu       sync.Mutex
	frame    int64 // the warmed frame this pod last read
	frameAt  time.Time
	localT   map[string][]byte // tiles of the warmed frame when there is no Redis
	localExp time.Time
}

// NewDemo returns the demo radar over svc, for the tiles that meet any of boxes. observe is called with "hit" or "miss" for each tile answered
// (the caller adds "limited"); it may be nil.
func NewDemo(svc *Service, boxes []Box, observe func(result string), log *slog.Logger) *Demo {
	if log == nil {
		log = slog.Default()
	}
	d := &Demo{svc: svc, set: map[demoTile]struct{}{}, observe: observe, log: log, localT: map[string][]byte{}}
	for z := DemoMinZoom; z <= DemoMaxZoom; z++ {
		for _, b := range boxes {
			x0, y1 := lonLatTile(b.W-demoPadDeg, b.S-demoPadDeg, z)
			x1, y0 := lonLatTile(b.E+demoPadDeg, b.N+demoPadDeg, z)
			for x := x0; x <= x1; x++ {
				for y := y0; y <= y1; y++ {
					t := demoTile{z, x, y}
					if _, dup := d.set[t]; dup {
						continue
					}
					if len(d.tiles) >= DemoMaxTiles {
						log.Warn("demo radar: tile set capped", "cap", DemoMaxTiles)
						return d
					}
					d.set[t] = struct{}{}
					d.tiles = append(d.tiles, t)
				}
			}
		}
	}
	return d
}

// Len is the number of tiles in the set.
func (d *Demo) Len() int { return len(d.tiles) }

// In reports whether the tile is one the demo serves.
func (d *Demo) In(z, x, y int) bool { _, ok := d.set[demoTile{z, x, y}]; return ok }

func lonLatTile(lon, lat float64, z int) (x, y int) {
	n := float64(int(1) << z)
	lat = math.Max(-85.0511, math.Min(85.0511, lat))
	x = int(math.Floor((lon + 180) / 360 * n))
	r := lat * math.Pi / 180
	y = int(math.Floor((1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * n))
	m := int(n) - 1
	return max(0, min(m, x)), max(0, min(m, y))
}

func (d *Demo) frameKey() string { return d.svc.rkey("demo", "frame") }
func (d *Demo) tileKey(frame int64, t demoTile) string {
	return d.svc.rkey("demo", "tile", strconv.FormatInt(frame, 10), strconv.Itoa(t.z), strconv.Itoa(t.x), strconv.Itoa(t.y))
}

// Handler is the demo radar routes, mounted with the prefix stripped (/api/demo/radar):
//
//	GET /frame           -> the warmed frame, in the shape of the signed-in route's answer, with the demo's zoom range
//	GET /{z}/{x}/{y}.png -> the warmed tile; an empty tile when it is not there yet; 404 outside the demo's set
//
// Neither route calls NOAA or renders anything.
func (d *Demo) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		for k, v := range r.URL.Query() {
			if k != "t" || len(v) != 1 {
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			if _, err := strconv.ParseInt(v[0], 10, 64); err != nil {
				http.Error(w, "bad frame", http.StatusBadRequest)
				return
			}
		}
		if r.URL.Path == "/frame" {
			d.serveFrame(w, r)
			return
		}
		z, x, y, ok := ParseTilePath(r.URL.Path)
		if !ok || !d.In(z, x, y) {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "image/png")
		frame, have := d.warmedFrame(r.Context())
		var b []byte
		if have {
			b, have = d.warmedTile(r.Context(), frame, demoTile{z, x, y})
		}
		if have {
			d.count("hit")
			h.Set("X-Radar-Frame", time.Unix(frame, 0).UTC().Format(time.RFC3339))
			h.Set("Cache-Control", "public, max-age=120")
		} else {
			d.count("miss")
			b = transparentPNG
			h.Set("Cache-Control", "public, max-age=15")
		}
		h.Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(b)
	})
}

func (d *Demo) count(result string) {
	if d.observe != nil {
		d.observe(result)
	}
}

func (d *Demo) serveFrame(w http.ResponseWriter, r *http.Request) {
	frame, ok := d.warmedFrame(r.Context())
	if !ok {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("Cache-Control", "public, max-age=15")
		http.Error(w, "radar is not available right now", http.StatusServiceUnavailable)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=30")
	f := time.Unix(frame, 0).UTC()
	h.Set("X-Radar-Frame", f.Format(time.RFC3339))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"frame": f.Format(time.RFC3339), "frame_unix": frame, "frames_unix": []int64{frame},
		"min_zoom": DemoMinZoom, "max_zoom": DemoMaxZoom,
	})
}

// warmedFrame is the frame the warming loop last completed, from shared Redis (or this pod's own warm when there is none).
func (d *Demo) warmedFrame(ctx context.Context) (int64, bool) {
	f, ok := d.readFrame(ctx)
	if ok && d.svc.cfg.Now().Sub(time.Unix(f, 0)) > staleAfter {
		return 0, false // a feed that stopped is not shown as the weather now
	}
	return f, ok
}

func (d *Demo) readFrame(ctx context.Context) (int64, bool) {
	now := d.svc.cfg.Now()
	d.mu.Lock()
	if d.frame != 0 && now.Sub(d.frameAt) < demoFrameRead {
		f := d.frame
		d.mu.Unlock()
		return f, true
	}
	if d.svc.cfg.Redis == nil {
		f, exp := d.frame, d.localExp
		d.mu.Unlock()
		return f, f != 0 && now.Before(exp)
	}
	d.mu.Unlock()
	c, cancel := context.WithTimeout(ctx, redisOp)
	defer cancel()
	n, err := d.svc.cfg.Redis.Get(c, d.frameKey()).Int64()
	if err != nil || n == 0 {
		return 0, false
	}
	d.mu.Lock()
	d.frame, d.frameAt = n, now
	d.mu.Unlock()
	return n, true
}

func (d *Demo) warmedTile(ctx context.Context, frame int64, t demoTile) ([]byte, bool) {
	if d.svc.cfg.Redis == nil {
		d.mu.Lock()
		defer d.mu.Unlock()
		b, ok := d.localT[strconv.FormatInt(frame, 10)+":"+strconv.Itoa(t.z)+":"+strconv.Itoa(t.x)+":"+strconv.Itoa(t.y)]
		return b, ok
	}
	c, cancel := context.WithTimeout(ctx, redisOp)
	defer cancel()
	b, err := d.svc.cfg.Redis.Get(c, d.tileKey(frame, t)).Bytes()
	return b, err == nil && len(b) > 0
}

// Run warms the demo's tiles until ctx ends: once at start, then every few minutes. Across pods one round at a time is enough, so a round is
// taken with a short Redis lock and the others skip it.
func (d *Demo) Run(ctx context.Context) {
	if d.Len() == 0 {
		return
	}
	d.Warm(ctx)
	tick := time.NewTicker(demoWarmEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.Warm(ctx)
		}
	}
}

// Warm runs one round: the newest frame's tiles, then the frame pointer, so a reader never sees a frame whose tiles are not all there. Returns the
// number of tiles stored.
func (d *Demo) Warm(ctx context.Context) int {
	if rc := d.svc.cfg.Redis; rc != nil {
		// One pod warms per round: the lock is held for most of the interval, and a pod that cannot reach Redis warms for itself.
		c, cancel := context.WithTimeout(ctx, redisOp)
		ok, err := rc.SetNX(c, d.svc.rkey("demo", "warm-lock"), "1", demoWarmEvery-time.Minute).Result()
		cancel()
		if err == nil && !ok {
			return 0
		}
	}
	fr, err := d.svc.Frames(ctx)
	if err != nil {
		d.log.Warn("demo radar: no frame", "err", err.Error())
		return 0
	}
	frame := fr.Latest
	if cur, ok := d.warmedFrame(ctx); ok && cur == frame.Unix() {
		return 0 // NOAA has no newer frame: the tiles stored for it are still right
	}
	local := map[string][]byte{}
	stored := 0
	for _, t := range d.tiles {
		if ctx.Err() != nil {
			return stored
		}
		var b []byte
		if rs := regionsFor(t.z, t.x, t.y); len(rs) == 0 {
			b = transparentPNG
		} else if b, err = d.svc.fetchTile(ctx, rs, frame, t.z, t.x, t.y); err != nil {
			d.log.Warn("demo radar: tile", "err", err.Error())
			continue
		}
		if len(b) > demoTileMax {
			continue
		}
		if rc := d.svc.cfg.Redis; rc != nil {
			c, cancel := context.WithTimeout(ctx, redisOp)
			err = rc.Set(c, d.tileKey(frame.Unix(), t), b, demoTTL).Err()
			cancel()
			if err != nil {
				continue
			}
		} else {
			local[strconv.FormatInt(frame.Unix(), 10)+":"+strconv.Itoa(t.z)+":"+strconv.Itoa(t.x)+":"+strconv.Itoa(t.y)] = b
		}
		stored++
	}
	if stored < len(d.tiles)/2 {
		return stored // a mostly failed round is not published; the previous frame stays until it expires
	}
	now := d.svc.cfg.Now()
	if rc := d.svc.cfg.Redis; rc != nil {
		c, cancel := context.WithTimeout(ctx, redisOp)
		_ = rc.Set(c, d.frameKey(), strconv.FormatInt(frame.Unix(), 10), demoTTL).Err()
		cancel()
	}
	d.mu.Lock()
	d.frame, d.frameAt = frame.Unix(), now
	if d.svc.cfg.Redis == nil {
		d.localT, d.localExp = local, now.Add(demoTTL)
	}
	d.mu.Unlock()
	return stored
}
