package chargers

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
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
			QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 10}, AfterKm: after, Limit: 2, Paged: true})
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

// A layer whose stations carry no export date (plain NREL chargers) still reports one: the archive file's Last-Modified. With neither,
// it is empty (the client says nothing about age) and never a guess.
func TestAsOfFallsBackToTheArchivesOwnDate(t *testing.T) {
	file := archiveAt(t, []station{{x: 1000, y: 6000, props: map[string]any{"@id": "d1", "dcfc_count": 2}}, {x: 1010, y: 6000, props: map[string]any{"@id": "d2", "dcfc_count": 2}}})
	path := [][2]float64{}
	for i := 0; i < 20; i++ {
		c := maptile.New(uint32(1000+i), 6000, 14).Bound().Center()
		path = append(path, [2]float64{c[1], c[0]})
	}
	serve := func(lastModified string) *Archive {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if lastModified != "" {
				w.Header().Set("Last-Modified", lastModified)
			}
			_, _ = w.Write(file)
		}))
		t.Cleanup(srv.Close)
		a := New(srv.URL)
		if err := a.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
		return a
	}
	opts := LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 10}}
	r, err := serve("Tue, 20 Oct 2026 04:00:00 GMT").QueryLayers(path, []string{"dcfc"}, opts)
	if err != nil || len(r.POIs) != 2 || r.AsOf != "2026-10-20" {
		t.Errorf("with Last-Modified: %d stations, asof %q, %v", len(r.POIs), r.AsOf, err)
	}
	r, _ = serve("").QueryLayers(path, []string{"dcfc"}, opts)
	if len(r.POIs) != 2 || r.AsOf != "" {
		t.Errorf("with no date anywhere: %d stations, asof %q, want empty", len(r.POIs), r.AsOf)
	}
	// A station's own export date wins over the file's.
	a, p2 := layerFixture(t)
	r, _ = a.QueryLayers(p2, []string{"dcfc", "camp"}, opts)
	if r.AsOf != "2026-10-21" {
		t.Errorf("a station's date should win: %q", r.AsOf)
	}
}

// A long corridor with many more stations than the cap: the capped answer covers the whole route, not its first stretch.
func TestCappedCorridorSpreadsAlongTheWholeRoute(t *testing.T) {
	const row, x0, n = 6000, 1000, 100
	var all []station
	add := func(i int, off int, id string, kw float64) {
		all = append(all, station{x: x0 + uint32(i), y: uint32(int(row) + off), props: map[string]any{"@id": id, "dcfc_count": 2, "max_power_kw": kw}})
	}
	for i := 0; i < n; i++ {
		// Three chargers a tile (rows -1, 0, +1 about 2.4 km apart): 300 along about 240 km; the middle one is the strong one.
		add(i, -1, fmt.Sprintf("s%03d-a", i), 50)
		add(i, 0, fmt.Sprintf("s%03d-b", i), 150)
		add(i, 1, fmt.Sprintf("s%03d-c", i), 50)
	}
	// A big city at the start: two more rows on each of the first 20 tiles. It must not use up the cap.
	for i := 0; i < 20; i++ {
		add(i, -2, fmt.Sprintf("city%03d-a", i), 50)
		add(i, 2, fmt.Sprintf("city%03d-b", i), 50)
	}
	a := loaded(t, archiveAt(t, all))
	path := make([][2]float64, 0, n)
	for i := 0; i < n; i++ {
		c := maptile.New(uint32(x0+i), row, 14).Bound().Center()
		path = append(path, [2]float64{c[1], c[0]})
	}
	const limit = 60
	r, err := a.QueryLayers(path, []string{"dcfc"}, LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 6}, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total < 300 || len(r.POIs) != limit || r.NextAfterKm != nil {
		t.Fatalf("total %d, returned %d, next %v: want the cap exactly, no cursor", r.Total, len(r.POIs), r.NextAfterKm)
	}
	first, last := r.POIs[0].AlongKm, r.POIs[len(r.POIs)-1].AlongKm
	if first > r.RouteKm*0.1 || last < r.RouteKm*0.9 {
		t.Errorf("the capped set spans %.0f to %.0f km of a %.0f km route", first, last, r.RouteKm)
	}
	// No stretch of 1/10 of the route is empty (taking the first 60 in route order would leave everything past the city empty).
	for tenth := 0; tenth < 10; tenth++ {
		lo, hi := r.RouteKm*float64(tenth)/10, r.RouteKm*float64(tenth+1)/10
		found := false
		for _, p := range r.POIs {
			if p.AlongKm >= lo && p.AlongKm < hi+0.001 {
				found = true
			}
		}
		if !found {
			t.Errorf("no station in the tenth %d of the route (%.0f to %.0f km)", tenth, lo, hi)
		}
	}
	// Route order, no duplicates.
	seen := map[string]bool{}
	for i, p := range r.POIs {
		if seen[p.EntityID] {
			t.Errorf("%s twice", p.EntityID)
		}
		seen[p.EntityID] = true
		if i > 0 && p.AlongKm < r.POIs[i-1].AlongKm {
			t.Errorf("not in route order at %d", i)
		}
	}
	// Outside the city the strong charger of a stretch is the one kept.
	strong := 0
	for _, p := range r.POIs {
		if p.MaxPowerKW >= 150 {
			strong++
		}
	}
	if strong < limit/3 {
		t.Errorf("only %d of %d kept stations are the strong ones", strong, limit)
	}
	// Paged mode is unchanged: the first stretch of the route in order, with a cursor.
	p, _ := a.QueryLayers(path, []string{"dcfc"}, LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 6}, Limit: limit, Paged: true})
	if p.NextAfterKm == nil || p.POIs[len(p.POIs)-1].AlongKm > r.RouteKm*0.5 {
		t.Errorf("paged: next %v, last %.0f km", p.NextAfterKm, p.POIs[len(p.POIs)-1].AlongKm)
	}
	// Under the cap nothing is dropped.
	u, _ := a.QueryLayers(path, []string{"dcfc"}, LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 6}, Limit: 1000})
	if len(u.POIs) != u.Total || u.NextAfterKm != nil {
		t.Errorf("under the cap: %d of %d, next %v", len(u.POIs), u.Total, u.NextAfterKm)
	}
}

