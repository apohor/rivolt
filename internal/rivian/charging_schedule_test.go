package rivian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Home and a spot ~2 km away.
const (
	schedHomeLat, schedHomeLon = 34.1478, -118.1445
	schedAwayLat, schedAwayLon = 34.1650, -118.1445
)

func TestLiveClientChargingSchedules(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"getVehicle":{"chargingSchedules":[
			{"startTime":1380,"duration":420,"location":{"latitude":34.1478,"longitude":-118.1445},"amperage":32,"enabled":true,"weekDays":["Monday","Tuesday"]},
			{"startTime":0,"duration":1440,"location":{"latitude":0,"longitude":0},"amperage":48,"enabled":false,"weekDays":[]}]}}}`)
	}))
	defer server.Close()
	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	c.userSessionToken = "u"
	got, err := c.ChargingSchedules(context.Background(), "veh-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"operationName":"GetChargingSchedule"`) || !strings.Contains(body, `"vehicleId":"veh-1"`) {
		t.Errorf("request body: %s", body)
	}
	if len(got) != 2 {
		t.Fatalf("got %d schedules", len(got))
	}
	s := got[0]
	if s.StartMin != 1380 || s.DurationMin != 420 || !s.Enabled || s.AmperageA != 32 ||
		strings.Join(s.WeekDays, ",") != "Monday,Tuesday" || !s.hasLoc {
		t.Errorf("first schedule = %+v", s)
	}
	if got[1].hasLoc {
		t.Error("a 0,0 location counted as a location")
	}
}

