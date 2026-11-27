package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/radar"
)

type fakeForecast struct{}

func (*fakeForecast) At(context.Context, float64, float64, time.Time) (radar.Reading, bool) {
	return radar.Reading{}, false
}

func (f *fakeForecast) ForecastHours(context.Context) (radar.ForecastHours, error) {
	return radar.ForecastHours{Cycle: "2026112712", Credit: "NOAA RRFS", Bounds: [4]float64{-134, 21, -60, 53},
		Hours: []radar.ForecastHour{{FH: 1, ValidUnix: 1795000000}}, HorizonUnix: 1795000000}, nil
}

func (f *fakeForecast) Tile(_ context.Context, cycle string, fh, z, x, y int) ([]byte, error) {
	if fh == 6 {
		return nil, radar.ErrFeedDown
	}
	if cycle != "2026112712" || fh > 6 {
		return nil, radar.ErrNoForecast
	}
	return []byte("\x89PNG-fake"), nil
}

// The forecast radar routes, through the real router with full public URLs (the path-matching lesson of #569): the hours list, a tile with its
// cache header, the addresses that are refused, and the mount being absent when the install has no forecast radar.
func TestForecastRadarThroughTheRouter(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	send := func(r http.Handler, method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		asSession(r, "").ServeHTTP(rec, req)
		return rec
	}
	router := New(Deps{Auth: svc, PlanRadar: &fakeForecast{}})

	rec := send(router, http.MethodGet, "/api/maps/forecast-radar/hours")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"credit":"NOAA RRFS"`) || !strings.Contains(rec.Body.String(), `"cycle":"2026112712"`) {
		t.Fatalf("hours: %d %s", rec.Code, rec.Body)
	}
	rec = send(router, http.MethodGet, "/api/maps/forecast-radar/2026112712/3/7/20/45.png")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "private, max-age=86400, immutable" {
		t.Fatalf("tile: %d %v", rec.Code, rec.Header())
	}
	// An hour not offered is 404, a feed that did not answer is 503 with Retry-After; neither may be cached.
	if rec = send(router, http.MethodGet, "/api/maps/forecast-radar/2026112712/9/7/20/45.png"); rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("404: %d %v", rec.Code, rec.Header())
	}
	if rec = send(router, http.MethodGet, "/api/maps/forecast-radar/2026112712/6/7/20/45.png"); rec.Code != 503 || rec.Header().Get("Retry-After") == "" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("503: %d %v", rec.Code, rec.Header())
	}
	if rec = send(router, http.MethodHead, "/api/maps/forecast-radar/2026112712/3/7/20/45.png"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d %d", rec.Code, rec.Body.Len())
	}
	for _, p := range []string{
		"/api/maps/forecast-radar/2026112712/9/7/20/45.png",  // past the horizon
		"/api/maps/forecast-radar/2026112600/3/7/20/45.png",  // another cycle
		"/api/maps/forecast-radar/2026112712/3/2/0/0.png",    // zoom below 3
		"/api/maps/forecast-radar/2026112712/3/8/0/0.png",    // zoom above 7
		"/api/maps/forecast-radar/2026112712/3/7/128/0.png",  // x outside the zoom
		"/api/maps/forecast-radar/2026112712/03/7/20/45.png", // leading zero
		"/api/maps/forecast-radar/2026112712/3/7/20/45",      // no extension
		"/api/maps/forecast-radar/2026112712/3/7/20/45.png/x",
		"/api/maps/forecast-radar/../radar/frame",
		"/api/maps/forecast-radar/abc/3/7/20/45.png",
	} {
		if rec := send(router, http.MethodGet, p); rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	if rec := send(router, http.MethodPost, "/api/maps/forecast-radar/hours"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", rec.Code)
	}
	// An install without a forecast radar has no such route.
	if rec := send(New(Deps{Auth: svc}), http.MethodGet, "/api/maps/forecast-radar/hours"); rec.Code != 404 {
		t.Errorf("no radar: %d", rec.Code)
	}
}

// The access log is written by the router's own middleware, so this goes through api.New with full URLs: a forecast radar tile is logged as the
// route, never as the address (where the user is looking), exactly like the live radar; the hours route and other paths are logged as they are.
func TestForecastRadarTileAddressIsNotInTheAccessLog(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	router := New(Deps{Auth: svc, PlanRadar: &fakeForecast{}, Radar: stub})
	for path, want := range map[string]string{
		"/api/maps/forecast-radar/2026112712/3/7/20/45.png": "/api/maps/forecast-radar/:tile",
		"/api/maps/radar/7/20/45.png":                       "/api/maps/radar/:tile",
		"/api/maps/forecast-radar/hours":                    "/api/maps/forecast-radar/hours",
	} {
		buf.Reset()
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev"+path, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		asSession(router, "").ServeHTTP(httptest.NewRecorder(), req)
		out := buf.String()
		if !strings.Contains(out, `"path":"`+want+`"`) || strings.Contains(out, "45.png") || strings.Contains(out, "2026112712") {
			t.Errorf("GET %s: access log %s, want path %s and no address", path, out, want)
		}
	}
}
