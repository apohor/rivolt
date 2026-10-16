package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestConnectionState(t *testing.T) {
	cases := []struct {
		replay, paused, reauth bool
		want                   string
	}{
		{false, false, false, "live"},
		{false, false, true, "reauth"},
		{false, true, true, "paused"},
		{true, true, true, "replay"},
	}
	for _, c := range cases {
		if got := connectionState(c.replay, c.paused, c.reauth); got != c.want {
			t.Errorf("%+v = %s, want %s", c, got, c.want)
		}
	}
}

func nowGet(t *testing.T, a nowAPI) (int, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/vehicles/{vehicleID}/now", func(w http.ResponseWriter, req *http.Request) { a.handle(uuid.New(), w, req) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vehicles/rv-1/now", nil))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func TestNowHandler(t *testing.T) {
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	since := at.Add(-40 * time.Minute)
	soc, peak, tire := 41.0, 148.0, 2.6
	a := nowAPI{
		snapshot: func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
			return nowSnapshot{At: &at, State: "charging", StateSince: &since, SoCAtStateStart: &soc,
				ChargePeakKW: &peak, TirePressureMinB: &tire, FailedSleeps6h: 2}, nil
		},
		connection: func(uuid.UUID) string { return "live" },
		now:        func() time.Time { return at },
	}
	code, m := nowGet(t, a)
	if code != 200 || m["state"] != "charging" || m["connection"] != "live" || m["failed_sleeps_6h"] != float64(2) {
		t.Fatalf("%d %v", code, m)
	}
	ch, ok := m["charge"].(map[string]any)
	if !ok || ch["peak_kw"] != 148.0 || ch["soc_start"] != 41.0 {
		t.Fatalf("charge: %v", m["charge"])
	}
	if m["location_fix_at"] != nil {
		t.Fatalf("location_fix_at should be null: %v", m["location_fix_at"])
	}

	// Not charging: no charge object; empty state reads unknown.
	a.snapshot = func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
		return nowSnapshot{}, nil
	}
	_, m = nowGet(t, a)
	if m["charge"] != nil || m["state"] != "unknown" || m["at"] != nil || m["tire_pressure_min_bar"] != nil {
		t.Fatalf("empty: %v", m)
	}
}

// asleep is independent of the displayed state: a plugged truck whose
// newest row is power_state=sleep reads state "plugged" and asleep true.
func TestNowAsleepFlagIndependentOfState(t *testing.T) {
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	mk := func(asleep bool) nowAPI {
		return nowAPI{
			snapshot: func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
				return nowSnapshot{At: &at, State: "plugged", Asleep: asleep}, nil
			},
			connection: func(uuid.UUID) string { return "live" },
			now:        func() time.Time { return at },
		}
	}
	_, m := nowGet(t, mk(true))
	if m["state"] != "plugged" || m["asleep"] != true {
		t.Fatalf("plugged+asleep: %v", m)
	}
	_, m = nowGet(t, mk(false))
	if m["state"] != "plugged" || m["asleep"] != false {
		t.Fatalf("plugged+awake: %v", m)
	}
	// An empty snapshot still carries the field (false).
	a := mk(false)
	a.snapshot = func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) { return nowSnapshot{}, nil }
	_, m = nowGet(t, a)
	if v, ok := m["asleep"]; !ok || v != false {
		t.Fatalf("empty: asleep = %v (present %v)", v, ok)
	}
}

// vehicle_state_frame_at is always present: the feed's last frame time
// while the connection is live (or replay), null otherwise.
func TestNowVehicleStateFrameAt(t *testing.T) {
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	frame := at.Add(-90 * time.Second)
	conn := "live"
	a := nowAPI{
		snapshot: func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
			return nowSnapshot{At: &at, State: "parked", Asleep: true}, nil
		},
		connection: func(uuid.UUID) string { return conn },
		frameAt:    func(context.Context, uuid.UUID, string) *time.Time { return &frame },
		now:        func() time.Time { return at },
	}
	for _, c := range []struct {
		conn string
		want any
	}{{"live", "2026-10-13T11:58:30Z"}, {"replay", "2026-10-13T11:58:30Z"}, {"paused", nil}, {"reauth", nil}} {
		conn = c.conn
		_, m := nowGet(t, a)
		if v, ok := m["vehicle_state_frame_at"]; !ok || v != c.want {
			t.Errorf("%s: vehicle_state_frame_at = %v (present %v), want %v", c.conn, v, ok, c.want)
		}
	}
	// No live feed to judge (or no monitor wired): null, still present.
	conn = "live"
	a.frameAt = func(context.Context, uuid.UUID, string) *time.Time { return nil }
	if _, m := nowGet(t, a); m["vehicle_state_frame_at"] != nil {
		t.Errorf("no feed: %v", m["vehicle_state_frame_at"])
	}
	a.frameAt = nil
	if _, m := nowGet(t, a); m["vehicle_state_frame_at"] != nil {
		t.Errorf("no seam: %v", m["vehicle_state_frame_at"])
	}
}

func TestNowRunCache(t *testing.T) {
	c := newNowRunCache(60 * time.Second)
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	alice, bob := uuid.New(), uuid.New()
	since := t0.Add(-time.Hour)
	c.put(alice, "rv-1", nowRun{state: "parked", since: &since, failed: 2}, t0)

	if r, ok := c.get(alice, "rv-1", "parked", t0.Add(30*time.Second)); !ok || r.failed != 2 || !r.since.Equal(since) {
		t.Fatalf("hit expected: %+v %v", r, ok)
	}
	if _, ok := c.get(alice, "rv-1", "charging", t0.Add(time.Second)); ok {
		t.Fatal("state change must invalidate")
	}
	if _, ok := c.get(alice, "rv-1", "parked", t0.Add(61*time.Second)); ok {
		t.Fatal("entry must expire after the TTL")
	}
	if _, ok := c.get(bob, "rv-1", "parked", t0.Add(time.Second)); ok {
		t.Fatal("cache must be per user")
	}
	if _, ok := c.get(alice, "rv-2", "parked", t0.Add(time.Second)); ok {
		t.Fatal("cache must be per vehicle")
	}
}
