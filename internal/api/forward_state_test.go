package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/forward/forwardtest"
	"github.com/apohor/rivolt/internal/planleg"
	"github.com/apohor/rivolt/internal/rivian"
)

const (
	fwdVehicle = "v-fake-1"
	fwdGroup   = "grp-1"
)

// fakeStateForwarder answers Do with a canned result, or runs local when run is set (this pod holds the group).
type fakeStateForwarder struct {
	run     bool
	res     forward.Result
	calls   []forward.Call
	noGroup []forward.Kind
}

func (f *fakeStateForwarder) NoGroup(k forward.Kind) { f.noGroup = append(f.noGroup, k) }

func (f *fakeStateForwarder) Do(ctx context.Context, c forward.Call, local func(context.Context) (forward.Result, error)) (forward.Result, string, error) {
	f.calls = append(f.calls, c)
	if f.run {
		r, err := local(ctx)
		return r, "local", err
	}
	return f.res, "ok", nil
}

func stateGet(h http.HandlerFunc) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/state/"+fwdVehicle, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("vehicleID", fwdVehicle)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func (c *restStateClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newStateForward(fw StateForwarder) *stateForward {
	return &stateForward{fw: fw, user: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		groupOf: func(string) (string, bool) { return fwdGroup, true }, forwardClient: anyClient}
}

func anyClient(any) bool { return true }

func TestForwardedStateServedByOwner(t *testing.T) {
	client := &restStateClient{}
	body, _ := json.Marshal(rivian.State{VehicleID: fwdVehicle, PowerState: "ready"})
	fw := &fakeStateForwarder{res: forward.Result{Status: 200, Body: body}}
	w := stateGet(handleVehicleStateForwarded(client, rivian.NewStateMonitor(nil, nil), nil, newStateForward(fw)))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if client.count() != 0 {
		t.Errorf("the requester called Rivian %d times, want 0", client.count())
	}
	if len(fw.calls) != 1 || fw.calls[0].Kind != forward.KindFreshState || fw.calls[0].Group != fwdGroup {
		t.Fatalf("forward calls: %+v", fw.calls)
	}
	if a, _ := fw.calls[0].Args.(forward.FreshStateArgs); a.VehicleID != fwdVehicle || a.MaxAgeMS != freshStateMaxAge.Milliseconds() {
		t.Errorf("args: %+v", fw.calls[0].Args)
	}
}

func TestForwardedStateHeldRunsLocally(t *testing.T) {
	client := &restStateClient{st: rivian.State{VehicleID: fwdVehicle}}
	fw := &fakeStateForwarder{run: true}
	if w := stateGet(handleVehicleStateForwarded(client, rivian.NewStateMonitor(nil, nil), nil, newStateForward(fw))); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if client.count() != 1 {
		t.Errorf("held: %d Rivian calls, want 1", client.count())
	}
}

func TestForwardedStateNoRouteKeepsDirectPath(t *testing.T) {
	fw := &fakeStateForwarder{}
	for name, tc := range map[string]struct {
		sf      *stateForward
		noGroup int
	}{
		"nil":      {nil, 0},
		"no group": {&stateForward{fw: fw, groupOf: func(string) (string, bool) { return "", false }, forwardClient: anyClient}, 1},
		"no fw":    {&stateForward{groupOf: func(string) (string, bool) { return fwdGroup, true }, forwardClient: anyClient}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			fw.noGroup = nil
			client := &restStateClient{st: rivian.State{VehicleID: fwdVehicle}}
			if w := stateGet(handleVehicleStateForwarded(client, rivian.NewStateMonitor(nil, nil), nil, tc.sf)); w.Code != 200 {
				t.Fatalf("status %d", w.Code)
			}
			if client.count() != 1 {
				t.Errorf("%d Rivian calls, want 1", client.count())
			}
			if len(fw.noGroup) != tc.noGroup || (tc.noGroup == 1 && fw.noGroup[0] != forward.KindFreshState) {
				t.Errorf("no_group counted %v, want %d for fresh_state", fw.noGroup, tc.noGroup)
			}
			if len(fw.calls) != 0 {
				t.Errorf("an unrouted call reached the forwarder")
			}
		})
	}
}

