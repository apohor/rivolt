package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"

	"github.com/apohor/rivolt/internal/weather"
)

// ambientTransport stands in for Open-Meteo: it counts requests, remembers the
// last URL, and answers with a one-hour forecast for the current hour.
type ambientTransport struct {
	calls atomic.Int32 // temperature requests
	rain  atomic.Int32 // precipitation requests
	last  atomic.Value // string: the last temperature request
	fail  bool
	// noRain makes the rain-ahead answer all null, as a server without the variable does.
	noRain bool
}

func (a *ambientTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.RawQuery, "hourly=precipitation&") {
		a.rain.Add(1)
		if a.fail {
			return nil, errors.New("rain: " + r.URL.String())
		}
		h0 := time.Now().UTC().Truncate(time.Hour)
		f := func(d int) string { return `"` + h0.Add(time.Duration(d)*time.Hour).Format("2006-01-02T15:04") + `"` }
		prob := `[0.2,1.5,0.3,9,4]`
		if a.noRain {
			prob = `[null,null,null,null,null]`
		}
		// Hours 0-2 are the next 3 h (sum 2.0 mm); hour 3 (9) and the past hour (4) are outside it.
		body := `{"hourly":{"time":[` + f(0) + `,` + f(1) + `,` + f(2) + `,` + f(3) + `,` + f(-1) + `],"precipitation":` + prob + `}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}, Request: r}, nil
	}
	a.calls.Add(1)
	a.last.Store(r.URL.String())
	if a.fail {
		return nil, errors.New(`Get "` + r.URL.String() + `": dial tcp: lookup api.open-meteo.com: no such host`)
	}
	hour := time.Now().UTC().Truncate(time.Hour).Format("2006-01-02T15:04")
	body := `{"hourly":{"time":["` + hour + `"],"temperature_2m":[7.4],"apparent_temperature":[5],"wind_speed_10m":[10],"wind_direction_10m":[200],"precipitation":[0],"relative_humidity_2m":[60],"weather_code":[1]}}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}, Request: r}, nil
}

func ambientFixture(optIn bool, tr *ambientTransport, logBuf *bytes.Buffer) func(context.Context, uuid.UUID, float64, float64) *nowAmbient {
	client := &weather.Client{HTTP: &http.Client{Transport: tr, Timeout: time.Second}, BaseURL: "https://archive.invalid"}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	demoID := uuid.MustParse("00000000-0000-0000-0000-00000000d3a0")
	return ambientLookup(func(context.Context, uuid.UUID) bool { return optIn }, client, newAmbientCache(), logger,
		func(u uuid.UUID) bool { return u == demoID })
}

func TestAmbientOffMeansNoCall(t *testing.T) {
	tr := &ambientTransport{}
	var logs bytes.Buffer
	look := ambientFixture(false, tr, &logs)
	if got := look(context.Background(), uuid.New(), 30.2672, -97.7431); got != nil {
		t.Errorf("got %+v with the toggle off", got)
	}
	if tr.calls.Load() != 0 {
		t.Errorf("%d upstream calls with the toggle off", tr.calls.Load())
	}
}

func TestAmbientNothingForTheDemoUser(t *testing.T) {
	tr := &ambientTransport{}
	var logs bytes.Buffer
	look := ambientFixture(true, tr, &logs)
	demoID := uuid.MustParse("00000000-0000-0000-0000-00000000d3a0")
	if got := look(context.Background(), demoID, 30.2672, -97.7431); got != nil || tr.calls.Load() != 0 {
		t.Errorf("demo user: %+v, %d calls", got, tr.calls.Load())
	}
}

