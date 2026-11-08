package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/forward/forwardtest"
	"github.com/apohor/rivolt/internal/planleg"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// planForwarder answers every plan_legs with a canned plan and counts the calls.
type planForwarder struct {
	mu      sync.Mutex
	res     forward.Result
	calls   int
	noGroup []forward.Kind
}

func (f *planForwarder) NoGroup(k forward.Kind) { f.noGroup = append(f.noGroup, k) }

func (f *planForwarder) Do(_ context.Context, _ forward.Call, _ func(context.Context) (forward.Result, error)) (forward.Result, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.res, "ok", nil
}

func (f *planForwarder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func samplePlan() *rivian.TripPlan {
	return &rivian.TripPlan{Status: "OK", ChargeStationsAvailable: true, Routes: []rivian.TripRoute{{
		DestinationReached: true, TotalTripDurationSec: 3600, TotalDriveDistanceMeters: 123456.5, ArrivalSoC: 21.5,
		Waypoints: []rivian.PlannedWaypoint{{WaypointType: "origin", Latitude: 34.1, Longitude: -118.2}, {WaypointType: "destination", Latitude: 35.1, Longitude: -117.2, ArrivalTimeUTC: "2026-11-08T10:00:00Z"}},
	}}}
}

func sampleInput() rivian.PlanTripInput {
	soc, yes := 15.0, true
	return rivian.PlanTripInput{
		VehicleID: "v-fake-1", StartingSoC: 80, StartingRangeMeters: 250000, OriginBearing: 90,
		Waypoints:               []rivian.PlanTripWaypoint{{Latitude: 34.1, Longitude: -118.2, WaypointType: "origin"}, {Latitude: 35.1, Longitude: -117.2, WaypointType: "destination", EntityID: "e1"}},
		TargetArrivalSocPercent: &soc, NetworkPreferences: []rivian.NetworkPreference{{NetworkID: "ea", Preference: 1}},
		DriveMode: "SPORT", HasAdapter: &yes, RoutePreferences: &rivian.RoutePreferences{AvoidTolls: true, AvoidHighways: true},
		DepartureTime: time.UnixMilli(1_800_000_000_123),
	}
}

func TestPlanLegsArgsRoundTrip(t *testing.T) {
	in := sampleInput()
	a := planLegsArgs(in)
	if got := PlanTripInputOf(&a); !reflect.DeepEqual(got, in) {
		t.Errorf("round trip changed the input:\n got %+v\nwant %+v", got, in)
	}
	// Optional fields stay absent.
	min := rivian.PlanTripInput{VehicleID: "v-fake-1", Waypoints: in.Waypoints}
	a = planLegsArgs(min)
	if got := PlanTripInputOf(&a); !reflect.DeepEqual(got, min) {
		t.Errorf("minimal input changed: %+v", got)
	}
}

func TestServePlanLegsRoundTripsThePlan(t *testing.T) {
	c := plannerClient{Client: rivian.NewStub(), TripPlanner: planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) { return samplePlan(), nil })}
	res := ServePlanLegs(context.Background(), c, sampleInput())
	got, err := decodeForwardedPlan(res)
	if err != nil || !reflect.DeepEqual(got, samplePlan()) {
		t.Errorf("plan changed across the wire: %+v (%v)", got, err)
	}
}

