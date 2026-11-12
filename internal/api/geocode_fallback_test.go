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

	"github.com/apohor/rivolt/internal/geocoding"
)

func photonStub(t *testing.T, status int, body string) *geocoding.PhotonClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return geocoding.NewPhotonClient(srv.URL)
}

func geocodeGet(h http.HandlerFunc) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/geocode?q=austin", nil))
	return w
}

// Without the operator's Open-Meteo switch, a Photon failure or no Photon at all answers "unavailable"; nothing is sent to a third party.
func TestGeocodeWithoutFallbackIsUnavailable(t *testing.T) {
	for name, photon := range map[string]*geocoding.PhotonClient{
		"photon down":    photonStub(t, 500, "boom"),
		"photon unwired": nil,
	} {
		w := geocodeGet(handleGeocode(photon, false, nil))
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "place search unavailable") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

// An empty Photon answer is an empty list, not an error and not a fallback.
func TestGeocodeEmptyPhotonIsAnEmptyList(t *testing.T) {
	w := geocodeGet(handleGeocode(photonStub(t, 200, `{"features":[]}`), false, nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

// A search the client abandoned gets no response and is counted; a real Photon failure still answers 503, logs a WARN, and leaves the query out of it.
func TestGeocodeCanceledIsQuietAndFailureIsLogged(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); slow.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	canceled := 0
	req := httptest.NewRequest(http.MethodGet, "/api/geocode?q=secretstreet", nil).WithContext(ctx)
	time.AfterFunc(50*time.Millisecond, cancel)
	w := httptest.NewRecorder()
	handleGeocode(geocoding.NewPhotonClient(slow.URL), false, func() { canceled++ })(w, req)
	if canceled != 1 || w.Body.Len() != 0 {
		t.Errorf("canceled=%d code=%d body=%q", canceled, w.Code, w.Body)
	}

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	w = httptest.NewRecorder()
	handleGeocode(photonStub(t, 500, "boom"), false, func() { canceled++ })(w, httptest.NewRequest(http.MethodGet, "/api/geocode?q=secretstreet", nil))
	if w.Code != http.StatusServiceUnavailable || canceled != 1 {
		t.Errorf("failure: code=%d canceled=%d", w.Code, canceled)
	}
	if !strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "secretstreet") {
		t.Errorf("log: %s", logs.String())
	}

	// A transport error carries the URL; the logged error must not.
	logs.Reset()
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	w = httptest.NewRecorder()
	handleGeocode(geocoding.NewPhotonClient(dead.URL), false, nil)(w, httptest.NewRequest(http.MethodGet, "/api/geocode?q=secretstreet", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "secretstreet") {
		t.Errorf("transport: code=%d log: %s", w.Code, logs.String())
	}
}

// A request deadline is not a client cancel: it stays a 503 with a WARN and is not counted as canceled.
func TestGeocodeDeadlineIsNotCanceled(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); slow.Close() })

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	canceled := 0
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/geocode?q=austin", nil).WithContext(ctx)
	handleGeocode(geocoding.NewPhotonClient(slow.URL), false, func() { canceled++ })(w, req)
	if w.Code != http.StatusServiceUnavailable || canceled != 0 || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("code=%d canceled=%d log=%s", w.Code, canceled, logs.String())
	}
}
