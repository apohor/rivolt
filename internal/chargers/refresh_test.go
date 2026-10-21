package chargers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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
