package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

// planFailureClass marks an owner's reply that carries a planFailure body (a planner error the requester rebuilds).
const planFailureClass = "plan_error"

// forwardPlanner sends a planner call that missed the leg cache and passed the quota to the vehicle's group owner. It sits innermost in
// withLegCache (inside the cache, the quota and the timing wrapper), so a cache hit, a quota refusal and a Pro refusal never reach it and
// make zero forwards. fw/groupOf/user come from the request; where the pod holds the group, the group is unknown, or the forward falls
// soft, inner (today's planner) runs.
type forwardPlanner struct {
	inner rivian.TripPlanner
	sf    *stateForward // the same requester wiring the state handler uses
}

func (p *forwardPlanner) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	sf := p.sf
	if sf == nil || sf.fw == nil || sf.groupOf == nil {
		return p.inner.PlanTrip(ctx, in)
	}
	group, ok := sf.groupOf(in.VehicleID)
	if !ok {
		if !sf.fw.NoGroup(forward.KindPlanLegs) {
			return decodeForwardedPlan(forward.UnavailableResult())
		}
		return p.inner.PlanTrip(ctx, in)
	}
	var (
		plan *rivian.TripPlan
		lerr error
		ran  bool
	)
	res, _, err := sf.fw.Do(ctx, forward.Call{Kind: forward.KindPlanLegs, User: sf.user.String(), Group: group, Args: planLegsArgs(in)},
		func(ctx context.Context) (forward.Result, error) {
			ran = true
			plan, lerr = p.inner.PlanTrip(ctx, in)
			return forward.Result{Status: http.StatusOK}, nil
		})
	if ran {
		return plan, lerr
	}
	if err != nil {
		return nil, err
	}
	return decodeForwardedPlan(res)
}

// planFailure is a planner error on the wire: enough to rebuild an error that behaves the same under errors.Is/As in the handlers.
type planFailure struct {
	Kind    string `json:"kind"` // upstream | rate_limit | invalid_trip | no_planner | no_fixture | needs_reauth | not_authenticated | other
	Msg     string `json:"msg"`
	Class   string `json:"class,omitempty"`
	Op      string `json:"op,omitempty"`
	HTTP    int    `json:"http,omitempty"`
	Ext     string `json:"ext,omitempty"`
	Reason  string `json:"reason,omitempty"`
	RetryMS int64  `json:"retry_ms,omitempty"`
}

var errorClasses = map[string]rivian.ErrorClass{
	rivian.ClassTransient.String(): rivian.ClassTransient, rivian.ClassOutage.String(): rivian.ClassOutage,
	rivian.ClassRateLimited.String(): rivian.ClassRateLimited, rivian.ClassUserAction.String(): rivian.ClassUserAction,
}

func planFailureOf(err error) planFailure {
	f := planFailure{Kind: "other", Msg: err.Error()}
	var ue *rivian.UpstreamError
	var rl *rivian.ErrRateLimited
	switch {
	case errors.As(err, &rl):
		f.Kind, f.RetryMS = "rate_limit", rl.RetryAfter.Milliseconds()
	case errors.Is(err, rivian.ErrInvalidTrip):
		f.Kind = "invalid_trip"
	case errors.Is(err, rivian.ErrNoPlanner):
		f.Kind = "no_planner"
	case errors.Is(err, rivian.ErrNoFixture):
		f.Kind = "no_fixture"
	case errors.Is(err, rivian.ErrNeedsReauth):
		f.Kind = "needs_reauth"
	case errors.Is(err, rivian.ErrNotAuthenticated):
		f.Kind = "not_authenticated"
	case errors.As(err, &ue):
		f.Kind, f.Class, f.Op, f.HTTP, f.Ext, f.Reason = "upstream", ue.Class.String(), ue.Op, ue.HTTPStatus, ue.ExtCode, ue.Reason
	}
	return f
}

// errOwnerUnavailable marks a forward that came back with no answer and no local run: the owner timed out (after the one retry on a lease
// change), the lease moved with nobody new to ask, its queue was full, or the message was refused. The handlers answer it 503 with
// Retry-After 5 and, in a multi-day plan, "the planner is busy". It is never retried on this pod, because the owner is probably still
// inside a slow Rivian call and a local retry would double the load (DA ruling on #1687).
//
// Everything else that goes wrong before an answer is a local plan, not this error: the owner busy past its one retry (local_busy), Redis
// down, the owner not ready, no owner or this pod being the owner (local), and an unknown group (no_group).
var errOwnerUnavailable = errors.New("the vehicle's owner did not answer")

// forwardedPlanError is a planner error that crossed the wire: its text is the owner's, and it matches the same sentinels and types.
type forwardedPlanError struct {
	msg  string
	errs []error
}

func (e *forwardedPlanError) Error() string   { return e.msg }
func (e *forwardedPlanError) Unwrap() []error { return e.errs }

