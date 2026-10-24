package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pm "github.com/protomaps/go-pmtiles/pmtiles"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/db"
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

// The route-carrying routes sit in groups that cap JSON bodies at 1 MiB, and a limit set inside another can only lower
// it, so a long drive's polyline (about 2 MB at 80,000 points) was refused with "request body too large" despite the
// handlers' own 8 MiB. The group middleware now gives those two routes routeBodyMax. chargers-along is checked through the
// real router with the full URL.
func TestRouteBodiesPassTheGroupBodyLimit(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	r := New(Deps{Auth: svc, ChargersArchive: emptyArchive(t)})
	body, _ := json.Marshal(map[string]any{"path": longRoute(80_000), "filter": "dcfc", "drive_sec": 40000})
	if len(body) < 1<<21-200_000 {
		t.Fatalf("test body is only %d bytes", len(body))
	}
	req := httptest.NewRequest(http.MethodPost, "/api/maps/chargers-along", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("chargers-along: %d %q", rec.Code, strings.TrimSpace(rec.Body.String()))
	}

	// overnight-candidates needs the trip-planner flag (a database) to be reached through the router; it sits in a group
	// with the same body middleware, checked here directly with the route's full path. Any other route keeps 1 MiB.
	read := func(path string) error {
		h := maxBodyBytes(maxJSONBody)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := io.ReadAll(r.Body); err != nil {
				http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			}
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			return errors.New(strings.TrimSpace(rec.Body.String()))
		}
		return nil
	}
	if err := read("/api/trips/overnight-candidates"); err != nil {
		t.Errorf("overnight-candidates: %v", err)
	}
	if err := read("/api/settings/anything"); err == nil {
		t.Error("an ordinary JSON route must keep the 1 MiB cap")
	}
}