// Opted in: the value comes back, the position is rounded to about 11 km BEFORE the
// request, and repeated polls (and a neighbour in the same cell) share one call.
func TestAmbientRoundsBeforeTheRequestAndCaches(t *testing.T) {
	tr := &ambientTransport{}
	var logs bytes.Buffer
	look := ambientFixture(true, tr, &logs)
	uid := uuid.New()
	got := look(context.Background(), uid, 30.2672, -97.7431)
	if got == nil || got.TempC != 7.4 || got.Source != "open-meteo" || got.AsOf.IsZero() {
		t.Fatalf("ambient = %+v", got)
	}
	url, _ := tr.last.Load().(string)
	if strings.Contains(url, "30.2672") || strings.Contains(url, "97.7431") || strings.Contains(url, "30.26") || strings.Contains(url, "97.74") {
		t.Errorf("the request carried unrounded coordinates: %s", url)
	}
	if !strings.Contains(url, "latitude=30.3000") || !strings.Contains(url, "longitude=-97.7000") {
		t.Errorf("request %s, want the position rounded to 0.1 degree", url)
	}
	for i := 0; i < 20; i++ {
		look(context.Background(), uid, 30.2672, -97.7431)
	}
	look(context.Background(), uuid.New(), 30.2701, -97.7399) // another user, same cell
	if n := tr.calls.Load(); n != 1 {
		t.Errorf("%d upstream calls for 22 lookups in one cell, want 1", n)
	}
	if n := tr.rain.Load(); n != 1 {
		t.Errorf("%d rain-ahead calls for 22 lookups in one cell, want 1", n)
	}
}

