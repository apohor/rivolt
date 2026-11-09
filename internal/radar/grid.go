// Package radar answers "is it likely to rain or snow where this truck will be, when it gets there?" from NOAA RRFS forecast composite
// reflectivity. A build job (rivolt-infra apps/platform/maps/radar) turns each forecast hour into one small grid file and serves it inside the
// cluster; this package reads those files, projects a point onto the grid, and reports the strongest echo around it at the hours either side of
// an arrival time. Nothing here talks to NOAA, and nothing about a user leaves the server.
package radar

import (
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// NoData is the reserved byte of the dBZ plane: no value there. It is never read as "dry".
const NoData = 255

// Header is the JSON header of a grid file (see radar_build.py).
type Header struct {
	V         int       `json:"v"`
	Domain    string    `json:"domain"`
	Cycle     string    `json:"cycle"`
	FH        int       `json:"fh"`
	CycleTime time.Time `json:"cycle_time"`
	ValidTime time.Time `json:"valid_time"`
	NX        int       `json:"nx"`
	NY        int       `json:"ny"`
	X0        float64   `json:"x0"` // projected metres of the grid's top-left corner
	Y0        float64   `json:"y0"`
	DX        float64   `json:"dx"` // metres per column
	DY        float64   `json:"dy"` // metres per row (negative: row 0 is the top)
	DBZOffset float64   `json:"dbz_offset"`
	DBZStep   float64   `json:"dbz_step"`
	NoData    int       `json:"nodata"`
	Planes    []string  `json:"planes"`
	Proj      LCC       `json:"proj"`
}

// Grid is one forecast hour: the dBZ plane as the file has it, and the snow flags as a bit set (a missing snow flag reads as no snow claim).
type Grid struct {
	Header Header
	dbz    []byte
	snow   []uint64
	proj   lccProjector
}

// Bytes is the memory a decoded grid holds, for the cache's size cap.
func (g *Grid) Bytes() int { return len(g.dbz) + 8*len(g.snow) + 512 }

// Decode reads a gzip grid file.
func Decode(r io.Reader) (*Grid, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("radar: not gzip: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("radar: read: %w", err)
	}
	if len(raw) < 8 || string(raw[:4]) != "RDR1" {
		return nil, errors.New("radar: bad magic")
	}
	hl := int(binary.BigEndian.Uint32(raw[4:8]))
	if hl <= 0 || 8+hl > len(raw) {
		return nil, errors.New("radar: bad header length")
	}
	var h Header
	if err := json.Unmarshal(raw[8:8+hl], &h); err != nil {
		return nil, fmt.Errorf("radar: header: %w", err)
	}
	if h.V != 1 || h.NX <= 0 || h.NY <= 0 || h.NX > 10000 || h.NY > 10000 || h.DX == 0 || h.DY == 0 || h.NoData != NoData || h.DBZStep <= 0 {
		return nil, errors.New("radar: unsupported header")
	}
	n := h.NX * h.NY
	body := raw[8+hl:]
	if len(body) != 2*n {
		return nil, fmt.Errorf("radar: planes are %d bytes, want %d", len(body), 2*n)
	}
	proj, err := newLCC(h.Proj)
	if err != nil {
		return nil, err
	}
	g := &Grid{Header: h, dbz: append([]byte(nil), body[:n]...), snow: make([]uint64, (n+63)/64), proj: proj}
	for i, b := range body[n:] {
		if b == 1 {
			g.snow[i>>6] |= 1 << (uint(i) & 63)
		}
	}
	return g, nil
}

// cell is the grid index of a point, or false outside the grid.
func (g *Grid) cell(lat, lon float64) (col, row int, ok bool) {
	x, y := g.proj.forward(lat, lon)
	c := (x - g.Header.X0) / g.Header.DX
	r := (y - g.Header.Y0) / g.Header.DY
	if math.IsNaN(c) || math.IsNaN(r) || c < 0 || r < 0 || c >= float64(g.Header.NX) || r >= float64(g.Header.NY) {
		return 0, 0, false
	}
	return int(c), int(r), true
}

// Echo is what a point sees at one forecast hour: the strongest dBZ in the 3x3 cells around it, whether any of them says snow, and whether any
// cell had a value at all.
type Echo struct {
	DBZ  float64
	Snow bool
	OK   bool
}

// At is the echo around a point, or OK=false outside the grid or where every one of the 3x3 cells has no data.
func (g *Grid) At(lat, lon float64) Echo {
	col, row, ok := g.cell(lat, lon)
	if !ok {
		return Echo{}
	}
	best := -1
	var snow bool
	for dr := -1; dr <= 1; dr++ {
		for dc := -1; dc <= 1; dc++ {
			c, r := col+dc, row+dr
			if c < 0 || r < 0 || c >= g.Header.NX || r >= g.Header.NY {
				continue
			}
			i := r*g.Header.NX + c
			v := int(g.dbz[i])
			if v == NoData {
				continue
			}
			if v > best {
				best = v
			}
			if g.snow[i>>6]&(1<<(uint(i)&63)) != 0 {
				snow = true
			}
		}
	}
	if best < 0 {
		return Echo{}
	}
	return Echo{DBZ: float64(best)*g.Header.DBZStep + g.Header.DBZOffset, Snow: snow, OK: true}
}
