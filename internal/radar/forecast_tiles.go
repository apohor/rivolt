package radar

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
	"sync"
	"time"
)

// Forecast radar tiles for the planner map: rendered here, on demand, from the same hourly grids the leg tags read (no second pipeline, no storage).
// A tile is a pure function of (cycle, hour, z, x, y), so its URL never changes meaning and it can be cached for good.

const (
	// ForecastMinZoom and ForecastMaxZoom bound the tiles: below 3 a continent-sized picture says nothing, above 7 a tile pixel (1.2 km) is finer
	// than the 3 km grid.
	ForecastMinZoom = 3
	ForecastMaxZoom = 7
	// DefaultTileCacheBytes caps the PNG bytes a pod keeps.
	DefaultTileCacheBytes = 16 << 20
)

// ErrFeedDown is a forecast that may exist but could not be read now (the radar service or its manifest did not answer): try again, do not
// tell the user the hour is out of reach.
var ErrFeedDown = errors.New("radar: forecast feed unavailable")

// ErrNoForecast is a forecast that cannot be shown: radar off, no usable cycle, or the cycle/hour asked for is not in the manifest (past the
// horizon, or aged out). It is never "dry".
var ErrNoForecast = errors.New("radar: no forecast")

// ForecastHour is one hour of the newest usable cycle.
type ForecastHour struct {
	FH        int   `json:"fh"`
	ValidUnix int64 `json:"valid_unix"`
}

// ForecastHours is what the planner map may ask for.
type ForecastHours struct {
	Cycle       string         `json:"cycle"`
	Credit      string         `json:"credit"`
	Bounds      [4]float64     `json:"bounds"` // west, south, east, north of the grid, degrees
	Hours       []ForecastHour `json:"hours"`
	HorizonUnix int64          `json:"horizon_unix"`
}

// ForecastHours lists the hours of the newest cycle that is not too old. ErrNoForecast when there is none.
func (s *Store) ForecastHours(ctx context.Context) (ForecastHours, error) {
	if s == nil {
		return ForecastHours{}, ErrNoForecast
	}
	m, err := s.manifest(ctx)
	if err != nil {
		return ForecastHours{}, ErrNoForecast
	}
	now := s.now()
	for _, c := range m.Cycles { // newest first
		if c.CycleTime.IsZero() || now.Sub(c.CycleTime) > MaxCycleAge || len(c.Hours) == 0 {
			continue
		}
		hs := append([]int(nil), c.Hours...)
		sort.Ints(hs)
		g, err := s.grid(ctx, c.Cycle, hs[0])
		if err != nil {
			return ForecastHours{}, ErrNoForecast
		}
		out := ForecastHours{Cycle: c.Cycle, Credit: "NOAA RRFS", Bounds: g.bounds()}
		for _, h := range hs {
			v := c.CycleTime.Add(time.Duration(h) * time.Hour).Unix()
			out.Hours = append(out.Hours, ForecastHour{FH: h, ValidUnix: v})
			out.HorizonUnix = v
		}
		return out, nil
	}
	return ForecastHours{}, ErrNoForecast
}

// usable reports whether cycle and hour are in the manifest and the cycle is young enough; err is ErrFeedDown when the manifest cannot be read.
func (s *Store) usable(ctx context.Context, cycle string, fh int) (bool, error) {
	m, err := s.manifest(ctx)
	if err != nil {
		return false, ErrFeedDown
	}
	now := s.now()
	for _, c := range m.Cycles {
		if c.Cycle != cycle {
			continue
		}
		if c.CycleTime.IsZero() || now.Sub(c.CycleTime) > MaxCycleAge {
			return false, nil
		}
		for _, h := range c.Hours {
			if h == fh {
				return true, nil
			}
		}
	}
	return false, nil
}