// The new fields: condition (the enum), wind in km/h, and the rain over the next 3 h (hour 3 and the past hour do not count).
func TestAmbientCarriesConditionWindAndRainChance(t *testing.T) {
	tr := &ambientTransport{}
	var logs bytes.Buffer
	got := ambientFixture(true, tr, &logs)(context.Background(), uuid.New(), 30.2672, -97.7431)
	if got == nil || got.Condition != "partly_cloudy" || got.WindKPH == nil || *got.WindKPH != 10 || got.RainNext3hMM == nil || *got.RainNext3hMM != 2.0 {
		t.Fatalf("ambient = %+v", got)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"rain_next3h_mm":2`) || !strings.Contains(string(b), `"wind_kph":10`) || !strings.Contains(string(b), `"condition":"partly_cloudy"`) {
		t.Errorf("json %s", b)
	}
}

// A server that has no precipitation probability leaves the key out (temperature still answers), and is asked once per cell, not per poll.
func TestAmbientRainChanceOmittedWhenUnknown(t *testing.T) {
	tr := &ambientTransport{noRain: true}
	var logs bytes.Buffer
	look := ambientFixture(true, tr, &logs)
	uid := uuid.New()
	for i := 0; i < 5; i++ {
		got := look(context.Background(), uid, 30.2672, -97.7431)
		if got == nil || got.TempC != 7.4 || got.RainNext3hMM != nil {
			t.Fatalf("ambient = %+v", got)
		}
		if b, _ := json.Marshal(got); strings.Contains(string(b), "rain_next3h_mm") {
			t.Fatalf("key present: %s", b)
		}
	}
	if n := tr.rain.Load(); n != 1 {
		t.Errorf("%d rain calls, want 1", n)
	}
}

// A failing rain call costs only the rain key.
func TestAmbientRainFailureKeepsTemperature(t *testing.T) {
	tr := &ambientTransport{}
	// Only the rain request fails: wrap the transport.
	client := &weather.Client{HTTP: &http.Client{Transport: rainDown{tr}, Timeout: time.Second}, BaseURL: "https://archive.invalid"}
	look := ambientLookup(func(context.Context, uuid.UUID) bool { return true }, client, newAmbientCache(), nil, nil)
	got := look(context.Background(), uuid.New(), 30.2672, -97.7431)
	if got == nil || got.TempC != 7.4 || got.RainNext3hMM != nil {
		t.Fatalf("ambient = %+v", got)
	}
}

type rainDown struct{ inner *ambientTransport }

func (r rainDown) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.RawQuery, "hourly=precipitation&") {
		return nil, errors.New("rain down")
	}
	return r.inner.RoundTrip(req)
}

func TestAmbientConditionEnum(t *testing.T) {
	for label, want := range map[string]string{"clear sky": "clear", "partly cloudy": "partly_cloudy", "overcast": "cloudy", "fog": "fog", "drizzle": "drizzle",
		"rain": "rain", "freezing rain": "rain", "snow": "snow", "thunderstorm": "thunder", "": ""} {
		if got := ambientCondition(label); got != want {
			t.Errorf("%q -> %q, want %q", label, got, want)
		}
	}
}

// The demo user gets fixed sample weather and the sample position, with no lookup; nobody else gets a location.
func TestNowDemoGetsSampleAmbientAndLocation(t *testing.T) {
	demoID := uuid.New()
	na := nowAPI{
		snapshot: func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
			return nowSnapshot{State: "parked"}, nil
		},
		connection: func(uuid.UUID) string { return "live" },
		isDemo:     func(u uuid.UUID) bool { return u == demoID },
		now:        time.Now,
	}
	get := func(u uuid.UUID) map[string]any {
		r := chi.NewRouter()
		r.Get("/vehicles/{vehicleID}/now", func(w http.ResponseWriter, req *http.Request) { na.handle(u, w, req) })
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/vehicles/rv1/now", nil))
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	d := get(demoID)
	amb, _ := d["ambient"].(map[string]any)
	loc, _ := d["location"].(map[string]any)
	if amb == nil || amb["condition"] == nil || amb["wind_kph"] == nil || amb["rain_next3h_mm"] == nil || amb["temp_c"] == nil {
		t.Errorf("demo ambient = %v", d["ambient"])
	}
	if loc == nil || loc["lat"] != 34.1613 || loc["lon"] != -118.1676 {
		t.Errorf("demo location = %v", d["location"])
	}
	o := get(uuid.New())
	if _, has := o["location"]; has {
		t.Errorf("a real user got a location: %v", o)
	}
}

var logTimeAttr = regexp.MustCompile(`(?m)^time=\S+`)

// A failing upstream gives no value and nothing about where the truck is in the
// logs (an HTTP client's error text carries the request URL).
func TestAmbientFailureLogsNoCoordinates(t *testing.T) {
	tr := &ambientTransport{fail: true}
	var logs bytes.Buffer
	look := ambientFixture(true, tr, &logs)
	if got := look(context.Background(), uuid.New(), 30.2672, -97.7431); got != nil {
		t.Errorf("got %+v from a failing upstream", got)
	}
	out := logs.String()
	if !strings.Contains(out, "ambient temperature unavailable") {
		t.Errorf("no log line: %q", out)
	}
	// The record's own timestamp can contain "30.2" (e.g. 05:29:30.287Z), so
	// drop the time= attribute before looking for coordinates.
	out = logTimeAttr.ReplaceAllString(out, "")
	for _, leak := range []string{"30.2", "97.7", "latitude", "longitude", "open-meteo.com"} {
		if strings.Contains(out, leak) {
			t.Errorf("the log leaks %q: %s", leak, out)
		}
	}
}

func TestAmbientDisabledWithoutAClientOrACache(t *testing.T) {
	if ambientLookup(nil, &weather.Client{}, newAmbientCache(), nil, nil) != nil || ambientLookup(func(context.Context, uuid.UUID) bool { return true }, nil, newAmbientCache(), nil, nil) != nil {
		t.Error("lookup built without an opt-in source or a client")
	}
}

// The /now handler returns the looked-up value, and never the position.
func TestNowReturnsAmbientButNeverTheCoordinates(t *testing.T) {
	tr := &ambientTransport{}
	var logs bytes.Buffer
	look := ambientFixture(true, tr, &logs)
	na := nowAPI{
		snapshot: func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
			return nowSnapshot{State: "parked", lat: 30.2672, lon: -97.7431, hasPos: true}, nil
		},
		connection: func(uuid.UUID) string { return "live" },
		ambient:    look,
		now:        time.Now,
	}
	serve := func() *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Get("/vehicles/{vehicleID}/now", func(w http.ResponseWriter, req *http.Request) { na.handle(uuid.New(), w, req) })
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/vehicles/rv1/now", nil))
		return rec
	}
	rec := serve()
	body := rec.Body.String()
	var got struct {
		Ambient *nowAmbient `json:"ambient"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Ambient == nil || got.Ambient.TempC != 7.4 {
		t.Fatalf("body %s (%v)", body, err)
	}
	for _, leak := range []string{"30.2", "97.7", "lat", "lon"} {
		if strings.Contains(body, leak) {
			t.Errorf("the response leaks %q: %s", leak, body)
		}
	}
	// Opted out: the field is absent.
	off := ambientFixture(false, &ambientTransport{}, &logs)
	na.ambient = off
	rec = serve()
	if strings.Contains(rec.Body.String(), "ambient") {
		t.Errorf("ambient present with the toggle off: %s", rec.Body.String())
	}
}

func TestAmbientPutRefusedWhileViewingAs(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/api/settings/ambient-temp", strings.NewReader(`{"enabled":true}`))
	req = req.WithContext(auth.WithImpersonation(req.Context(), uuid.New(), uuid.New()))
	w := httptest.NewRecorder()
	handleAmbientTempPut(nil)(uuid.New(), w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code=%d, want 403 for an admin viewing as a user", w.Code)
	}
}

func TestAmbientNoLookupWhileViewingAsTheUser(t *testing.T) {
	tr := &ambientTransport{}
	var logs bytes.Buffer
	look := ambientFixture(true, tr, &logs)
	ctx := auth.WithImpersonation(context.Background(), uuid.New(), uuid.New())
	if got := look(ctx, uuid.New(), 30.2672, -97.7431); got != nil || tr.calls.Load() != 0 {
		t.Errorf("while viewing as: %+v, %d upstream calls, want none", got, tr.calls.Load())
	}
	// The same user's own session still gets the value.
	if got := look(context.Background(), uuid.New(), 30.2672, -97.7431); got == nil {
		t.Error("the user's own session got nothing")
	}
}

// The view-as path end to end: the real impersonation middleware (which is what
// sets the impersonation context in production) in front of the real /now handler.
// An admin who views as an opted-in user must not trigger a weather lookup or see
// the field; the user's own request still gets it.
func TestAmbientViewAsThroughImpersonationMiddleware(t *testing.T) {
	admin, target := uuid.New(), uuid.New()
	roles := roleLookupStub(map[uuid.UUID]string{admin: "admin", target: "user"})
	tr := &ambientTransport{}
	var logs bytes.Buffer
	na := nowAPI{
		snapshot: func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
			return nowSnapshot{State: "parked", lat: 30.2672, lon: -97.7431, hasPos: true}, nil
		},
		connection: func(uuid.UUID) string { return "live" },
		ambient:    ambientFixture(true, tr, &logs),
		now:        time.Now,
	}
	// uid is the identity the handler serves: the target while viewing as, else the caller.
	h := func(w http.ResponseWriter, r *http.Request) {
		uid, _ := auth.UserFromContext(r.Context())
		na.handle(uid, w, r)
	}
	r := chi.NewRouter()
	r.Use(impersonationMW(roles, alwaysEnabled))
	r.Get("/api/vehicles/{vehicleID}/now", h)

	get := func(viewAs bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/vehicles/rv1/now", nil)
		req = req.WithContext(auth.WithUser(req.Context(), admin))
		if viewAs {
			req.Header.Set(ImpersonateHeader, target.String())
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	w := get(true)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "ambient") || tr.calls.Load() != 0 {
		t.Fatalf("view-as: code %d, %d upstream calls, body %s; want 200, none, no ambient", w.Code, tr.calls.Load(), w.Body.String())
	}
	// Without the header the same request is the caller's own and does look up.
	if w := get(false); !strings.Contains(w.Body.String(), "ambient") || tr.calls.Load() != 1 {
		t.Fatalf("own session: %d upstream calls, body %s; want one lookup and ambient", tr.calls.Load(), w.Body.String())
	}
}
