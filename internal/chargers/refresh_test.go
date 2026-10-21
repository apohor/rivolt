package chargers

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/maptile"
	pm "github.com/protomaps/go-pmtiles/pmtiles"
)

// minimalArchive is a valid PMTiles file with an empty root directory.
func minimalArchive() []byte {
	root := pm.SerializeEntries(nil, pm.Gzip)
	h := pm.HeaderV3{
		SpecVersion: 3, RootOffset: pm.HeaderV3LenBytes, RootLength: uint64(len(root)),
		InternalCompression: pm.Gzip, TileCompression: pm.Gzip, TileType: pm.Mvt,
		MinZoom: 14, MaxZoom: 14,
	}
	return append(pm.SerializeHeader(h), root...)
}

// Refresh fetches the file when its ETag changed and does nothing (and keeps
// the loaded copy) on 304 or on a failed fetch.
func TestArchiveRefresh(t *testing.T) {
	body := minimalArchive()
	var etag atomic.Value
	etag.Store(`"v1"`)
	var status atomic.Int32
	status.Store(http.StatusOK)
	var gets, conditional int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++
		if int(status.Load()) != http.StatusOK {
			http.Error(w, "down", int(status.Load()))
			return
		}
		e := etag.Load().(string)
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			conditional++
			if inm == e {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.Header().Set("ETag", e)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	a := New(srv.URL)
	ctx := context.Background()
	if err := a.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	first := a.LoadedAt()

	if changed, err := a.Refresh(ctx); err != nil || changed {
		t.Fatalf("unchanged file: changed=%v err=%v, want false, nil", changed, err)
	}
	if conditional != 1 || !a.LoadedAt().Equal(first) {
		t.Errorf("conditional GETs %d, loadedAt moved=%v; want one 304 and no reload", conditional, !a.LoadedAt().Equal(first))
	}

	etag.Store(`"v2"`)
	if changed, err := a.Refresh(ctx); err != nil || !changed {
		t.Fatalf("new file: changed=%v err=%v, want true, nil", changed, err)
	}
	if !a.LoadedAt().After(first) {
		t.Error("loadedAt did not move after a changed file")
	}
	if changed, _ := a.Refresh(ctx); changed {
		t.Error("the new ETag was not remembered")
	}

	// A failing server keeps the loaded copy.
	etag.Store(`"v3"`)
	status.Store(http.StatusInternalServerError)
	at := a.LoadedAt()
	if changed, err := a.Refresh(ctx); err == nil || changed || !a.LoadedAt().Equal(at) {
		t.Errorf("server error: changed=%v err=%v, want an error and the old copy", changed, err)
	}
}

// archiveWith builds a PMTiles file with n charger features, one tile each.
func archiveWith(t *testing.T, n int) []byte {
	t.Helper()
	var entries []pm.EntryV3
	var data []byte
	for i := 0; i < n; i++ {
		tile := maptile.New(uint32(1000+i), 6000, 14)
		f := geojson.NewFeature(tile.Bound().Center())
		f.Properties = geojson.Properties{"name": "S", "@id": "nrel/" + string(rune('a'+i%26)) + string(rune('A'+i/26))}
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
	out := append(pm.SerializeHeader(h), root...)
	return append(out, data...)
}

// A new file with under 80% of the loaded stations is refused and the loaded
// copy stays; it is not downloaded again while it is the same file, and a
// healthy file after it is taken.
func TestArchiveRefreshShrinkGuard(t *testing.T) {
	files := map[string][]byte{"v1": archiveWith(t, 20), "v2": archiveWith(t, 12), "v3": archiveWith(t, 17)}
	var cur atomic.Value
	cur.Store("v1")
	var full atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := cur.Load().(string)
		etag := `"` + v + `"`
		if inm := r.Header.Get("If-None-Match"); inm != "" && bytes.Contains([]byte(inm), []byte(etag)) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("ETag", etag)
		_, _ = w.Write(files[v])
	}))
	defer srv.Close()

	a := New(srv.URL)
	ctx := context.Background()
	if err := a.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if a.stations != 20 {
		t.Fatalf("stations = %d, want 20", a.stations)
	}
	first := a.LoadedAt()

	cur.Store("v2") // 12 of 20 = 60%
	if changed, err := a.Refresh(ctx); err == nil || changed {
		t.Fatalf("shrunk file: changed=%v err=%v, want refused", changed, err)
	}
	if a.stations != 20 || !a.LoadedAt().Equal(first) {
		t.Error("the refused file replaced the loaded copy")
	}
	n := full.Load()
	if changed, err := a.Refresh(ctx); err != nil || changed || full.Load() != n {
		t.Errorf("same refused file again: changed=%v err=%v downloads +%d, want a 304", changed, err, full.Load()-n)
	}

	cur.Store("v3") // 17 of 20 = 85%
	if changed, err := a.Refresh(ctx); err != nil || !changed || a.stations != 17 {
		t.Errorf("healthy file: changed=%v err=%v stations=%d, want taken with 17", changed, err, a.stations)
	}
}