// Chargers and hotels together over the cap: every kind is spread on its own, so dense chargers do not push the hotels out (hotels have no
// power to rank by, and used to lose every stretch to a charger).
func TestCappedCorridorKeepsEveryKindAlongTheRoute(t *testing.T) {
	const row, x0, n = 6000, 1000, 100
	var all []station
	for i := 0; i < n; i++ {
		for off := -1; off <= 1; off++ {
			all = append(all, station{x: x0 + uint32(i), y: uint32(int(row) + off), props: map[string]any{
				"@id": fmt.Sprintf("d%03d_%d", i, off), "dcfc_count": 2, "max_power_kw": 150.0}})
		}
	}
	for i := 3; i < n; i += 3 { // a hotel every third tile, 33 along the route
		all = append(all, station{x: x0 + uint32(i), y: row + 2, props: map[string]any{"@id": fmt.Sprintf("h%03d", i), "facility_type": "HOTEL", "l2_count": 1}})
	}
	a := loaded(t, archiveAt(t, all))
	path := make([][2]float64, 0, n)
	for i := 0; i < n; i++ {
		c := maptile.New(uint32(x0+i), row, 14).Bound().Center()
		path = append(path, [2]float64{c[1], c[0]})
	}
	const limit = 60
	r, err := a.QueryLayers(path, []string{"dcfc", "hotels"}, LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 6}, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.POIs) > limit || len(r.POIs) < limit-2 {
		t.Fatalf("returned %d, want about the cap of %d", len(r.POIs), limit)
	}
	has := func(kind string, lo, hi float64) bool {
		for _, p := range r.POIs {
			if p.AlongKm >= lo && p.AlongKm < hi+0.001 {
				for _, k := range p.Kinds {
					if k == kind {
						return true
					}
				}
			}
		}
		return false
	}
	for tenth := 0; tenth < 10; tenth++ {
		lo, hi := r.RouteKm*float64(tenth)/10, r.RouteKm*float64(tenth+1)/10
		if !has("hotels", lo, hi) {
			t.Errorf("no hotel in the tenth %d of the route", tenth)
		}
		if !has("dcfc", lo, hi) {
			t.Errorf("no charger in the tenth %d of the route", tenth)
		}
	}
	// A kind that is small hands its unused share to the others: 33 hotels are all kept, and the chargers take the rest.
	hotels := 0
	for _, p := range r.POIs {
		for _, k := range p.Kinds {
			if k == "hotels" {
				hotels++
			}
		}
	}
	if hotels < 30 {
		t.Errorf("%d hotels kept, want all of the 33 that fit in their share", hotels)
	}
}

