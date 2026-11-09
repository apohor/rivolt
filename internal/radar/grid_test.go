package radar

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
	"time"
)

var testProj = LCC{Type: "lcc", Lat0: 38.5, Lon0: -97.5, Lat1: 38.5, Lat2: 38.5, RadiusM: 6371229}

// Golden vectors from gdaltransform (GDAL 3, +proj=lcc +lat_0=38.5 +lon_0=-97.5 +lat_1=38.5 +lat_2=38.5 +R=6371229).
func TestProjectionMatchesGDAL(t *testing.T) {
	p, err := newLCC(testProj)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ lat, lon, x, y float64 }{
		{40.0, -100.0, -213005.614423898, 169710.584324835},
		{41.88, -87.63, 817041.598525767, 419923.706287942},
		{42.36, -71.06, 2147721.04686192, 740183.080870074},
		{30.0, -90.0, 729178.208512805, -918854.381115411},
		{47.6, -122.3, -1861652.21885075, 1268679.65778891},
	} {
		x, y := p.forward(c.lat, c.lon)
		if math.Abs(x-c.x) > 1 || math.Abs(y-c.y) > 1 {
			t.Errorf("(%v,%v) = (%.1f, %.1f), want (%.1f, %.1f)", c.lat, c.lon, x, y, c.x, c.y)
		}
	}
}

// synthFile builds a small grid file centred on the projection origin: 20x20 cells of 3 km, top-left at (-30 km, +30 km).
func synthFile(t *testing.T, mut func(dbz, snow []byte)) []byte {
	t.Helper()
	const n = 20
	h := Header{V: 1, Domain: "conus", Cycle: "2026110818", FH: 3, NX: n, NY: n, X0: -30000, Y0: 30000, DX: 3000, DY: -3000,
		DBZOffset: -32, DBZStep: 0.5, NoData: NoData, Planes: []string{"dbz", "csnow"}, Proj: testProj,
		CycleTime: time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC), ValidTime: time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC)}
	dbz := bytes.Repeat([]byte{0}, n*n)
	snow := make([]byte, n*n)
	mut(dbz, snow)
	hj, _ := json.Marshal(h)
	var raw bytes.Buffer
	raw.WriteString("RDR1")
	_ = binary.Write(&raw, binary.BigEndian, uint32(len(hj)))
	raw.Write(hj)
	raw.Write(dbz)
	raw.Write(snow)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	return gz.Bytes()
}

func synth(t *testing.T, mut func(dbz, snow []byte)) *Grid {
	t.Helper()
	g, err := Decode(bytes.NewReader(synthFile(t, mut)))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func enc(dbz float64) byte { return byte((dbz + 32) / 0.5) }

func TestAtFindsStrongestNeighbourAndSnow(t *testing.T) {
	// the origin (38.5, -97.5) projects to (0,0): column 10, row 10.
	g := synth(t, func(dbz, snow []byte) {
		dbz[10*20+11] = enc(30)
		snow[10*20+11] = 1
		dbz[9*20+9] = enc(12)
	})
	e := g.At(38.5, -97.5)
	if !e.OK || e.DBZ != 30 || !e.Snow {
		t.Fatalf("got %+v", e)
	}
}

func TestAtNoDataIsNotDry(t *testing.T) {
	g := synth(t, func(dbz, snow []byte) {
		for i := range dbz {
			dbz[i] = NoData
		}
	})
	if e := g.At(38.5, -97.5); e.OK {
		t.Fatalf("no-data cells must not read as dry: %+v", e)
	}
	dry := synth(t, func(dbz, snow []byte) {})
	if e := dry.At(38.5, -97.5); !e.OK || e.DBZ != -32 {
		t.Fatalf("zero cells are dry: %+v", e)
	}
}

func TestAtOutsideGrid(t *testing.T) {
	g := synth(t, func(dbz, snow []byte) {})
	if g.At(47.6, -122.3).OK {
		t.Fatal("Seattle is outside the small grid")
	}
}

func TestDecodeRejectsBadInput(t *testing.T) {
	if _, err := Decode(bytes.NewReader([]byte("nope"))); err == nil {
		t.Fatal("not gzip")
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("XXXX\x00\x00\x00\x02{}"))
	_ = zw.Close()
	if _, err := Decode(&gz); err == nil {
		t.Fatal("bad magic")
	}
}