func (f planFailure) err() error {
	e := &forwardedPlanError{msg: f.Msg}
	switch f.Kind {
	case "upstream":
		e.errs = []error{&rivian.UpstreamError{Class: errorClasses[f.Class], Op: f.Op, HTTPStatus: f.HTTP, ExtCode: f.Ext, Reason: f.Reason}}
	case "rate_limit":
		e.errs = []error{&rivian.ErrRateLimited{RetryAfter: time.Duration(f.RetryMS) * time.Millisecond}}
	case "invalid_trip":
		e.errs = []error{rivian.ErrInvalidTrip}
	case "no_planner":
		e.errs = []error{rivian.ErrNoPlanner}
	case "no_fixture":
		e.errs = []error{rivian.ErrNoFixture}
	case "needs_reauth":
		e.errs = []error{rivian.ErrNeedsReauth}
	case "not_authenticated":
		e.errs = []error{rivian.ErrNotAuthenticated}
	}
	return e
}

func decodeForwardedPlan(res forward.Result) (*rivian.TripPlan, error) {
	if res.Status == http.StatusOK {
		var plan rivian.TripPlan
		if err := json.Unmarshal(res.Body, &plan); err != nil {
			return nil, &rivian.UpstreamError{Class: rivian.ClassTransient, Op: "PlanTrip", Reason: "bad reply from the vehicle's owner"}
		}
		return &plan, nil
	}
	if res.Class == planFailureClass {
		var f planFailure
		if json.Unmarshal(res.Body, &f) == nil && f.Kind != "" {
			return nil, f.err()
		}
	}
	// The owner could not be reached or would not answer: an outage the client can retry, not a verdict on the trip.
	return nil, &forwardedPlanError{msg: errOwnerUnavailable.Error(), errs: []error{
		&rivian.UpstreamError{Class: rivian.ClassOutage, Op: "PlanTrip", Reason: "the vehicle's owner did not answer"}, errOwnerUnavailable}}
}

// ServePlanLegs is the owner's side of a forwarded plan_legs: one planner call against the user's own client, answered as a Result. The
// leg cache and the quota are the requester's and are not applied here.
func ServePlanLegs(ctx context.Context, c rivian.Client, in rivian.PlanTripInput) forward.Result {
	var tp rivian.TripPlanner
	if c != nil {
		tp, _ = tripPlannerOf(c)
	}
	if tp == nil {
		return planFailureResult(rivian.ErrNoPlanner)
	}
	plan, err := tp.PlanTrip(ctx, in)
	if err != nil {
		return planFailureResult(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return forward.Result{Status: http.StatusBadGateway, Class: forward.ClassInternal}
	}
	return forward.Result{Status: http.StatusOK, Body: raw}
}

func planFailureResult(err error) forward.Result {
	raw, _ := json.Marshal(planFailureOf(err))
	return forward.Result{Status: http.StatusBadGateway, Class: planFailureClass, Body: raw}
}

// PlanTripInputOf rebuilds the planner input from forwarded args (the inverse of planLegsArgs).
func PlanTripInputOf(a *forward.PlanLegsArgs) rivian.PlanTripInput {
	in := rivian.PlanTripInput{
		VehicleID: a.VehicleID, StartingSoC: a.StartingSoC, StartingRangeMeters: a.StartingRangeMeters, OriginBearing: a.OriginBearing,
		TargetArrivalSocPercent: a.TargetArrivalSocPercent, DriveMode: a.DriveMode, HasAdapter: a.HasAdapter,
	}
	for _, w := range a.Waypoints {
		in.Waypoints = append(in.Waypoints, rivian.PlanTripWaypoint{Latitude: w.Latitude, Longitude: w.Longitude, WaypointType: w.WaypointType, EntityID: w.EntityID})
	}
	for _, n := range a.NetworkPreferences {
		in.NetworkPreferences = append(in.NetworkPreferences, rivian.NetworkPreference{NetworkID: n.NetworkID, Preference: n.Preference})
	}
	if rp := a.RoutePreferences; rp != nil {
		in.RoutePreferences = &rivian.RoutePreferences{AvoidTolls: rp.AvoidTolls, AvoidFerries: rp.AvoidFerries, AvoidHighways: rp.AvoidHighways}
	}
	if a.DepartureTimeMS != 0 {
		in.DepartureTime = time.UnixMilli(a.DepartureTimeMS)
	}
	return in
}

func planLegsArgs(in rivian.PlanTripInput) forward.PlanLegsArgs {
	a := forward.PlanLegsArgs{
		VehicleID: in.VehicleID, StartingSoC: in.StartingSoC, StartingRangeMeters: in.StartingRangeMeters, OriginBearing: in.OriginBearing,
		TargetArrivalSocPercent: in.TargetArrivalSocPercent, DriveMode: in.DriveMode, HasAdapter: in.HasAdapter,
		Waypoints: []forward.Waypoint{},
	}
	for _, w := range in.Waypoints {
		a.Waypoints = append(a.Waypoints, forward.Waypoint{Latitude: w.Latitude, Longitude: w.Longitude, WaypointType: w.WaypointType, EntityID: w.EntityID})
	}
	for _, n := range in.NetworkPreferences {
		a.NetworkPreferences = append(a.NetworkPreferences, forward.NetworkPref{NetworkID: n.NetworkID, Preference: n.Preference})
	}
	if rp := in.RoutePreferences; rp != nil {
		a.RoutePreferences = &forward.RoutePrefs{AvoidTolls: rp.AvoidTolls, AvoidFerries: rp.AvoidFerries, AvoidHighways: rp.AvoidHighways}
	}
	if !in.DepartureTime.IsZero() {
		a.DepartureTimeMS = in.DepartureTime.UnixMilli()
	}
	return a
}