func TestForwardedStateErrorsPassThrough(t *testing.T) {
	notConnected := json.RawMessage(`{"error":"Rivian is not connected to this account","class":"user_action","reason":"not_connected"}`)
	for _, tc := range []struct {
		name       string
		res        forward.Result
		status     int
		retryAfter string
	}{
		{"not connected", forward.Result{Status: 409, Class: "user_action", Body: notConnected}, 409, ""},
		{"rate limited", forward.Result{Status: 429, Class: "upstream", Body: json.RawMessage(`{"error":"slow down"}`)}, 429, "30"},
		{"unavailable", forward.Result{Status: forward.StatusUnavailable, Class: forward.ClassUnavailable}, 503, "5"},
		{"not implemented", forward.Result{Status: 404, Class: classNotImplemented, Body: json.RawMessage(`"no state"`)}, 404, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &restStateClient{}
			fw := &fakeStateForwarder{res: tc.res}
			w := stateGet(handleVehicleStateForwarded(client, rivian.NewStateMonitor(nil, nil), nil, newStateForward(fw)))
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if got := w.Header().Get("Retry-After"); tc.retryAfter != "" && got == "" {
				t.Errorf("no Retry-After, want one (%s)", tc.retryAfter)
			}
			if tc.name == "not connected" && !bytes.Contains(w.Body.Bytes(), []byte(`"not_connected"`)) {
				t.Errorf("body lost the reason: %s", w.Body)
			}
			if client.count() != 0 {
				t.Errorf("the requester called Rivian after a forwarded failure")
			}
		})
	}
}

func TestServeFreshStateCacheAndFetch(t *testing.T) {
	ctx := context.Background()
	client := &restStateClient{st: rivian.State{VehicleID: fwdVehicle, PowerState: "ready"}}
	mon := rivian.NewStateMonitor(nil, nil)

	// Nothing cached: one fetch, and the cache is primed, so the next call within max age costs Rivian nothing.
	if r := ServeFreshState(ctx, client, mon, nil, fwdVehicle, 20*time.Second); r.Status != 200 {
		t.Fatalf("first: %d %s", r.Status, r.Body)
	}
	if r := ServeFreshState(ctx, client, mon, nil, fwdVehicle, 20*time.Second); r.Status != 200 {
		t.Fatalf("second: %d", r.Status)
	}
	if client.count() != 1 {
		t.Errorf("%d Rivian calls for two forwarded fetches, want 1 (the second is the owner's fresh cache)", client.count())
	}
	// max age 0 never uses the cache.
	ServeFreshState(ctx, client, mon, nil, fwdVehicle, 0)
	if client.count() != 2 {
		t.Errorf("max age 0 made %d calls in total, want 2", client.count())
	}
}

func TestServeFreshStateErrorsMatchTheHandler(t *testing.T) {
	ctx := context.Background()
	unlinked := func(context.Context) (bool, error) { return false, nil }

	r := ServeFreshState(ctx, &restStateClient{err: rivian.ErrNotAuthenticated}, nil, unlinked, fwdVehicle, 0)
	if r.Status != 409 || r.Class != rivian.ClassUserAction.String() || !bytes.Contains(r.Body, []byte("not_connected")) {
		t.Errorf("unlinked account: %d %q %s", r.Status, r.Class, r.Body)
	}
	r = ServeFreshState(ctx, &restStateClient{err: rivian.ErrNotAuthenticated}, nil, func(context.Context) (bool, error) { return true, nil }, fwdVehicle, 0)
	if r.Status == 409 {
		t.Errorf("a linked account must not be told to connect: %d", r.Status)
	}
	r = ServeFreshState(ctx, &restStateClient{err: rivian.ErrNotImplemented}, nil, nil, fwdVehicle, 0)
	if r.Status != 404 || r.Class != classNotImplemented {
		t.Errorf("not implemented: %d %q", r.Status, r.Class)
	}
	r = ServeFreshState(ctx, nil, nil, nil, fwdVehicle, 0)
	if r.Status != 404 {
		t.Errorf("no client: %d", r.Status)
	}
	// Whatever the generic error, status and body are what stateErrorResponse says.
	e := errors.New("boom")
	r = ServeFreshState(ctx, &restStateClient{err: e}, nil, nil, fwdVehicle, 0)
	want, _ := stateErrorResponse(ctx, e, nil)
	if r.Status != want {
		t.Errorf("status %d, handler says %d", r.Status, want)
	}
}

// ownerRunner serves fresh_state exactly as the binary's runner does, over a recording client.
type ownerRunner struct {
	client rivian.Client
	mon    *rivian.StateMonitor
}

func (o *ownerRunner) Run(ctx context.Context, kind forward.Kind, _, _ string, args any) (forward.Result, error) {
	a := args.(*forward.FreshStateArgs)
	return ServeFreshState(ctx, o.client, o.mon, nil, a.VehicleID, time.Duration(a.MaxAgeMS)*time.Millisecond), nil
}