// Whatever error the owner's planner returns, the requester's handler answers exactly what a local call would have: the same status, the
// same body (class, reason, text) and the same errors.Is/As behaviour.
func TestPlanErrorsCrossTheWireIntact(t *testing.T) {
	errs := map[string]error{
		"user action":   &rivian.UpstreamError{Class: rivian.ClassUserAction, Op: "PlanTrip", HTTPStatus: 401, Reason: "session expired", Cause: errors.New("denied")},
		"rate limited":  &rivian.UpstreamError{Class: rivian.ClassRateLimited, Op: "PlanTrip", HTTPStatus: 429, Cause: errors.New("slow")},
		"outage":        &rivian.UpstreamError{Class: rivian.ClassOutage, Op: "PlanTrip", HTTPStatus: 503, ExtCode: "X"},
		"transient":     fmt.Errorf("plan: %w", &rivian.UpstreamError{Class: rivian.ClassTransient, Op: "PlanTrip"}),
		"our budget":    &rivian.ErrRateLimited{RetryAfter: 7 * time.Second},
		"invalid trip":  fmt.Errorf("%w: waypoints too close", rivian.ErrInvalidTrip),
		"no planner":    rivian.ErrNoPlanner,
		"no fixture":    rivian.ErrNoFixture,
		"needs reauth":  rivian.ErrNeedsReauth,
		"unauthed":      rivian.ErrNotAuthenticated,
		"something new": errors.New("boom"),
	}
	for name, orig := range errs {
		t.Run(name, func(t *testing.T) {
			c := plannerClient{Client: rivian.NewStub(), TripPlanner: errPlanner{orig}}
			_, got := decodeForwardedPlan(ServePlanLegs(context.Background(), c, sampleInput()))
			if got == nil {
				t.Fatal("no error")
			}
			ws, wb := upstreamErrorBody(orig)
			gs, gb := upstreamErrorBody(got)
			if ws != gs || !reflect.DeepEqual(wb, gb) {
				t.Errorf("handler would answer %d %v, now %d %v", ws, wb, gs, gb)
			}
			for _, s := range []error{rivian.ErrInvalidTrip, rivian.ErrNoPlanner, rivian.ErrNoFixture, rivian.ErrNeedsReauth, rivian.ErrNotAuthenticated} {
				if errors.Is(orig, s) != errors.Is(got, s) {
					t.Errorf("errors.Is(%v) differs", s)
				}
			}
			var a, b *rivian.ErrRateLimited
			if errors.As(orig, &a) != errors.As(got, &b) || (a != nil && *a != *b) {
				t.Errorf("ErrRateLimited differs: %v %v", a, b)
			}
			if errClass(orig) != errClass(got) {
				t.Errorf("errClass %q vs %q", errClass(orig), errClass(got))
			}
		})
	}
}

func TestForwardPlannerRouting(t *testing.T) {
	ok := &planForwarder{res: ServePlanLegs(context.Background(), plannerClient{Client: rivian.NewStub(), TripPlanner: planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) { return samplePlan(), nil })}, sampleInput())}
	newSF := func(fw StateForwarder, groupOK bool) *stateForward {
		return &stateForward{fw: fw, user: uuid.New(), groupOf: func(string) (string, bool) { return "g", groupOK }}
	}
	local := 0
	inner := planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) {
		local++
		return &rivian.TripPlan{Status: "LOCAL"}, nil
	})

	p := &forwardPlanner{inner: inner, sf: newSF(ok, true)}
	if plan, err := p.PlanTrip(context.Background(), sampleInput()); err != nil || plan.Status != "OK" || local != 0 || ok.count() != 1 {
		t.Errorf("forwarded: %+v %v local=%d forwards=%d", plan, err, local, ok.count())
	}
	// No group, nil requester: the pod plans itself; an unknown group is counted as no_group for plan_legs, once.
	ok.noGroup = nil
	for name, sf := range map[string]*stateForward{"no group": newSF(ok, false), "nil": nil} {
		before := ok.count()
		p := &forwardPlanner{inner: inner, sf: sf}
		if plan, err := p.PlanTrip(context.Background(), sampleInput()); err != nil || plan.Status != "LOCAL" || ok.count() != before {
			t.Errorf("%s: %+v %v", name, plan, err)
		}
	}
	if len(ok.noGroup) != 1 || ok.noGroup[0] != forward.KindPlanLegs {
		t.Errorf("no_group counted %v, want one plan_legs", ok.noGroup)
	}
	// The owner did not answer: an outage the handler reports as 503, never a silent local call.
	down := &planForwarder{res: forward.Result{Status: forward.StatusUnavailable, Class: forward.ClassUnavailable}}
	before := local
	_, err := (&forwardPlanner{inner: inner, sf: newSF(down, true)}).PlanTrip(context.Background(), sampleInput())
	if st, _ := upstreamErrorBody(err); err == nil || st != 503 || local != before {
		t.Errorf("unavailable: %v status %d local=%d", err, st, local)
	}
}