// The cap is never exceeded, whatever the number of kinds: a limit below the number of kinds, equal to it, and just above it.
func TestCappedCorridorNeverExceedsTheLimitWithManyKinds(t *testing.T) {
	const row, x0, n = 6000, 1000, 100
	var all []station
	for i := 0; i < n; i++ {
		for off := -1; off <= 1; off++ {
			all = append(all, station{x: x0 + uint32(i), y: uint32(int(row) + off), props: map[string]any{
				"@id": fmt.Sprintf("d%03d_%d", i, off), "dcfc_count": 2, "l2_count": 1, "max_power_kw": 150.0}})
		}
		all = append(all, station{x: x0 + uint32(i), y: row + 2, props: map[string]any{"@id": fmt.Sprintf("h%03d", i), "facility_type": "HOTEL", "l2_count": 1}})
		all = append(all, station{x: x0 + uint32(i), y: row - 2, props: map[string]any{"@id": fmt.Sprintf("c%03d", i), "facility_type": "CAMPGROUND", "hookup_amps": 50}})
	}
	a := loaded(t, archiveAt(t, all))
	path := make([][2]float64, 0, n)
	for i := 0; i < n; i++ {
		c := maptile.New(uint32(x0+i), row, 14).Bound().Center()
		path = append(path, [2]float64{c[1], c[0]})
	}
	kinds := []string{"dcfc", "l2", "hotels", "camp"}
	for _, limit := range []int{1, 2, 3, 4, 5, 6, 10, 37, 100} {
		r, err := a.QueryLayers(path, kinds, LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 6}, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.POIs) > limit {
			t.Errorf("limit %d returned %d", limit, len(r.POIs))
		}
		if len(r.POIs) == 0 {
			t.Errorf("limit %d returned nothing", limit)
		}
		if limit >= len(kinds) {
			seen := map[string]bool{}
			for _, p := range r.POIs {
				for _, k := range p.Kinds {
					seen[k] = true
				}
			}
			for _, k := range kinds {
				if !seen[k] {
					t.Errorf("limit %d: no %s kept", limit, k)
				}
			}
		}
	}
}

// With no corridor_km each kind has its own width: fast 8 km, Level 2 4.8 km, hotels and campgrounds 16 km. A row is about 1.75 km at this latitude (a tile is 2.4 km at the equator).
func TestEachKindHasItsOwnDefaultWidth(t *testing.T) {
	const row, x0 = 6000, 1000
	mk := func(i int, rows int, p map[string]any) station {
		return station{x: x0 + uint32(i), y: uint32(row + rows), props: p}
	}
	a := loaded(t, archiveAt(t, []station{
		mk(10, 3, map[string]any{"@id": "dcfc_near", "dcfc_count": 2}),                          // about 5.2 km: inside 8
		mk(20, 6, map[string]any{"@id": "dcfc_far", "dcfc_count": 2}),                           // about 10.5 km: outside 8
		mk(30, 2, map[string]any{"@id": "l2_near", "l2_count": 2}),                              // about 3.5 km: inside 4.8
		mk(40, 4, map[string]any{"@id": "l2_far", "l2_count": 2}),                               // about 7 km: outside 4.8
		mk(50, 7, map[string]any{"@id": "hotel_near", "facility_type": "HOTEL", "l2_count": 1}), // about 12 km: inside 16
		mk(60, 12, map[string]any{"@id": "hotel_far", "facility_type": "HOTEL", "l2_count": 1}), // about 21 km: outside 16
		mk(70, 7, map[string]any{"@id": "camp_near", "facility_type": "CAMPGROUND", "hookup_amps": 50}),
		mk(80, 12, map[string]any{"@id": "camp_far", "facility_type": "CAMPGROUND", "hookup_amps": 50}),
	}))
	path := make([][2]float64, 0, 100)
	for i := 0; i < 100; i++ {
		c := maptile.New(uint32(x0+i), row, 14).Bound().Center()
		path = append(path, [2]float64{c[1], c[0]})
	}
	r, err := a.QueryLayers(path, []string{"dcfc", "l2", "hotels", "camp"}, LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, id := range ids(r) {
		got[id] = true
	}
	for _, want := range []string{"dcfc_near", "l2_near", "hotel_near", "camp_near"} {
		if !got[want] {
			t.Errorf("%s dropped; kept %v", want, ids(r))
		}
	}
	for _, bad := range []string{"dcfc_far", "l2_far", "hotel_far", "camp_far"} {
		if got[bad] {
			t.Errorf("%s kept; it is outside its kind's width", bad)
		}
	}
	// The total counts after the per-kind filter, so "showing x of y" is true.
	if r.Total != 4 || len(r.POIs) != 4 {
		t.Errorf("total %d, returned %d, want 4 and 4", r.Total, len(r.POIs))
	}
	// A station that is both fast and a hotel keeps only the kinds whose width it is inside.
	// Asking for fast alone scans only 8 km: the hotels are not even looked at.
	f, _ := a.QueryLayers(path, []string{"dcfc"}, LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true}})
	if ids(f)[0] != "dcfc_near" || len(f.POIs) != 1 || f.Total != 1 {
		t.Errorf("fast only: %v total %d", ids(f), f.Total)
	}
	// An explicit corridor_km is used for every kind, as before.
	e, _ := a.QueryLayers(path, []string{"dcfc", "l2"}, LayerOptions{QueryCorridorOptions: QueryCorridorOptions{IncludeEndpoints: true, CorridorKm: 20}})
	eg := map[string]bool{}
	for _, id := range ids(e) {
		eg[id] = true
	}
	if !eg["dcfc_far"] || !eg["l2_far"] {
		t.Errorf("explicit 20 km: %v", ids(e))
	}
}

