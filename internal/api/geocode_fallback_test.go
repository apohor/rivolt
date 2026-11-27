package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/overnight"
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
		w := geocodeGet(handleGeocode(photon, nil, false, nil))
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "place search unavailable") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

// An empty Photon answer is an empty list, not an error and not a fallback.
func TestGeocodeEmptyPhotonIsAnEmptyList(t *testing.T) {
	w := geocodeGet(handleGeocode(photonStub(t, 200, `{"features":[]}`), nil, false, nil))
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
	handleGeocode(geocoding.NewPhotonClient(slow.URL), nil, false, func() { canceled++ })(w, req)
	if canceled != 1 || w.Body.Len() != 0 {
		t.Errorf("canceled=%d code=%d body=%q", canceled, w.Code, w.Body)
	}

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	w = httptest.NewRecorder()
	handleGeocode(photonStub(t, 500, "boom"), nil, false, func() { canceled++ })(w, httptest.NewRequest(http.MethodGet, "/api/geocode?q=secretstreet", nil))
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
	handleGeocode(geocoding.NewPhotonClient(dead.URL), nil, false, nil)(w, httptest.NewRequest(http.MethodGet, "/api/geocode?q=secretstreet", nil))
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
	handleGeocode(geocoding.NewPhotonClient(slow.URL), nil, false, func() { canceled++ })(w, req)
	if w.Code != http.StatusServiceUnavailable || canceled != 0 || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("code=%d canceled=%d log=%s", w.Code, canceled, logs.String())
	}
}

// lat and lon reach Photon as the bias when both are valid, and are ignored when absent, out of range, lone or not numbers.
func TestGeocodeNearParams(t *testing.T) {
	for _, c := range []struct {
		qs      string
		biased  bool
		wantLat string
	}{
		{"&lat=36.06&lon=-102.51", true, "36.0600"},
		{"", false, ""},
		{"&lat=36.06", false, ""},
		{"&lat=95&lon=10", false, ""},
		{"&lat=10&lon=-181", false, ""},
		{"&lat=abc&lon=1", false, ""},
		{"&lat=NaN&lon=1", false, ""},
		{"&lat=0&lon=0", false, ""},
	} {
		var sawLat string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if l := r.URL.Query().Get("lat"); l != "" {
				sawLat = l
			}
			_, _ = w.Write([]byte(`{"features":[{"geometry":{"coordinates":[-101.9,35.8]},"properties":{"name":"Dumas","osm_key":"place","osm_value":"city"}}]}`))
		}))
		w := httptest.NewRecorder()
		handleGeocode(geocoding.NewPhotonClient(srv.URL), nil, false, nil)(w, httptest.NewRequest(http.MethodGet, "/api/geocode?q=dumas"+c.qs, nil))
		srv.Close()
		if w.Code != http.StatusOK || sawLat != c.wantLat {
			t.Errorf("%q: status %d, photon saw lat %q, want %q", c.qs, w.Code, sawLat, c.wantLat)
		}
	}
}

type fakeHotelNames struct{ hits []overnight.HotelHit }

func (f fakeHotelNames) Search(string, float64, float64, bool, int) []overnight.HotelHit {
	return f.hits
}

var dalhartHampton = overnight.HotelHit{Name: "Hampton Inn Dalhart", City: "Dalhart", State: "TX", Country: "US", Lat: 36.0600, Lon: -102.5100}

