package planleg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/rivian"
)

type countingPlanner struct {
	calls int
	plan  *rivian.TripPlan
	err   error
}

func (p *countingPlanner) PlanTrip(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) {
	p.calls++
	return p.plan, p.err
}

func setup(t *testing.T) (*Cache, *miniredis.Miniredis, *time.Time) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := New(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "test", nil)
	clock := time.Date(2026, 10, 20, 14, 7, 30, 0, time.UTC)
	c.now = func() time.Time { return clock }
	return c, mr, &clock
}

func in() rivian.PlanTripInput {
	tt := true
	tg := 15.0
	return rivian.PlanTripInput{
		VehicleID: "v1", StartingSoC: 80.2, DriveMode: "EVERYDAY", HasAdapter: &tt, TargetArrivalSocPercent: &tg,
		DepartureTime:      time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
		NetworkPreferences: []rivian.NetworkPreference{{NetworkID: "10027", Preference: 1}, {NetworkID: "10050", Preference: 1}},
		Waypoints: []rivian.PlanTripWaypoint{
			{Latitude: 34.16131, Longitude: -118.16762}, {Latitude: 36.16989, Longitude: -115.13981},
		},
	}
}

var uid = uuid.MustParse("11111111-2222-3333-4444-555555555555")

func TestRepeatedLegIsServedFromCache(t *testing.T) {
	c, mr, _ := setup(t)
	inner := &countingPlanner{plan: &rivian.TripPlan{Status: "OK", Routes: []rivian.TripRoute{{ArrivalSoC: 42}}}}
	p := c.Wrap(inner, uid)

	a, err := p.PlanTrip(context.Background(), in())
	if err != nil || a.Routes[0].ArrivalSoC != 42 || inner.calls != 1 {
		t.Fatalf("first call: %v, %v, upstream calls %d", a, err, inner.calls)
	}
	b, _ := p.PlanTrip(context.Background(), in())
	if inner.calls != 1 || b.Routes[0].ArrivalSoC != 42 {
		t.Errorf("a repeat must come from the cache (upstream calls %d)", inner.calls)
	}
	// The cached entry expires after 15 minutes.
	mr.FastForward(TTL + time.Second)
	_, _ = p.PlanTrip(context.Background(), in())
	if inner.calls != 2 {
		t.Errorf("after the TTL the planner must be asked again (calls %d)", inner.calls)
	}
}

func TestKeyNormalisation(t *testing.T) {
	c, _, clock := setup(t)
	base := c.legKey(uid, in(), *clock)
	same := map[string]func(*rivian.PlanTripInput){
		"soc within the same percent": func(i *rivian.PlanTripInput) { i.StartingSoC = 79.6 },
		"waypoints within 1e-4":       func(i *rivian.PlanTripInput) { i.Waypoints[0].Latitude += 0.00003 },
		"departure within 15 minutes": func(i *rivian.PlanTripInput) { i.DepartureTime = i.DepartureTime.Add(14 * time.Minute) },
		"network order": func(i *rivian.PlanTripInput) {
			i.NetworkPreferences[0], i.NetworkPreferences[1] = i.NetworkPreferences[1], i.NetworkPreferences[0]
		},
		"an empty route preference":     func(i *rivian.PlanTripInput) { i.RoutePreferences = &rivian.RoutePreferences{} },
		"starting range is not keyed":   func(i *rivian.PlanTripInput) { i.StartingRangeMeters = 123456 },
		"the destination's own bearing": func(i *rivian.PlanTripInput) { i.OriginBearing = 90 },
	}
	for name, mut := range same {
		x := in()
		mut(&x)
		if got := c.legKey(uid, x, *clock); got != base {
			t.Errorf("%s changed the key", name)
		}
	}
	differ := map[string]func(*rivian.PlanTripInput){
		"vehicle":          func(i *rivian.PlanTripInput) { i.VehicleID = "v2" },
		"soc by a point":   func(i *rivian.PlanTripInput) { i.StartingSoC = 81.2 },
		"a waypoint moved": func(i *rivian.PlanTripInput) { i.Waypoints[1].Longitude += 0.0002 },
		"a via added": func(i *rivian.PlanTripInput) {
			i.Waypoints = append(i.Waypoints[:1], rivian.PlanTripWaypoint{Latitude: 35, Longitude: -117}, i.Waypoints[1])
		},
		"drive mode":           func(i *rivian.PlanTripInput) { i.DriveMode = "SPORT" },
		"trailer":              func(i *rivian.PlanTripInput) { i.TrailerProfile = "small" },
		"adapter":              func(i *rivian.PlanTripInput) { f := false; i.HasAdapter = &f },
		"adapter unset":        func(i *rivian.PlanTripInput) { i.HasAdapter = nil },
		"target":               func(i *rivian.PlanTripInput) { v := 20.0; i.TargetArrivalSocPercent = &v },
		"target unset":         func(i *rivian.PlanTripInput) { i.TargetArrivalSocPercent = nil },
		"a network preference": func(i *rivian.PlanTripInput) { i.NetworkPreferences = i.NetworkPreferences[:1] },
		"departure 15 min on":  func(i *rivian.PlanTripInput) { i.DepartureTime = i.DepartureTime.Add(15 * time.Minute) },
	}
	for name, mut := range differ {
		x := in()
		mut(&x)
		if c.legKey(uid, x, *clock) == base {
			t.Errorf("%s must change the key", name)
		}
	}
	if c.legKey(uuid.New(), in(), *clock) == base {
		t.Error("another user must not share a cache entry")
	}
	// A departure that is not in the future is "now": the same 15-minute slot is
	// the same key, the next slot is a different one.
	past := in()
	past.DepartureTime = time.Time{}
	k1 := c.legKey(uid, past, *clock)
	past.DepartureTime = clock.Add(-3 * time.Hour)
	if c.legKey(uid, past, *clock) != k1 {
		t.Error("a zero and a past departure are both \"now\"")
	}
	if c.legKey(uid, past, clock.Add(15*time.Minute)) == k1 {
		t.Error("\"now\" must move with the 15-minute slot")
	}
}

