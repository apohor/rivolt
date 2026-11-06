package chargers

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/maptile"
	pm "github.com/protomaps/go-pmtiles/pmtiles"
)

// station is one synthetic feature at a z14 tile.
type station struct {
	x, y  uint32
	props map[string]any
}

func archiveAt(t *testing.T, all []station) []byte {
	t.Helper()
	var entries []pm.EntryV3
	var data []byte
	for _, s := range all {
		tile := maptile.New(s.x, s.y, 14)
		f := geojson.NewFeature(tile.Bound().Center())
		f.Properties = geojson.Properties(s.props)
		layer := &mvt.Layer{Name: "chargers", Version: 2, Extent: 4096, Features: []*geojson.Feature{f}}
		layer.ProjectToTile(tile)
		raw, err := mvt.Marshal(mvt.Layers{layer})
		if err != nil {
			t.Fatal(err)
		}
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		entries = append(entries, pm.EntryV3{TileID: pm.ZxyToID(14, tile.X, tile.Y), Offset: uint64(len(data)), Length: uint32(gz.Len()), RunLength: 1})
		data = append(data, gz.Bytes()...)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].TileID < entries[j].TileID })
	root := pm.SerializeEntries(entries, pm.Gzip)
	h := pm.HeaderV3{
		SpecVersion: 3, RootOffset: pm.HeaderV3LenBytes, RootLength: uint64(len(root)),
		TileDataOffset: uint64(pm.HeaderV3LenBytes + len(root)), TileDataLength: uint64(len(data)),
		InternalCompression: pm.Gzip, TileCompression: pm.Gzip, TileType: pm.Mvt, MinZoom: 14, MaxZoom: 14,
	}
	return append(append(pm.SerializeHeader(h), root...), data...)
}

func loaded(t *testing.T, file []byte) *Archive {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(file) }))
	t.Cleanup(srv.Close)
	a := New(srv.URL)
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}

// A straight west-to-east route along one tile row: 100 tiles, about 2.4 km each at this latitude, with stations of every kind
// at known tiles. Distances along the route are then checkable, and the order is not the file's.
func layerFixture(t *testing.T) (*Archive, [][2]float64) {
	const row = 6000
	const x0 = 1000
	mk := func(i int, off uint32, p map[string]any) station {
		return station{x: x0 + uint32(i), y: row + off, props: p}
	}
	all := []station{
		mk(80, 0, map[string]any{"@id": "h80", "facility_type": "HOTEL", "l2_count": 2, "asof": "2026-10-20"}),
		mk(20, 0, map[string]any{"@id": "d20", "dcfc_count": 4, "asof": "2026-10-20"}),
		mk(50, 0, map[string]any{"@id": "c50", "facility_type": "CAMPGROUND", "hookup_amps": 50, "asof": "2026-10-21"}),
		mk(35, 1, map[string]any{"@id": "h35", "facility_type": "HOTEL", "l2_count": 1}), // one tile off the row: about 2 km
		mk(60, 40, map[string]any{"@id": "far", "dcfc_count": 2}),                        // 100 km off: outside the corridor
		mk(20, 2, map[string]any{"@id": "d20b", "dcfc_count": 1}),                        // same distance along as d20, further off
		mk(5, 0, map[string]any{"@id": "d5", "dcfc_count": 2}),                           // near the start
	}
	a := loaded(t, archiveAt(t, all))
	path := make([][2]float64, 0, 100)
	for i := 0; i < 100; i++ {
		c := maptile.New(uint32(x0+i), row, 14).Bound().Center()
		path = append(path, [2]float64{c[1], c[0]})
	}
	return a, path
}

func ids(r LayerResult) []string {
	var out []string
	for _, p := range r.POIs {
		out = append(out, p.EntityID)
	}
	return out
}

