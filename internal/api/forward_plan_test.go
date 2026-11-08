package api

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/planleg"
	"github.com/apohor/rivolt/internal/rivian"
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