func TestQuotaCountsUpstreamCallsOnly(t *testing.T) {
	c, _, clock := setup(t)
	inner := &countingPlanner{plan: &rivian.TripPlan{Status: "OK"}}
	p := c.Wrap(inner, uid)
	// 60 different legs are allowed (each a miss); cache hits do not count.
	for i := 0; i < QuotaPerHour; i++ {
		x := in()
		x.Waypoints[0].Latitude = 30 + float64(i)/10
		if _, err := p.PlanTrip(context.Background(), x); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	for i := 0; i < 20; i++ { // repeats of an earlier leg: free
		x := in()
		x.Waypoints[0].Latitude = 30
		if _, err := p.PlanTrip(context.Background(), x); err != nil {
			t.Fatalf("a cached repeat was refused: %v", err)
		}
	}
	if inner.calls != QuotaPerHour {
		t.Fatalf("upstream calls = %d, want %d", inner.calls, QuotaPerHour)
	}
	// The 61st distinct leg is refused, with the time to the next hour.
	x := in()
	x.Waypoints[0].Latitude = 40
	_, err := p.PlanTrip(context.Background(), x)
	q, ok := IsQuota(err)
	if !ok {
		t.Fatalf("61st leg = %v, want a QuotaError", err)
	}
	if want := time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC).Sub(*clock); q.RetryAfter != want {
		t.Errorf("RetryAfter = %v, want %v", q.RetryAfter, want)
	}
	if inner.calls != QuotaPerHour {
		t.Error("a refused call must not reach the planner")
	}
	// Another user is unaffected; the next hour resets the count.
	if _, err := c.Wrap(inner, uuid.New()).PlanTrip(context.Background(), x); err != nil {
		t.Errorf("another user: %v", err)
	}
	*clock = clock.Add(time.Hour)
	if _, err := p.PlanTrip(context.Background(), x); err != nil {
		t.Errorf("next hour: %v", err)
	}
}

func TestSimulatedPlansAreCachedAndCountedErrorsAreNot(t *testing.T) {
	c, _, _ := setup(t)
	// A simulated (Valhalla-backed) plan is deterministic for its inputs, and
	// costs a route: it is cached and counts against the quota like any other.
	sim := &countingPlanner{plan: &rivian.TripPlan{Status: "OK", Simulated: true}}
	p := c.Wrap(sim, uid)
	a, _ := p.PlanTrip(context.Background(), in())
	b, _ := p.PlanTrip(context.Background(), in())
	if sim.calls != 1 || !a.Simulated || !b.Simulated {
		t.Errorf("a simulated plan is cached, and stays marked simulated (calls %d)", sim.calls)
	}
	for i := 0; i < QuotaPerHour; i++ {
		x := in()
		x.Waypoints[0].Latitude = 30 + float64(i)/10
		_, _ = p.PlanTrip(context.Background(), x)
	}
	x := in()
	x.Waypoints[0].Latitude = 45
	if _, err := p.PlanTrip(context.Background(), x); !errors.As(err, new(*QuotaError)) {
		t.Errorf("simulated planner calls count toward the quota: %v", err)
	}
	boom := &countingPlanner{err: errors.New("upstream")}
	pb := c.Wrap(boom, uuid.New()) // a user with quota left
	_, _ = pb.PlanTrip(context.Background(), in())
	_, _ = pb.PlanTrip(context.Background(), in())
	if boom.calls != 2 {
		t.Errorf("an error must not be cached (calls %d)", boom.calls)
	}
}