// The cache and the quota sit in front of the forward: a repeated leg and a leg over the quota make no forward, and a planner that is not
// the live client is never forwarded.
func TestLegCacheAndQuotaComeBeforeTheForward(t *testing.T) {
	mr := miniredis.RunT(t)
	fw := &planForwarder{res: ServePlanLegs(context.Background(), plannerClient{Client: rivian.NewStub(), TripPlanner: planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) { return samplePlan(), nil })}, sampleInput())}
	d := Deps{PlanLegs: planleg.New(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "t", nil), StateForward: fw,
		GroupOfVehicle: func(string) (string, bool) { return "g", true }}
	uid := uuid.New()
	planner, _ := tripPlannerOf(withLegCache(d, uid, rivian.NewLive()))

	in := sampleInput()
	in.DepartureTime = time.Time{}
	if _, err := planner.PlanTrip(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.PlanTrip(context.Background(), in); err != nil || fw.count() != 1 {
		t.Fatalf("the repeat: err %v, %d forwards, want 1 (cache hit)", err, fw.count())
	}
	var qerr error
	for i := 1; i <= planleg.QuotaPerHour+1 && qerr == nil; i++ {
		in2 := in
		in2.StartingSoC = float64(i % 100)
		in2.Waypoints = []rivian.PlanTripWaypoint{{Latitude: 10 + float64(i)/100, Longitude: 20}, {Latitude: 11, Longitude: 21}}
		_, qerr = planner.PlanTrip(context.Background(), in2)
	}
	if _, isQuota := planleg.IsQuota(qerr); !isQuota {
		t.Fatalf("quota never refused: %v", qerr)
	}
	if fw.count() != planleg.QuotaPerHour {
		t.Errorf("%d forwards when the quota refused, want exactly %d (the refused leg makes none)", fw.count(), planleg.QuotaPerHour)
	}

	before := fw.count()
	fp, _ := tripPlannerOf(withLegCache(d, uuid.New(), fixtureClient()))
	_, _ = fp.PlanTrip(context.Background(), in)
	if fw.count() != before {
		t.Error("a non-live planner was forwarded")
	}
}

// Reply size (DA: measure it). The plans the planner returns are small: Rivian's RouteResponseRaw is empty (our own Valhalla adds the
// geometry on the requester, after the cache), so a leg is its stops. The stored plans of the demo are about 1.5-2 KB each. This bounds the
// worst plan we would accept in practice, three routes of forty fully populated stops (a cross-country leg has a dozen or so; a full stop is about 0.5 KB, so the cap holds around 500), at a
// fraction of the reply cap, with room to spare for the signature envelope.
func TestWorstCasePlanFitsTheReplyCapWithRoom(t *testing.T) {
	stop := rivian.PlannedWaypoint{WaypointType: "charger", EntityID: "00000000-0000-0000-0000-000000000000", Name: "A fairly long charging station name, Some Town, ST",
		Latitude: 34.123456, Longitude: -118.123456, MaxPowerKW: 350, ChargeDurationSec: 1800, ArrivalSoC: 12.345678, DepartureSoC: 80.123456,
		ArrivalReachableMeters: 123456.789, DepartureReachableMeters: 234567.891, AdapterRequired: true,
		ArrivalTimeUTC: "2026-11-08T10:00:00Z", DepartureTimeUTC: "2026-11-08T10:30:00Z", DistanceFromOriginMeters: 345678.912}
	plan := &rivian.TripPlan{Status: "OK", ChargeStationsAvailable: true}
	for r := 0; r < 3; r++ {
		route := rivian.TripRoute{DestinationReached: true}
		for i := 0; i < 40; i++ {
			route.Waypoints = append(route.Waypoints, stop)
		}
		plan.Routes = append(plan.Routes, route)
	}
	res := ServePlanLegs(context.Background(), plannerClient{Client: rivian.NewStub(), TripPlanner: planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) { return plan, nil })}, sampleInput())
	if res.Status != 200 {
		t.Fatalf("status %d", res.Status)
	}
	t.Logf("worst plausible plan_legs body: %d bytes (cap %d)", len(res.Body), forward.MaxReplyBytes)
	if limit := forward.MaxReplyBytes / 2; len(res.Body) > limit {
		t.Errorf("a plausible worst-case plan is %d bytes, more than half the %d cap: gzip or a larger cap is needed", len(res.Body), forward.MaxReplyBytes)
	}
}