// The owner's search: the hotel from the charger archive comes first, labelled with its cleaned name and "Texas, United States", marked as an EV
// charging stop; the same hotel in Photon's rows (same name, a few metres away) is dropped, a different one stays.
func TestGeocodeAddsChargerArchiveHotels(t *testing.T) {
	photon := photonStub(t, 200, `{"features":[
	 {"geometry":{"coordinates":[-102.5101,36.0601]},"properties":{"name":"Hampton Inn","city":"Dalhart","state":"Texas","country":"United States","countrycode":"US","osm_key":"tourism","osm_value":"hotel"}},
	 {"geometry":{"coordinates":[-101.9,35.2]},"properties":{"name":"Hampton Inn","city":"Amarillo","state":"Texas","country":"United States","countrycode":"US","osm_key":"tourism","osm_value":"hotel"}}]}`)
	w := geocodeGet(handleGeocode(photon, fakeHotelNames{[]overnight.HotelHit{dalhartHampton}}, false, nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got []geocoding.Result
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d rows, want the charger row and Amarillo: %+v", len(got), got)
	}
	f := got[0]
	if f.Name != "Hampton Inn Dalhart" || f.Admin1 != "Texas" || f.Locality != "Dalhart" || f.Country != "United States" || !f.EVCharging {
		t.Errorf("first row = %+v", f)
	}
	if got[1].Locality != "Amarillo" || got[1].EVCharging {
		t.Errorf("second row = %+v, want the Amarillo Photon row", got[1])
	}
}

// A charger hotel whose name lacks the town gets it appended, and a US state code is spelled out, so the row reads "Dalhart, Texas, United States"
// like the Photon rows; a state that is not a code, or a Canadian one, is left as it is.
func TestHotelRowNamesTheTownAndSpellsTheState(t *testing.T) {
	rows := hotelResults(fakeHotelNames{[]overnight.HotelHit{
		{Name: "Super 8", City: "Dalhart", State: "TX", Country: "US", Lat: 36.06, Lon: -102.51},
		{Name: "Best Western", City: "Banff", State: "AB", Country: "CA", Lat: 51.17, Lon: -115.57},
	}}, "super", nil, 5)
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	if r := rows[0]; r.Name != "Super 8, Dalhart" || r.Admin1 != "Texas" || r.Country != "United States" {
		t.Errorf("US row = %+v", r)
	}
	if r := rows[1]; r.Name != "Best Western, Banff" || r.Admin1 != "AB" || r.Country != "Canada" {
		t.Errorf("CA row = %+v", r)
	}
}

// With Photon down, a charger-archive hotel still answers (200), and the 503 stays for a search nothing answers.
func TestGeocodeHotelAnswersWhenPhotonIsDown(t *testing.T) {
	w := geocodeGet(handleGeocode(photonStub(t, 500, "boom"), fakeHotelNames{[]overnight.HotelHit{dalhartHampton}}, false, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Hampton Inn Dalhart") {
		t.Errorf("hotel with Photon down: %d %s", w.Code, w.Body)
	}
	w = geocodeGet(handleGeocode(photonStub(t, 500, "boom"), fakeHotelNames{}, false, nil))
	if w.Code != 503 {
		t.Errorf("no hotel, Photon down: %d, want 503", w.Code)
	}
}

func TestMergeHotelResultsKeepsCountAndOtherNames(t *testing.T) {
	h := geocoding.Result{Name: "Hampton Inn Dalhart", Latitude: 36.06, Longitude: -102.51, Locality: "Dalhart", EVCharging: true}
	far := geocoding.Result{Name: "Hampton Inn, Dalhart", Latitude: 36.2, Longitude: -102.51, Locality: "Dalhart"} // 15 km away: another hotel
	other := geocoding.Result{Name: "Best Western", Latitude: 36.06, Longitude: -102.51}
	got := mergeHotelResults([]geocoding.Result{h}, []geocoding.Result{far, other})
	if len(got) != 3 || !got[0].EVCharging || got[1].Name != "Hampton Inn, Dalhart" {
		t.Errorf("merge = %+v", got)
	}
}

// Two hotel hits must not push the plain rows off the end: the far namesake a biased search works in ("Boston, MA" from Texas) stays.
func TestMergeHotelResultsKeepsEveryPlainRow(t *testing.T) {
	hotels := []geocoding.Result{
		{Name: "Hampton Inn Boston/Natick", Latitude: 42.28, Longitude: -71.35, Locality: "Natick", EVCharging: true},
		{Name: "Holiday Inn Boston", Latitude: 42.4, Longitude: -71.1, Locality: "Somerville", EVCharging: true},
	}
	var plain []geocoding.Result
	for i, n := range []string{"Boston, Texas", "Boston, Georgia", "Boston, Kentucky", "Boston, Massachusetts", "Boston, Lincolnshire"} {
		plain = append(plain, geocoding.Result{Name: n, Latitude: float64(30 + i), Longitude: -90})
	}
	got := mergeHotelResults(hotels, plain)
	if len(got) != 7 {
		t.Fatalf("%d rows, want 2 hotels and all 5 plain rows", len(got))
	}
	if got[5].Name != "Boston, Massachusetts" {
		t.Errorf("rows = %+v", got)
	}
}

// Before the charger archive carries towns, a hotel with no city is left out of a one-word search (the town itself is what is wanted) and
// still answers a longer one.
func TestHotelWithNoTownSkipsAOneWordSearch(t *testing.T) {
	hs := fakeHotelNames{[]overnight.HotelHit{{Name: "Hampton Inn Dumas", Country: "US", Lat: 35.86, Lon: -101.97}}}
	if rows := hotelResults(hs, "dumas", nil, 3); len(rows) != 0 {
		t.Errorf("one word, no city: %+v", rows)
	}
	if rows := hotelResults(hs, "hampton inn dumas", nil, 3); len(rows) != 1 {
		t.Errorf("three words, no city: %d rows", len(rows))
	}
}

func TestMergeHotelResultsKeepsAnExactCityAheadOfTheHotels(t *testing.T) {
	city := geocoding.Result{Name: "Boston", Admin1: "Massachusetts", Exact: true}
	hotels := []geocoding.Result{{Name: "Boston Inn", EVCharging: true}, {Name: "Boston Suites", EVCharging: true}}
	got := mergeHotelResults(hotels, []geocoding.Result{city, {Name: "Boston, CA"}})
	if got[0].Name != "Boston" || got[1].Name != "Boston Inn" || len(got) != 4 {
		t.Errorf("got %+v, want the city first, then the hotels, then the rest", got)
	}
	plain := mergeHotelResults(hotels, []geocoding.Result{{Name: "Boston, CA"}})
	if plain[0].Name != "Boston Inn" {
		t.Errorf("without an exact city the hotels stay first: %+v", plain)
	}
}
