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
	var path, vars string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, vars = r.URL.Path, r.URL.Query().Get("hourly")
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