// Tile is the PNG of one forecast hour: transparent under the light class edge and wherever the grid has no value (outside it, or no data),
// NOAA's colours above. ErrNoForecast when the cycle or hour is not offered, ErrFeedDown when the grid could not be read; a bad address is the caller's to refuse (ParseTilePath-like).
func (s *Store) Tile(ctx context.Context, cycle string, fh, z, x, y int) ([]byte, error) {
	if s == nil {
		return nil, ErrNoForecast
	}
	if z < ForecastMinZoom || z > ForecastMaxZoom || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		return nil, errors.New("radar: bad tile")
	}
	if ok, err := s.usable(ctx, cycle, fh); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNoForecast
	}
	key := fmt.Sprintf("%s/%03d/%d/%d/%d", cycle, fh, z, x, y)
	if b, ok := s.tileGet(key); ok {
		s.tileCount("hit")
		return b, nil
	}
	v, err, _ := s.tileFlight.Do(key, func() (any, error) {
		// The render is shared by every caller of this tile: it does not die with the first caller's request.
		g, err := s.grid(context.WithoutCancel(ctx), cycle, fh)
		if err != nil {
			return nil, ErrFeedDown
		}
		b, opaque := g.renderTile(z, x, y)
		if opaque {
			s.tileCount("render")
		} else {
			s.tileCount("absent")
		}
		s.tilePut(key, b)
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (s *Store) tileCount(outcome string) {
	if s.OnTile != nil {
		s.OnTile(outcome)
	}
}

func (s *Store) tileGet(key string) ([]byte, bool) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	el, ok := s.tiles[key]
	if !ok {
		return nil, false
	}
	s.tlru.MoveToFront(el)
	return el.Value.(*tileEntry).b, true
}

func (s *Store) tilePut(key string, b []byte) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.tiles == nil {
		s.tiles, s.tlru = map[string]*list.Element{}, list.New()
	}
	if _, ok := s.tiles[key]; ok {
		return
	}
	s.tiles[key] = s.tlru.PushFront(&tileEntry{key: key, b: b})
	s.tsize += len(b) + len(key)
	for s.tsize > DefaultTileCacheBytes && s.tlru.Len() > 1 {
		old := s.tlru.Back()
		e := old.Value.(*tileEntry)
		s.tlru.Remove(old)
		delete(s.tiles, e.key)
		s.tsize -= len(e.b) + len(e.key)
	}
}

// TileCachedBytes is the PNG bytes held, for a gauge.
func (s *Store) TileCachedBytes() int {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	return s.tsize
}

type tileEntry struct {
	key string
	b   []byte
}

type tileState struct {
	tmu   sync.Mutex
	tiles map[string]*list.Element
	tlru  *list.List
	tsize int
}

// The palette is the live legend's (web next-plan.css .radar-strip): light to heavy in NOAA's colours.
var rampColors = []struct {
	min float64
	c   color.NRGBA
}{
	{15, color.NRGBA{0x00, 0xed, 0xed, 0xd0}},
	{25, color.NRGBA{0x00, 0xc8, 0x00, 0xd8}},
	{35, color.NRGBA{0xff, 0xff, 0x00, 0xe0}},
	{45, color.NRGBA{0xff, 0x8f, 0x00, 0xe0}},
	{50, color.NRGBA{0xff, 0x00, 0x00, 0xe0}},
	{60, color.NRGBA{0xff, 0x00, 0xff, 0xe0}},
}

func rampAt(dbz float64) (color.NRGBA, bool) {
	var out color.NRGBA
	ok := false
	for _, r := range rampColors {
		if dbz >= r.min {
			out, ok = r.c, true
		}
	}
	return out, ok
}

// renderTile draws one 256x256 Web Mercator tile from the grid: each pixel takes the cell it falls in (nearest). No value (outside the grid, or the
// no-data byte) stays transparent; it is never drawn as a dry colour. opaque is whether any pixel was drawn.
func (g *Grid) renderTile(z, x, y int) ([]byte, bool) {
	const n = 256
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	world := float64(n) * math.Pow(2, float64(z))
	lons := make([]float64, n)
	for i := range lons {
		lons[i] = (float64(x*n+i)+0.5)/world*360 - 180
	}
	h := g.Header
	opaque := false
	for j := 0; j < n; j++ {
		my := math.Pi * (1 - 2*(float64(y*n+j)+0.5)/world)
		lat := math.Atan(math.Sinh(my)) * 180 / math.Pi
		for i := 0; i < n; i++ {
			col, row, ok := g.cell(lat, lons[i])
			if !ok {
				continue
			}
			v := g.dbz[row*h.NX+col]
			if v == NoData {
				continue
			}
			if c, ok := rampAt(float64(v)*h.DBZStep + h.DBZOffset); ok {
				img.SetNRGBA(i, j, c)
				opaque = true
			}
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	_ = enc.Encode(&buf, img)
	return buf.Bytes(), opaque
}

// bounds is the grid's lon/lat box: the edge of the grid walked cell by cell and inverse-projected.
func (g *Grid) bounds() [4]float64 {
	h := g.Header
	w, s, e, nn := 180.0, 90.0, -180.0, -90.0
	add := func(c, r float64) {
		lat, lon := g.proj.inverse(h.X0+c*h.DX, h.Y0+r*h.DY)
		w, e = math.Min(w, lon), math.Max(e, lon)
		s, nn = math.Min(s, lat), math.Max(nn, lat)
	}
	const steps = 32
	for i := 0; i <= steps; i++ {
		f := float64(i) / steps
		add(f*float64(h.NX), 0)
		add(f*float64(h.NX), float64(h.NY))
		add(0, f*float64(h.NY))
		add(float64(h.NX), f*float64(h.NY))
	}
	r := func(v float64) float64 { return math.Round(v*100) / 100 }
	return [4]float64{r(w), r(s), r(e), r(nn)}
}