func TestLayersComeBackInRouteOrderWithDistances(t *testing.T) {
	a, path := layerFixture(t)
	r, err := a.QueryLayers(path, []string{"dcfc", "hotels", "camp"}, LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 10}})
	if err != nil {
		t.Fatal(err)
	}
	got := ids(r)
	want := []string{"d5", "d20", "d20b", "h35", "c50", "h80"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
	if r.Total != 6 || r.NextAfterKm != nil {
		t.Errorf("total %d next %v", r.Total, r.NextAfterKm)
	}
	for i := 1; i < len(r.POIs); i++ {
		if r.POIs[i].AlongKm < r.POIs[i-1].AlongKm {
			t.Errorf("not sorted along the route at %d", i)
		}
	}
	by := map[string]CorridorPOI{}
	for _, p := range r.POIs {
		by[p.EntityID] = p
	}
	if d := by["d20b"].OffKm; d < 3 || d > 6 {
		t.Errorf("d20b is about two tiles off the route, offKm = %v", d)
	}
	if d := by["d20"].OffKm; d > 0.05 {
		t.Errorf("d20 sits on the route, offKm = %v", d)
	}
	if by["c50"].AlongKm < 85 || by["c50"].AlongKm > 93 {
		t.Errorf("c50 is 50 tiles along (about 89 km at this latitude), alongKm = %v", by["c50"].AlongKm)
	}
	if r.AsOf != "2026-10-21" {
		t.Errorf("asof = %q, want the newest export date among the stations", r.AsOf)
	}
	// Kinds names the requested layers a station belongs to, and only those.
	if k := by["h35"].Kinds; len(k) != 1 || k[0] != "hotels" {
		t.Errorf("h35 kinds = %v", k)
	}
	if k := by["d5"].Kinds; len(k) != 1 || k[0] != "dcfc" {
		t.Errorf("d5 kinds = %v", k)
	}
	for _, p := range r.POIs {
		if p.EntityID == "far" {
			t.Error("a station 100 km off the route was returned")
		}
	}
}

// Only the layers asked for come back; the same scan serves several.
func TestOnlyTheRequestedKindsAreReturned(t *testing.T) {
	a, path := layerFixture(t)
	opts := LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 10}}
	r, err := a.QueryLayers(path, []string{"camp"}, opts)
	if err != nil || len(r.POIs) != 1 || r.POIs[0].EntityID != "c50" {
		t.Errorf("camp only: %v, %v", ids(r), err)
	}
	r, _ = a.QueryLayers(path, []string{"hotels"}, opts)
	if got := ids(r); len(got) != 2 || got[0] != "h35" || got[1] != "h80" {
		t.Errorf("hotels only: %v", got)
	}
}

func TestBadKindsAreRefused(t *testing.T) {
	a, path := layerFixture(t)
	for _, kinds := range [][]string{nil, {}, {"all"}, {"dcfc", "nope"}, {"dcfc", "l2", "hotels", "camp", "dcfc"}} {
		if _, err := a.QueryLayers(path, kinds, LayerOptions{}); !errors.Is(err, ErrBadKinds) {
			t.Errorf("%v: err = %v, want ErrBadKinds", kinds, err)
		}
	}
}

// Paging walks the whole result without losing or repeating a station, including two that share a distance along the route
// (d20 and d20b), which a page must not split.
func TestPagingAlongTheRouteLosesAndRepeatsNothing(t *testing.T) {
	a, path := layerFixture(t)
	var all []string
	after := 0.0
	for pages := 0; pages < 10; pages++ {
		r, err := a.QueryLayers(path, []string{"dcfc", "hotels", "camp"}, LayerOptions{
			QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 10}, AfterKm: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if r.Total != 6 {
			t.Fatalf("total changed to %d", r.Total)
		}
		all = append(all, ids(r)...)
		if r.NextAfterKm == nil {
			break
		}
		after = *r.NextAfterKm
	}
	want := []string{"d5", "d20", "d20b", "h35", "c50", "h80"}
	if len(all) != len(want) {
		t.Fatalf("paged %v, want %v", all, want)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("paged %v, want %v", all, want)
		}
	}
}

// Accept narrows the result before paging, so Total counts only what passes.
func TestAcceptFiltersBeforePagingAndCounting(t *testing.T) {
	a, path := layerFixture(t)
	r, err := a.QueryLayers(path, []string{"dcfc", "hotels", "camp"}, LayerOptions{
		QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 10},
		Accept:               func(p POI) bool { return p.EntityID == "h35" || p.EntityID == "d5" },
	})
	if err != nil || r.Total != 2 || len(r.POIs) != 2 {
		t.Errorf("accepted %v total %d, %v", ids(r), r.Total, err)
	}
}