func TestChargingScheduleNeverSerializesLocation(t *testing.T) {
	s := ChargingSchedule{StartMin: 1380, DurationMin: 420, Enabled: true, lat: schedHomeLat, lon: schedHomeLon, hasLoc: true}
	b, err := json.Marshal(State{ChargingSchedules: withAppliesHere([]ChargingSchedule{s}, schedHomeLat, schedHomeLon)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "34.14") || strings.Contains(string(b), "118.14") {
		t.Errorf("schedule location leaked into JSON: %s", b)
	}
	if !strings.Contains(string(b), `"applies_here":true`) {
		t.Errorf("applies_here missing: %s", b)
	}
}

func TestWithAppliesHere(t *testing.T) {
	at := ChargingSchedule{lat: schedHomeLat, lon: schedHomeLon, hasLoc: true}
	none := ChargingSchedule{}
	got := withAppliesHere([]ChargingSchedule{at, none}, schedHomeLat+0.001, schedHomeLon) // ~110 m
	if got[0].AppliesHere == nil || !*got[0].AppliesHere {
		t.Error("110 m from home: want applies_here true")
	}
	if got[1].AppliesHere != nil {
		t.Error("a schedule without a location got applies_here")
	}
	if got := withAppliesHere([]ChargingSchedule{at}, schedAwayLat, schedAwayLon); got[0].AppliesHere == nil || *got[0].AppliesHere {
		t.Error("2 km away: want applies_here false")
	}
	if got := withAppliesHere([]ChargingSchedule{at}, 0, 0); got[0].AppliesHere != nil {
		t.Error("unknown truck position: want applies_here nil")
	}
}

func schedMonitor(lat, lon float64) (*StateMonitor, *atomic.Int32) {
	m := NewStateMonitor(nil, nil)
	var calls atomic.Int32
	m.fetchSchedules = func(context.Context, string) ([]ChargingSchedule, error) {
		calls.Add(1)
		return []ChargingSchedule{{StartMin: 1380, DurationMin: 420, Enabled: true,
			WeekDays: []string{"Monday"}, lat: schedHomeLat, lon: schedHomeLon, hasLoc: true}}, nil
	}
	m.cache["v"] = &State{VehicleID: "v", Latitude: lat, Longitude: lon}
	return m, &calls
}

func TestReadChargingSchedulesCadence(t *testing.T) {
	ctx := context.Background()
	m, calls := schedMonitor(schedHomeLat, schedHomeLon)
	m.readChargingSchedules(ctx, "v", scheduleConnectMinAge)
	if calls.Load() != 1 {
		t.Fatalf("first read: %d calls", calls.Load())
	}
	sch := m.cache["v"].ChargingSchedules
	if len(sch) != 1 || sch[0].AppliesHere == nil || !*sch[0].AppliesHere {
		t.Fatalf("cached schedules = %+v", sch)
	}
	m.readChargingSchedules(ctx, "v", scheduleNudgeMinAge) // same place, just read
	if calls.Load() != 1 {
		t.Errorf("a nudge right after a read at the same place fetched again")
	}
	m.mu.Lock()
	next := *m.cache["v"]
	next.Latitude, next.Longitude = schedAwayLat, schedAwayLon
	m.cache["v"] = &next
	m.mu.Unlock()
	m.readChargingSchedules(ctx, "v", scheduleNudgeMinAge) // moved: read again
	if calls.Load() != 2 {
		t.Errorf("a nudge after moving 2 km didn't fetch")
	}
	if a := m.cache["v"].ChargingSchedules[0].AppliesHere; a == nil || *a {
		t.Errorf("away from home: applies_here %v, want false", a)
	}
	m.readChargingSchedules(ctx, "v", 0) // daily: always
	if calls.Load() != 3 {
		t.Errorf("the daily read was skipped")
	}
}

func TestSchedulesSurviveMerges(t *testing.T) {
	cached := &State{ChargingSchedules: []ChargingSchedule{{StartMin: 60}}}
	if got := mergeState(cached, &State{ChargerState: "charging_ready"}); len(got.ChargingSchedules) != 1 {
		t.Error("a vehicleState push dropped the schedules")
	}
	if got := mergeState(&State{ChargerState: "charging_ready"}, cached); len(got.ChargingSchedules) != 1 {
		t.Error("a REST fold under the cache dropped the schedules")
	}
}

func TestScheduleNudgeOnPlugIn(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	ch := m.scheduleNudgeChan("v")
	unplugged := &State{ChargerState: "charging_ready", ChargerStatus: "chrgr_sts_not_connected"}
	idle := &State{ChargerState: "charging_ready", ChargerStatus: "chrgr_sts_connected_no_chrg"}
	charging := &State{ChargerState: "charging_active", ChargerStatus: "chrgr_sts_connected_charging"}
	scheduled := &State{ChargerState: "charging_scheduled"} // an R2 before its plug status arrives
	pending := func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	m.noteScheduleNudge("v", unplugged, idle)
	if !pending() {
		t.Error("plug-in, not charging: no nudge")
	}
	m.noteScheduleNudge("v", idle, idle)
	if pending() {
		t.Error("still idle: nudged again")
	}
	m.noteScheduleNudge("v", unplugged, charging)
	if pending() {
		t.Error("plugged in and charging: nudged")
	}
	m.noteScheduleNudge("v", unplugged, scheduled)
	if !pending() {
		t.Error("charging_scheduled without a plug status: no nudge")
	}
}

func TestChargingScheduleLoopReadsOnConnectAndNudge(t *testing.T) {
	oldJitter := scheduleConnectJitter
	scheduleConnectJitter = 0
	defer func() { scheduleConnectJitter = oldJitter }()
	m, calls := schedMonitor(schedHomeLat, schedHomeLon)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.chargingScheduleLoop(ctx, "v"); close(done) }()
	wait := func(n int32) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); calls.Load() < n; {
			if time.Now().After(deadline) {
				t.Fatalf("calls = %d, want %d", calls.Load(), n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait(1) // connect
	// The loop records its read after the call returns; backdating it before that
	// would hit a nil map or be overwritten (a flake under load). Wait for the record.
	for deadline := time.Now().Add(2 * time.Second); ; {
		m.mu.Lock()
		_, recorded := m.scheduleReads["v"]
		m.mu.Unlock()
		if recorded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the loop never recorded its read")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.mu.Lock()
	m.scheduleReads["v"] = scheduleRead{at: time.Now().Add(-time.Hour), lat: schedHomeLat, lon: schedHomeLon}
	m.mu.Unlock()
	m.noteScheduleNudge("v", &State{}, &State{ChargerState: "charging_scheduled"})
	wait(2) // plug-in nudge
	cancel()
	<-done
}

// Rivian refuses the schedules of shared and authorized-driver accounts
// (FORBIDDEN). That is not an error: the vehicle is marked "not available",
// the read is not repeated for a week (not on connect, nudge or daily tick),
// and a later success clears the mark.
func TestChargingSchedulesForbiddenIsNotAvailableAndBacksOff(t *testing.T) {
	ctx := context.Background()
	m := NewStateMonitor(nil, nil)
	var calls int
	forbidden := &UpstreamError{Class: ClassUserAction, Op: "GetChargingSchedule", ExtCode: "FORBIDDEN", Cause: errors.New("User is forbidden from accessing this resource")}
	var next error = forbidden
	m.fetchSchedules = func(context.Context, string) ([]ChargingSchedule, error) {
		calls++
		if next != nil {
			return nil, next
		}
		return []ChargingSchedule{{StartMin: 60, DurationMin: 120, Enabled: true}}, nil
	}
	m.cache["v"] = &State{VehicleID: "v", ChargingSchedules: []ChargingSchedule{{StartMin: 1}}}

	m.readChargingSchedules(ctx, "v", scheduleConnectMinAge)
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if st := m.cache["v"]; !st.ChargingSchedulesUnavailable || st.ChargingSchedules != nil {
		t.Errorf("state = unavailable %v schedules %v; want marked unavailable and no stale list", st.ChargingSchedulesUnavailable, st.ChargingSchedules)
	}
	for _, age := range []time.Duration{0, scheduleNudgeMinAge, scheduleConnectMinAge} {
		m.readChargingSchedules(ctx, "v", age)
	}
	if calls != 1 {
		t.Errorf("read again inside the back-off: %d calls", calls)
	}
	// The mark survives a vehicleState push.
	if got := mergeState(m.cache["v"], &State{ChargerState: "charging_ready"}); !got.ChargingSchedulesUnavailable {
		t.Error("a push dropped the unavailable mark")
	}
	// A week on, one more try; a success clears the mark.
	m.mu.Lock()
	m.scheduleUnavailableUntil["v"] = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	next = nil
	m.readChargingSchedules(ctx, "v", 0)
	if calls != 2 || m.cache["v"].ChargingSchedulesUnavailable || len(m.cache["v"].ChargingSchedules) != 1 {
		t.Errorf("after the back-off: calls %d unavailable %v schedules %v", calls, m.cache["v"].ChargingSchedulesUnavailable, m.cache["v"].ChargingSchedules)
	}
}

// Any other failure is still a warning and is retried as before.
func TestChargingSchedulesOtherErrorsKeepRetrying(t *testing.T) {
	ctx := context.Background()
	m := NewStateMonitor(nil, nil)
	var calls int
	m.fetchSchedules = func(context.Context, string) ([]ChargingSchedule, error) {
		calls++
		return nil, &UpstreamError{Class: ClassOutage, Op: "GetChargingSchedule", HTTPStatus: 502}
	}
	m.cache["v"] = &State{VehicleID: "v"}
	m.readChargingSchedules(ctx, "v", 0)
	m.readChargingSchedules(ctx, "v", 0)
	if calls != 2 || m.cache["v"].ChargingSchedulesUnavailable {
		t.Errorf("calls %d unavailable %v; an outage must not back off or mark", calls, m.cache["v"].ChargingSchedulesUnavailable)
	}
}

// A read refused by the shared Rivian budget (a handover burst) is retried once after the wait it names, and
// the read is recorded when the retry works.
func TestScheduleReadRetriesOnceAfterARateLimit(t *testing.T) {
	oldCap, oldJitter := scheduleRetryCap, scheduleRetryJitter
	scheduleRetryCap, scheduleRetryJitter = 50*time.Millisecond, time.Millisecond
	defer func() { scheduleRetryCap, scheduleRetryJitter = oldCap, oldJitter }()

	m, calls := schedMonitor(schedHomeLat, schedHomeLon)
	failures := 1
	inner := m.fetchSchedules
	m.fetchSchedules = func(ctx context.Context, v string) ([]ChargingSchedule, error) {
		if failures > 0 {
			failures--
			calls.Add(1)
			return nil, &ErrRateLimited{RetryAfter: 10 * time.Millisecond}
		}
		return inner(ctx, v)
	}
	m.readChargingSchedules(context.Background(), "v", scheduleConnectMinAge)
	if calls.Load() != 2 {
		t.Fatalf("fetch calls = %d, want 2 (the refusal and one retry)", calls.Load())
	}
	m.mu.Lock()
	_, recorded := m.scheduleReads["v"]
	m.mu.Unlock()
	if !recorded {
		t.Error("the retried read was not recorded")
	}

	// A second refusal is not retried again (one retry only).
	calls.Store(0)
	failures = 2
	m2, calls2 := schedMonitor(schedHomeLat, schedHomeLon)
	n := 0
	m2.fetchSchedules = func(context.Context, string) ([]ChargingSchedule, error) {
		n++
		calls2.Add(1)
		return nil, &ErrRateLimited{RetryAfter: time.Millisecond}
	}
	m2.readChargingSchedules(context.Background(), "v", scheduleConnectMinAge)
	if n != 2 {
		t.Errorf("fetch calls on a persistent refusal = %d, want 2", n)
	}
}

func TestDescribeNamesOurGatesAndTheRootType(t *testing.T) {
	cases := map[string]error{
		"rate limited, retry after 3s": fmt.Errorf("GetChargingSchedule: %w", &ErrRateLimited{RetryAfter: 3 * time.Second}),
		"rivian upstream breaker open": fmt.Errorf("x: %w", ErrUpstreamBreakerOpen),
		"error (*errors.errorString)":  fmt.Errorf("wrapped: %w", errors.New("secret body text")),
	}
	for want, err := range cases {
		if got := Describe(err); got != want {
			t.Errorf("Describe(%v) = %q, want %q", err, got, want)
		}
	}
	if strings.Contains(Describe(fmt.Errorf("w: %w", errors.New("secret body text"))), "secret") {
		t.Error("Describe leaked the cause's text")
	}
}