// An owner that does not answer (the forward timed out, after its one retry): the single plan is 503 with Retry-After 5 and the
// existing "Rivian's planner isn't answering" copy on the web (any 5xx); a multi-day plan names the day and says the planner is busy. It
// is never replanned on this pod.
func TestOwnerSilentPlanIs503RetryAfter5(t *testing.T) {
	local := 0
	inner := planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) {
		local++
		return samplePlan(), nil
	})
	down := &planForwarder{res: forward.Result{Status: forward.StatusUnavailable, Class: forward.ClassUnavailable}}
	sf := &stateForward{fw: down, user: uuid.New(), groupOf: func(string) (string, bool) { return "g", true }}
	c := plannerClient{Client: rivian.NewStub(), TripPlanner: &forwardPlanner{inner: inner, sf: sf}}

	rec := httptest.NewRecorder()
	handleTripPlan(c, nil, nil, uuid.New(), nil, nil, nil, nil)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planBody)))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("status %d Retry-After %q: %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if local != 0 {
		t.Errorf("the plan was retried on this pod %d times", local)
	}
	_, err := (&forwardPlanner{inner: inner, sf: sf}).PlanTrip(context.Background(), sampleInput())
	if got := plainPlanReason(err); got != "the planner is busy, try again in a moment" {
		t.Errorf("multi-day reason %q", got)
	}
}

// gatedRunner holds every call until released, so an owner can be kept at its per-user cap.
type gatedRunner struct {
	gate chan struct{}
	n    atomic.Int32
}

func (g *gatedRunner) Run(ctx context.Context, _ forward.Kind, _, _ string, _ any) (forward.Result, error) {
	g.n.Add(1)
	select {
	case <-g.gate:
	case <-ctx.Done():
	}
	return ServePlanLegs(ctx, plannerClient{Client: rivian.NewStub(), TripPlanner: planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) { return samplePlan(), nil })}, sampleInput()), nil
}

// The soft cases plan on this pod and are counted by why: an owner still busy after its one retry is `local_busy`, Redis down is `local`.
// Neither is the 503 path (only a timeout is).
func TestPlanLegsSoftCasesPlanLocally(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = rdb.Close() })
	signer, _ := forward.NewSigner(bytes.Repeat([]byte{7}, forward.KeyLen))
	m := forward.NewMetrics(prometheus.NewRegistry())
	keys := forward.Keys{}
	const group = "g"
	ownerView := &forwardtest.LeaseView{Owners: map[string]forward.LeaseOwner{group: {Found: true, Pod: "pod-a", Epoch: 3}}, HeldAt: map[string]int64{group: 3}}
	runner := &gatedRunner{gate: make(chan struct{})}
	o, err := forward.NewOwner(forward.OwnerConfig{Pod: "pod-a", Keys: keys, Signer: signer, Redis: rdb, Leases: ownerView, Runner: runner, Metrics: m, BlockFor: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); o.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for dl := time.Now().Add(2 * time.Second); !mr.Exists(keys.Ready("pod-a")) && time.Now().Before(dl); {
		time.Sleep(5 * time.Millisecond)
	}
	fw, err := forward.NewForwarder(forward.RequesterConfig{Pod: "pod-b", Keys: keys, Signer: signer, Redis: rdb, Metrics: m,
		Leases: &forwardtest.LeaseView{Owners: map[string]forward.LeaseOwner{group: {Found: true, Pod: "pod-a", Epoch: 3}}},
		Jitter: func() time.Duration { return 10 * time.Millisecond }})
	if err != nil {
		t.Fatal(err)
	}
	user := uuid.New()
	local := atomic.Int32{}
	inner := planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) {
		local.Add(1)
		return &rivian.TripPlan{Status: "LOCAL"}, nil
	})
	p := &forwardPlanner{inner: inner, sf: &stateForward{fw: fw, user: user, groupOf: func(string) (string, bool) { return group, true }}}
	count := func(outcome string) float64 {
		return testutil.ToFloat64(m.Total.WithLabelValues(string(forward.KindPlanLegs), outcome))
	}

	// Two legs fill the user's cap at the owner; the third is told busy twice and plans here.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = p.PlanTrip(context.Background(), sampleInput()) }()
	}
	for dl := time.Now().Add(3 * time.Second); runner.n.Load() < 2 && time.Now().Before(dl); {
		time.Sleep(5 * time.Millisecond)
	}
	plan, err := p.PlanTrip(context.Background(), sampleInput())
	if err != nil || plan.Status != "LOCAL" || local.Load() != 1 || count("local_busy") != 1 || count("timeout") != 0 {
		t.Errorf("busy twice: plan %+v err %v, %d local plans, local_busy=%v timeout=%v", plan, err, local.Load(), count("local_busy"), count("timeout"))
	}
	close(runner.gate)
	wg.Wait()

	// Redis down: the plan is made here and counted local.
	mr.Close()
	local.Store(0)
	plan, err = p.PlanTrip(context.Background(), sampleInput())
	if err != nil || plan.Status != "LOCAL" || local.Load() != 1 || count("local") != 1 {
		t.Errorf("redis down: plan %+v err %v, %d local plans, local=%v", plan, err, local.Load(), count("local"))
	}
}

