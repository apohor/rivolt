package weather

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type obsLog struct {
	mu  sync.Mutex
	got []string
}

func (o *obsLog) add(ep, oc string) {
	o.mu.Lock()
	o.got = append(o.got, ep+"/"+oc)
	o.mu.Unlock()
}

func (o *obsLog) list() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Join(o.got, ",")
}

// A self-hosted client sends recent times to its own server's forecast API, with the usual variables, and counts the call.
func TestSelfHostedClientUsesItsServerForRecentTimes(t *testing.T) {
	var path, vars, models string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, vars, models = r.URL.Path, r.URL.Query().Get("hourly"), r.URL.Query().Get("models")
		_, _ = w.Write([]byte(`{"hourly":{"time":["` + r.URL.Query().Get("start_date") + `T00:00"],"temperature_2m":[11.5]}}`))
	}))
	defer srv.Close()
	c := NewSelfHostedClient(srv.URL + "/")
	var o obsLog
	c.Observe = o.add
	snap, _, err := c.FetchHour(context.Background(), 30.27, -97.74, time.Now().Add(-48*time.Hour), 0, false)
	if err != nil || snap == nil || !snap.HasTemp || snap.TempC != 11.5 {
		t.Fatalf("snapshot %+v, err %v", snap, err)
	}
	if path != "/v1/forecast" {
		t.Errorf("path %q, want /v1/forecast", path)
	}
	if models != "gem_seamless" {
		t.Errorf("models %q, want gem_seamless (without it the server answers all-null)", models)
	}
	for _, v := range []string{"temperature_2m", "apparent_temperature", "wind_speed_10m", "wind_direction_10m", "precipitation", "relative_humidity_2m", "weather_code"} {
		if !strings.Contains(vars, v) {
			t.Errorf("variable %s missing from %q", v, vars)
		}
	}
	if o.list() != "forecast/ok" {
		t.Errorf("observed %q", o.list())
	}
	// A planned departure in the future also goes to the forecast API.
	if _, _, err := c.FetchHour(context.Background(), 30.27, -97.74, time.Now().Add(72*time.Hour), 0, false); err != nil {
		t.Errorf("future hour: %v", err)
	}
}

// Older than the window: no request at all (our server has no archive), a typed error, and a skipped_old count, for both fetchers.
func TestSelfHostedClientSkipsOldTimesWithoutARequest(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	c := NewSelfHostedClient(srv.URL)
	var o obsLog
	c.Observe = o.add
	old := time.Now().Add(-(selfHostedWindowDays + 1) * 24 * time.Hour)
	if _, _, err := c.FetchHour(context.Background(), 30, -97, old, 0, false); !errors.Is(err, ErrTooOld) {
		t.Errorf("FetchHour err %v, want ErrTooOld", err)
	}
	if _, err := c.FetchRange(context.Background(), 30, -97, old, old.Add(time.Hour), 0, false); !errors.Is(err, ErrTooOld) {
		t.Errorf("FetchRange err %v, want ErrTooOld", err)
	}
	if hits != 0 {
		t.Errorf("%d requests sent for a time the server does not keep", hits)
	}
	if o.list() != "archive/skipped_old,archive/skipped_old" {
		t.Errorf("observed %q", o.list())
	}
}

// The public-API client is unchanged: a time older than 80 days goes to the archive base, and an upstream error is counted.
func TestPublicClientStillUsesTheArchiveAndCountsErrors(t *testing.T) {
	srv, calls := upstreamStub(t, 10, 20, 0)
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	var o obsLog
	c.Observe = o.add
	if _, _, err := c.FetchHour(context.Background(), 30, -97, time.Now().Add(-200*24*time.Hour), 0, false); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || o.list() != "archive/ok" {
		t.Errorf("calls %d observed %q", *calls, o.list())
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	c2 := &Client{HTTP: bad.Client(), BaseURL: bad.URL}
	c2.Observe = o.add
	if _, _, err := c2.FetchHour(context.Background(), 30, -97, time.Now().Add(-200*24*time.Hour), 0, false); err == nil {
		t.Fatal("a 500 was accepted")
	}
	if !strings.HasSuffix(o.list(), "archive/error") {
		t.Errorf("observed %q", o.list())
	}
}

// Snow is the snapshot's own weather code, never the temperature; freezing rain is not snow.
func TestSnapshotSnowing(t *testing.T) {
	if (*Snapshot)(nil).Snowing() {
		t.Error("nil snapshot")
	}
	for label, want := range map[string]bool{"snow": true, "rain": false, "freezing rain": false, "clear sky": false, "": false} {
		s := &Snapshot{TempC: -10, HasTemp: true, Conditions: label, HasConditions: label != ""}
		if got := s.Snowing(); got != want {
			t.Errorf("%q: %v, want %v", label, got, want)
		}
	}
	if (&Snapshot{TempC: -20, HasTemp: true}).Snowing() {
		t.Error("a cold snapshot with no weather code reads as snow")
	}
}

// The series request carries the same model, and the public client sends none (its own best_match is right for the public API).
func TestModelsParameterOnlyOnTheSelfHostedClient(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Query().Get("models"))
		_, _ = w.Write([]byte(`{"minutely_15":{"time":["2026-01-01T00:00"],"temperature_2m":[1]}}`))
	}))
	defer srv.Close()
	self := NewSelfHostedClient(srv.URL)
	now := time.Now()
	_, _ = self.FetchRange(context.Background(), 30, -97, now.Add(-time.Hour), now, 0, false)
	pub := NewClient()
	pub.ForecastURL = srv.URL
	pub.HTTP = srv.Client()
	_, _ = pub.FetchRange(context.Background(), 30, -97, now.Add(-time.Hour), now, 0, false)
	if len(got) != 2 || got[0] != "gem_seamless" || got[1] != "" {
		t.Errorf("models sent: %q, want [gem_seamless \"\"]", got)
	}
}