// With Redis down planning still works (no cache), and the quota still holds: counted in this
// process as this pod's share of the 60 (60 / 3 replicas = 20), so a Redis outage is not an
// unlimited Rivian budget and the replicas together stay inside it.
func TestRedisDownKeepsAPerProcessQuota(t *testing.T) {
	c, mr, _ := setup(t)
	mr.Close()
	const share = 20 // ceil(60 / 3)
	var fallbacks int
	c.OnFallback = func() { fallbacks++ }
	inner := &countingPlanner{plan: &rivian.TripPlan{Status: "OK"}}
	p := c.Wrap(inner, uid)
	start := time.Now()
	for i := 0; i < share; i++ {
		if _, err := p.PlanTrip(context.Background(), in()); err != nil {
			t.Fatalf("call %d with Redis down: %v", i, err)
		}
	}
	_, err := p.PlanTrip(context.Background(), in())
	if q, ok := IsQuota(err); !ok || q.RetryAfter <= 0 {
		t.Fatalf("call %d with Redis down: %v, want the quota error (a Redis outage must not lift the quota)", share+1, err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("%d planner calls took %v with Redis down: the cache must not slow planning", share+1, d)
	}
	if inner.calls != share {
		t.Errorf("upstream calls = %d, want %d (the 21st is refused before it goes upstream)", inner.calls, share)
	}
	if fallbacks != share+1 {
		t.Errorf("fallbacks counted = %d, want %d", fallbacks, share+1)
	}
	// Another user has their own count; the next hour starts over.
	other := uuid.MustParse("99999999-2222-3333-4444-555555555555")
	if _, err := c.Wrap(inner, other).PlanTrip(context.Background(), in()); err != nil {
		t.Errorf("another user was refused: %v", err)
	}
	c.now = func() time.Time { return time.Date(2026, 10, 20, 15, 0, 1, 0, time.UTC) }
	if _, err := p.PlanTrip(context.Background(), in()); err != nil {
		t.Errorf("the quota did not reset with the hour: %v", err)
	}
}

func TestNilCacheWrapsNothing(t *testing.T) {
	inner := &countingPlanner{plan: &rivian.TripPlan{}}
	var c *Cache
	if c.Wrap(inner, uid) != rivian.TripPlanner(inner) {
		t.Error("a nil Cache must return the planner unchanged")
	}
}

// An install without Redis has no leg cache but keeps the quota, per process.
func TestNoRedisKeepsAPerProcessQuota(t *testing.T) {
	c := New(nil, "x", nil)
	if c == nil {
		t.Fatal("no Redis must still give a Cache for the quota")
	}
	inner := &countingPlanner{plan: &rivian.TripPlan{Status: "OK"}}
	p := c.Wrap(inner, uid)
	for i := 0; i < QuotaPerHour+3; i++ {
		_, err := p.PlanTrip(context.Background(), in())
		if i < QuotaPerHour && err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if i >= QuotaPerHour {
			if _, ok := IsQuota(err); !ok {
				t.Fatalf("call %d: %v, want the quota error", i, err)
			}
		}
	}
	if inner.calls != QuotaPerHour {
		t.Errorf("upstream calls = %d, want %d (no cache without Redis, quota on)", inner.calls, QuotaPerHour)
	}
}

// The share follows the configured number of replicas, never below one call.
func TestFallbackQuotaIsAShareOfTheHourlyQuota(t *testing.T) {
	c, mr, _ := setup(t)
	mr.Close()
	for div, want := range map[float64]int{0: 20, 1: 60, 2: 30, 3: 20, 4: 15, 7: 9, 60: 1, 500: 1} {
		c.FallbackDivisor = div
		if got := c.fallbackQuota(); got != want {
			t.Errorf("divisor %v: %d a user an hour, want %d", div, got, want)
		}
	}
	// With no Redis at all this process is the whole quota.
	if got := New(nil, "x", nil).fallbackQuota(); got != QuotaPerHour {
		t.Errorf("no Redis: %d, want %d", got, QuotaPerHour)
	}
}