// The whole path over Redis: a request pod that does not hold the group serves /api/state through the owner, and Rivian is called
// exactly once, by the owner.
func TestForwardedStateEndToEnd(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = rdb.Close() })
	signer, _ := forward.NewSigner(bytes.Repeat([]byte{7}, forward.KeyLen))
	keys := forward.Keys{}
	m := forward.NewMetrics(prometheus.NewRegistry())

	ownerClient := &restStateClient{st: rivian.State{VehicleID: fwdVehicle, PowerState: "ready", BatteryLevelPct: 55}}
	ownerLeases := &forwardtest.LeaseView{
		Owners: map[string]forward.LeaseOwner{fwdGroup: {Found: true, Pod: "pod-a", Epoch: 3}},
		HeldAt: map[string]int64{fwdGroup: 3},
	}
	o, err := forward.NewOwner(forward.OwnerConfig{Pod: "pod-a", Keys: keys, Signer: signer, Redis: rdb, Leases: ownerLeases,
		Runner: &ownerRunner{client: ownerClient, mon: rivian.NewStateMonitor(nil, nil)}, Metrics: m, BlockFor: time.Second})
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
		Leases: &forwardtest.LeaseView{Owners: map[string]forward.LeaseOwner{fwdGroup: {Found: true, Pod: "pod-a", Epoch: 3}}}})
	if err != nil {
		t.Fatal(err)
	}
	reqClient := &restStateClient{}
	w := stateGet(handleVehicleStateForwarded(reqClient, rivian.NewStateMonitor(nil, nil), nil, newStateForward(fw)))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var st rivian.State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.BatteryLevelPct != 55 {
		t.Errorf("state %+v (%v)", st, err)
	}
	if reqClient.count() != 0 || ownerClient.count() != 1 {
		t.Errorf("Rivian calls: requester %d, owner %d; want 0 and 1", reqClient.count(), ownerClient.count())
	}
}

// The client-type check is the only thing the replay test switch changes: the live client is forwarded, the replay client only with the
// switch, and every other client (stub, mock, fixture, fakes) never is.
func TestForwardableClients(t *testing.T) {
	replay := &rivian.ReplayClient{}
	var nilLive *rivian.LiveClient
	for name, tc := range map[string]struct {
		sf   *stateForward
		c    any
		want bool
	}{
		"live":                 {&stateForward{}, rivian.NewLive(), true},
		"nil live":             {&stateForward{}, nilLive, false},
		"replay, no switch":    {&stateForward{}, replay, false},
		"replay, switch":       {&stateForward{replayTest: true}, replay, true},
		"live, switch":         {&stateForward{replayTest: true}, rivian.NewLive(), true},
		"stub, switch":         {&stateForward{replayTest: true}, rivian.NewStub(), false},
		"mock, switch":         {&stateForward{replayTest: true}, rivian.NewMock(), false},
		"nil requester":        {nil, replay, false},
		"deps wiring, switch":  {Deps{StateForward: &fakeStateForwarder{}, GroupOfVehicle: func(string) (string, bool) { return "g", true }, ForwardReplayTest: true}.stateForward(uuid.New()), replay, true},
		"deps wiring, no flag": {Deps{StateForward: &fakeStateForwarder{}, GroupOfVehicle: func(string) (string, bool) { return "g", true }}.stateForward(uuid.New()), replay, false},
	} {
		if got := tc.sf.forwardable(tc.c); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// With the switch, a replay client's planner leg is forwarded (the preview evidence path); without it, it is planned here.
func TestReplayPlannerForwardsOnlyWithTheSwitch(t *testing.T) {
	for _, on := range []bool{false, true} {
		fw := &planForwarder{res: ServePlanLegs(context.Background(), plannerClient{Client: rivian.NewStub(), TripPlanner: planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) { return samplePlan(), nil })}, sampleInput())}
		mr := miniredis.RunT(t)
		d := Deps{PlanLegs: planleg.New(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "t", nil), StateForward: fw,
			GroupOfVehicle: func(string) (string, bool) { return "g", true }, ForwardReplayTest: on}
		rc := &rivian.ReplayClient{}
		planner, ok := tripPlannerOf(withLegCache(d, uuid.New(), rc))
		if !ok {
			t.Fatal("no planner")
		}
		_, _ = planner.PlanTrip(context.Background(), sampleInput()) // the replay planner has no feed here: its own answer is not under test
		if want := map[bool]int{false: 0, true: 1}[on]; fw.count() != want {
			t.Errorf("switch %v: %d forwards, want %d", on, fw.count(), want)
		}
	}
}
