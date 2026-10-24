package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pm "github.com/protomaps/go-pmtiles/pmtiles"

	"github.com/apohor/rivolt/internal/chargers"
)

// emptyArchive serves a valid PMTiles file with no tiles, so the handler's checks run and the query finds nothing.
func emptyArchive(t *testing.T) *chargers.Archive {
	t.Helper()
	root := pm.SerializeEntries(nil, pm.Gzip)
	h := pm.HeaderV3{
		SpecVersion: 3, RootOffset: pm.HeaderV3LenBytes, RootLength: uint64(len(root)),
		InternalCompression: pm.Gzip, TileCompression: pm.Gzip, TileType: pm.Mvt, MinZoom: 14, MaxZoom: 14,
	}
	file := append(pm.SerializeHeader(h), root...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(file) }))
	t.Cleanup(srv.Close)
	a := chargers.New(srv.URL)
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}

func longRoute(points int) [][2]float64 {
	p := make([][2]float64, points)
	for i := range p {
		f := float64(i) / float64(points-1)
		p[i] = [2]float64{33 + 6*f, -97 - 8*f} // about 1,000 km
	}
	return p
}

func postAlong(h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/maps/chargers-along", bytes.NewReader(b)))
	return rec
}

// A long drive's polyline has tens of thousands of points. It used to be refused ("path has too many points", 400)
// above 5,000, so the trip map showed no chargers on any long route; now it is thinned and answered.
func TestChargersAlongTakesALongRoutePolyline(t *testing.T) {
	h := handleChargersAlong(emptyArchive(t))
	for _, n := range []int{100, 5000, 5001, 30_000, 150_000} {
		rec := postAlong(h, map[string]any{"path": longRoute(n), "filter": "hotels"})
		if rec.Code != http.StatusOK {
			t.Errorf("%d points: %d %q", n, rec.Code, rec.Body.String())
		}
	}
}

func TestChargersAlongStillRefusesAbsurdAndBadInput(t *testing.T) {
	h := handleChargersAlong(emptyArchive(t))
	if rec := postAlong(h, map[string]any{"path": longRoute(chargers.MaxRoutePoints + 1)}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "too many points") {
		t.Errorf("over the input cap: %d %q", rec.Code, rec.Body.String())
	}
	if rec := postAlong(h, map[string]any{"path": [][2]float64{{1, 2}}}); rec.Code != http.StatusBadRequest {
		t.Errorf("one point: %d", rec.Code)
	}
}