// Every planner input the trip handlers can produce is one the forwarded schema accepts. The Nov 8 preview flip failed because it was not: the
// schema listed Rivian's old drive-mode enum and refused EVERYDAY and DISTANCE, the web's own defaults, so those plans were dropped by the owner
// in silence. This runs the real handlers over every drive mode (and unknown ones, which the handlers drop to the default) and checks each input
// they hand the planner against the owner's own admission test.
func TestForwardedSchemaAcceptsEveryInputTheHandlersProduce(t *testing.T) {
	signer, err := forward.NewSigner(bytes.Repeat([]byte{7}, forward.KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string, in rivian.PlanTripInput) {
		t.Helper()
		args, _ := json.Marshal(planLegsArgs(in))
		req := &forward.Request{V: forward.Version, ID: strings.Repeat("a", 32), User: uuid.NewString(), Group: "g", Epoch: 1, Kind: forward.KindPlanLegs, Args: args}
		signer.SignRequest(req)
		if _, err := forward.Check(signer, req); err != nil {
			t.Errorf("%s: the owner would refuse this input (%v): %+v", what, err, in)
		}
	}
	modes := append([]string{"", "CONSERVE", "ALL_PURPOSE", "bogus"}, settings.DriveModes...)
	for _, mode := range modes {
		var seen []rivian.PlanTripInput
		single := planFuncAPI(func(_ context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
			seen = append(seen, in)
			return samplePlan(), nil
		})
		body := `{"vehicle_id":"v1","starting_soc":80,"drive_mode":"` + mode + `","waypoints":[
		 {"latitude":34.0562,"longitude":-118.2427,"waypoint_type":"origin"},
		 {"latitude":36.0,"longitude":-119.0,"waypoint_type":"waypoint"},
		 {"latitude":32.7157,"longitude":-117.1611,"waypoint_type":"destination"}]}`
		rec := httptest.NewRecorder()
		handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: single}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
			httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(body)))
		if len(seen) != 1 {
			t.Fatalf("single plan, mode %q: %d planner calls (status %d: %s)", mode, len(seen), rec.Code, rec.Body)
		}
		check("single plan, mode "+mode, seen[0])

		days := &daysPlanner{sim: true}
		_, _ = runMultiday(t, days, `{`+mdVehicle+`"drive_mode":"`+mode+`","start_at":"2026-10-20T08:00:00-07:00","tz":"America/Los_Angeles",
		  "origin":{"lat":34.16,"lon":-118.17,"label":"Home"},
		  "days":[{"end":{"lat":36.0,"lon":-119.96,"label":"Kettleman"},"overnight":{"l2_kw":3.3,"until":"08:00","max_soc_pct":80}},
		   {"vias":[{"lat":36.5,"lon":-120.5,"label":"Via"}],"end":{"lat":37.79,"lon":-122.39,"label":"SF"}}]}`)
		if len(days.calls) == 0 {
			t.Fatalf("multi-day, mode %q: no planner call", mode)
		}
		for i, in := range days.calls {
			check(fmt.Sprintf("multi-day leg %d, mode %s", i+1, mode), in)
		}
	}
}

// The schema's drive modes are the settings' list: neither can grow without the other.
func TestForwardedDriveModesMatchTheHandlers(t *testing.T) {
	for _, m := range settings.DriveModes {
		if !forward.ValidDriveMode(m) {
			t.Errorf("settings drive mode %q is refused by the forwarded schema", m)
		}
	}
	for _, m := range []string{"CONSERVE", "ALL_PURPOSE", "bogus", "everyday"} {
		if forward.ValidDriveMode(m) {
			t.Errorf("%q is accepted by the forwarded schema but the handlers drop it", m)
		}
	}
}