// Inside a stretch the station nearest the route wins, power only breaks a tie: an off-route 350 kW site does not beat an on-highway 150 kW one.
func TestSpreadPrefersTheStationNearestTheRoute(t *testing.T) {
	all := []CorridorPOI{
		{POI: POI{EntityID: "far350", MaxPowerKW: 350}, AlongKm: 10, OffKm: 28},
		{POI: POI{EntityID: "near150", MaxPowerKW: 150}, AlongKm: 12, OffKm: 0.4},
		{POI: POI{EntityID: "near350", MaxPowerKW: 350}, AlongKm: 14, OffKm: 0.4},
		{POI: POI{EntityID: "tail", MaxPowerKW: 50}, AlongKm: 95, OffKm: 1},
	}
	got := spreadAlongRoute(all, []int{0, 1, 2, 3}, 2, 100, true)
	var names []string
	for _, i := range got {
		names = append(names, all[i].EntityID)
	}
	// One slot for the stretch with three stations: the equally near pair is decided by power, never the 28 km one.
	if len(names) != 2 || names[0] != "near350" || names[1] != "tail" {
		t.Errorf("kept %v, want [near350 tail]", names)
	}
}

// The planner settings: a preset resizes each kind's width, start exclusion drops stations near the route's start (and Total agrees), and the
// minimum fast power drops only fast chargers of a known power below it.
func TestPlannerSettingsPresetsStartAndPower(t *testing.T) {
	const row, x0 = 6000, 1000
	mk := func(i int, rows int, p map[string]any) station {
		return station{x: x0 + uint32(i), y: uint32(row + rows), props: p}
	}
	a := loaded(t, archiveAt(t, []station{
		mk(2, 0, map[string]any{"@id": "fast_start", "dcfc_count": 2, "max_power_kw": 350}),
		mk(30, 2, map[string]any{"@id": "fast_slow", "dcfc_count": 2, "max_power_kw": 62}),
		mk(40, 2, map[string]any{"@id": "fast_unknown", "dcfc_count": 2}),
		mk(50, 3, map[string]any{"@id": "fast_mid", "dcfc_count": 2, "max_power_kw": 150}),     // about 5.2 km: outside close (3.2), inside normal (8)
		mk(60, 8, map[string]any{"@id": "fast_wide", "dcfc_count": 2, "max_power_kw": 150}),    // about 14 km: only wide (16)
		mk(70, 4, map[string]any{"@id": "hotel_mid", "facility_type": "HOTEL", "l2_count": 1}), // about 7 km: close (8) keeps it
	}))
	path := make([][2]float64, 0, 100)
	for i := 0; i < 100; i++ {
		c := maptile.New(uint32(x0+i), row, 14).Bound().Center()
		path = append(path, [2]float64{c[1], c[0]})
	}
	kinds := []string{"dcfc", "hotels"}
	q := func(preset string, o LayerOptions) map[string]bool {
		o.QueryCorridorOptions.IncludeEndpoints = true
		o.KindKm = CorridorPresets[preset]
		r, err := a.QueryLayers(path, kinds, o)
		if err != nil {
			t.Fatal(err)
		}
		if r.Total != len(r.POIs) {
			t.Errorf("%s: total %d but %d returned", preset, r.Total, len(r.POIs))
		}
		got := map[string]bool{}
		for _, id := range ids(r) {
			got[id] = true
		}
		return got
	}
	if g := q("normal", LayerOptions{}); !g["fast_mid"] || g["fast_wide"] || !g["hotel_mid"] {
		t.Errorf("normal: %v", g)
	}
	if g := q("close", LayerOptions{}); g["fast_mid"] || !g["hotel_mid"] {
		t.Errorf("close: %v", g)
	}
	if g := q("wide", LayerOptions{}); !g["fast_mid"] || !g["fast_wide"] {
		t.Errorf("wide: %v", g)
	}
	// 25 miles is about 40 km: fast_start (about 5 km along) goes, the rest stay.
	if g := q("normal", LayerOptions{MinAlongKm: 25 * 1.609344}); g["fast_start"] || !g["fast_mid"] {
		t.Errorf("start exclusion: %v", g)
	}
	// 150 kW: the 62 kW charger goes, the unknown-power one stays, the 150 stays; a hotel is not a fast charger and is untouched.
	if g := q("normal", LayerOptions{MinFastKW: 150}); g["fast_slow"] || !g["fast_unknown"] || !g["fast_mid"] || !g["hotel_mid"] {
		t.Errorf("min fast power: %v", g)
	}
}
