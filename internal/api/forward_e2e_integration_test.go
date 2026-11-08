//go:build integration && e2e_forward

package api

// The multi-pod forward e2e harness (single-owner M3). Two pods run in one process, each with what a real pod has: its own lease store on one
// real Postgres, its own forwarder and owner loop on one Redis, its own LiveClient, and the real GET /api/state handler. The Rivian gateway
// is a fake on loopback that counts the calls it receives, so "exactly one Rivian call" is counted where Rivian would count it, not at a
// fake client. Only fake users, vehicles and tokens minted here are used; nothing reaches Rivian (the clients' endpoint is the fake's URL).
//
// What this does not cover: the router and auth middleware in front of the handler, and cmd/rivolt's forwardRunner glue (a six-line twin
// below stands in for it; cmd/rivolt/forward_test.go covers the real one).
//
// Run: go test -tags=integration,e2e_forward ./internal/api -run ForwardE2E

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

const e2eVehicle = "e2e-veh-1"

// fakeGateway is a Rivian GraphQL gateway that answers GetVehicleState and counts what it is asked.
type fakeGateway struct {
	srv *httptest.Server
	mu  sync.Mutex
	ops map[string]int
	// hold, when set, makes every GetVehicleState wait for it to be closed (or for the request to end): a call in flight at a pod that dies.
	hold chan struct{}
}

func (g *fakeGateway) holdState() (release func()) {
	g.mu.Lock()
	g.hold = make(chan struct{})
	ch := g.hold
	g.mu.Unlock()
	return func() { close(ch) }
}

func newFakeGateway(t *testing.T) *fakeGateway {
	g := &fakeGateway{ops: map[string]int{}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			OperationName string `json:"operationName"`
		}
		_ = json.Unmarshal(b, &req)
		g.mu.Lock()
		g.ops[req.OperationName]++
		hold := g.hold
		g.mu.Unlock()
		if hold != nil && req.OperationName == "GetVehicleState" {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if req.OperationName == "planTripWithMultiStopV2" {
			_, _ = io.WriteString(w, `{"data":{"planTrip2":{"status":"OK","plans":[{"summary":{"destinationReachable":true,"totalTripDurationSeconds":3600,"totalDriveDistanceMeters":123456,"arrivalSOCPercent":21.5},"waypoints":[{"waypointType":"origin","latitude":34.1,"longitude":-118.2},{"waypointType":"destination","latitude":35.1,"longitude":-117.2,"arrivalTimeUTC":"2026-11-08T10:00:00Z"}]}]}}}`)
			return
		}
		if req.OperationName == "GetVehicleState" {
			_, _ = io.WriteString(w, `{"data":{"vehicleState":{"batteryLevel":{"timeStamp":"2026-11-08T00:00:00.000Z","value":55}}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGateway) count(op string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ops[op]
}

// e2ePod is one pod: lease store, forwarder, owner loop, Rivian client and monitor.
type e2ePod struct {
	name    string
	store   *leases.Store
	gw      *fakeGateway
	client  *rivian.LiveClient
	mon     *rivian.StateMonitor
	fwd     *forward.Forwarder
	metrics *forward.Metrics
	reg     *prometheus.Registry
	stop    context.CancelFunc
	user    uuid.UUID
	view    *forward.PGLeaseView

	// stall, when set, makes the owner side wait on it before running a kind, ignoring the request deadline: a pod that has hung or died with
	// the request in hand (a dead process sends no reply). entered counts the kinds that reached it.
	stall   chan struct{}
	entered atomic.Int32
}

type e2eRunner struct{ p *e2ePod }

// Run is the owner side of fresh_state and plan_legs, as cmd/rivolt's forwardRunner does it.
func (r e2eRunner) Run(ctx context.Context, kind forward.Kind, user, group string, args any) (forward.Result, error) {
	r.p.entered.Add(1)
	if r.p.stall != nil {
		<-r.p.stall
	}
	if pa, ok := args.(*forward.PlanLegsArgs); ok && kind == forward.KindPlanLegs {
		if g, known := r.p.store.GroupOfVehicle(pa.VehicleID); !known || g != group {
			return forward.Result{Status: 404, Class: "not_found", Body: json.RawMessage(`{}`)}, nil
		}
		return ServePlanLegs(ctx, r.p.client, PlanTripInputOf(pa)), nil
	}
	a, ok := args.(*forward.FreshStateArgs)
	if kind != forward.KindFreshState || !ok {
		return forward.Result{Status: 501, Class: "not_implemented"}, nil
	}
	if g, known := r.p.store.GroupOfVehicle(a.VehicleID); !known || g != group {
		return forward.Result{Status: 404, Class: "not_found", Body: json.RawMessage(`{}`)}, nil
	}
	has := func(context.Context) (bool, error) { return true, nil }
	return ServeFreshState(ctx, r.p.client, r.p.mon, has, a.VehicleID, time.Duration(a.MaxAgeMS)*time.Millisecond), nil
}

type e2eEnv struct {
	pool  *sql.DB
	uid   uuid.UUID
	rdb   *redis.Client
	mr    *miniredis.Miniredis
	key   []byte
	group string
	a, b  *e2ePod

	timeouts map[forward.Kind]time.Duration
}

// e2eOpts tunes an environment: Timeouts shortens a kind's wait so a silent owner is a quick test.
type e2eOpts struct {
	Timeouts map[forward.Kind]time.Duration
}

func newE2EEnv(t *testing.T, opts ...e2eOpts) *e2eEnv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "e2e-fwd-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,$2)`, uid, e2eVehicle); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id) VALUES ($1,'rivian.session','\x00'::bytea,'k')`, uid); err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	env := &e2eEnv{uid: uid, pool: pool, rdb: rdb, mr: mr, key: []byte(strings.Repeat("k", 32))}
	if len(opts) > 0 {
		env.timeouts = opts[0].Timeouts
	}
	env.a = env.pod(t, "pod-a", uid)
	env.b = env.pod(t, "pod-b", uid)
	// Pod A takes the group; both pods then know the vehicle's group, as the coordinator's pass leaves them.
	if _, err := env.a.store.LeadGroups(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT group_id FROM account_leases WHERE pod_id = 'pod-a'`).Scan(&env.group); err != nil {
		t.Fatalf("pod-a holds no group: %v", err)
	}
	for _, p := range []*e2ePod{env.a, env.b} {
		p.store.SetVehicleGroups(map[string]string{e2eVehicle: env.group})
	}
	env.waitReady(t, env.a)
	return env
}

func (e *e2eEnv) pod(t *testing.T, name string, uid uuid.UUID) *e2ePod {
	t.Helper()
	store, err := leases.NewStore(e.pool, name)
	if err != nil {
		t.Fatal(err)
	}
	store.SetGroupLead(true)
	signer, err := forward.NewSigner(e.key)
	if err != nil {
		t.Fatal(err)
	}
	gw := newFakeGateway(t)
	cl := rivian.NewLive().WithEndpoint(gw.srv.URL)
	cl.Restore(rivian.Session{UserSessionToken: "fake-user-session", CSRFToken: "fake-csrf", AppSessionToken: "fake-app", AuthenticatedAt: time.Now()})
	reg := prometheus.NewRegistry()
	p := &e2ePod{name: name, store: store, gw: gw, client: cl, mon: rivian.NewStateMonitor(nil, nil), reg: reg, user: uid, metrics: forward.NewMetrics(reg)}
	view := forward.NewPGLeaseView(e.pool, store)
	p.view = view
	keys := forward.Keys{Prefix: "e2e"}
	owner, err := forward.NewOwner(forward.OwnerConfig{Pod: name, Keys: keys, Signer: signer, Redis: e.rdb, Leases: view,
		Runner: e2eRunner{p}, Metrics: p.metrics, BlockFor: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	p.fwd, err = forward.NewForwarder(forward.RequesterConfig{Pod: name, Keys: keys, Signer: signer, Redis: e.rdb, Leases: view, Metrics: p.metrics, Timeouts: e.timeouts})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	p.stop = stop
	done := make(chan struct{})
	go func() { owner.Run(ctx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	return p
}

// waitReady waits for the pod's readiness marker, which requesters look for before forwarding to it.
func (e *e2eEnv) waitReady(t *testing.T, p *e2ePod) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if n, _ := e.rdb.Exists(context.Background(), forward.Keys{Prefix: "e2e"}.Ready(p.name)).Result(); n == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never became ready", p.name)
}

// getState is GET /api/state/{vehicle} on pod p, through the real handler.
func (p *e2ePod) getState(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	sf := &stateForward{fw: p.fwd, groupOf: p.store.GroupOfVehicle, user: p.user}
	h := handleVehicleStateForwarded(p.client, p.mon, func(context.Context) (bool, error) { return true, nil }, sf)
	r := httptest.NewRequest(http.MethodGet, "/api/state/"+e2eVehicle, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("vehicleID", e2eVehicle)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func (p *e2ePod) forwardTotal(kind forward.Kind, outcome string) float64 {
	return testutil.ToFloat64(p.metrics.Total.WithLabelValues(string(kind), outcome))
}

// (a) fresh_state: the pod that does not hold the group answers a state read through the owner, and Rivian is asked exactly once, by the owner.
func TestForwardE2EFreshState(t *testing.T) {
	env := newE2EEnv(t)
	w := env.b.getState(t)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var st rivian.State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.BatteryLevelPct != 55 {
		t.Fatalf("body %s (%v)", w.Body, err)
	}
	if got := env.b.gw.count("GetVehicleState"); got != 0 {
		t.Errorf("the requesting pod called Rivian %d times, want 0", got)
	}
	if got := env.a.gw.count("GetVehicleState"); got != 1 {
		t.Errorf("the owner called Rivian %d times, want exactly 1", got)
	}
	if got := env.b.forwardTotal(forward.KindFreshState, "ok"); got != 1 {
		t.Errorf("requester ok count %v, want 1", got)
	}
	// A second read inside the owner's freshness window is answered from its cache: still one Rivian call in all.
	if w := env.b.getState(t); w.Code != 200 {
		t.Fatalf("second read: %d", w.Code)
	}
	if got := env.a.gw.count("GetVehicleState") + env.b.gw.count("GetVehicleState"); got != 1 {
		t.Errorf("two reads cost %d Rivian calls, want 1", got)
	}
}

// (e) signatures: nothing that fails verification reaches Rivian, and no reply is sent to the forger.
func TestForwardE2ESignatures(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()
	var epoch int64
	if err := env.pool.QueryRowContext(ctx, `SELECT epoch FROM account_leases WHERE group_id=$1`, env.group).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	nowMS := time.Now().UnixMilli()
	args, _ := json.Marshal(forward.FreshStateArgs{VehicleID: e2eVehicle, MaxAgeMS: 1})
	mk := func() *forward.Request {
		id, _ := forward.NewID()
		return &forward.Request{V: forward.Version, ID: id, User: env.a.user.String(), Group: env.group, Epoch: epoch,
			Kind: forward.KindFreshState, Args: args, ExpiresMS: nowMS + 60_000}
	}
	good, _ := forward.NewSigner(env.key)
	wrong, _ := forward.NewSigner([]byte(strings.Repeat("x", 32)))
	cases := map[string]func() *forward.Request{
		"wrong key": func() *forward.Request { r := mk(); wrong.SignRequest(r); return r },
		"forged user": func() *forward.Request {
			r := mk()
			good.SignRequest(r)
			r.User = uuid.NewString()
			return r
		},
		"forged group": func() *forward.Request {
			r := mk()
			good.SignRequest(r)
			r.Group = "someone-elses-group"
			return r
		},
		"forged epoch": func() *forward.Request {
			r := mk()
			good.SignRequest(r)
			r.Epoch++
			return r
		},
		"forged args": func() *forward.Request {
			r := mk()
			good.SignRequest(r)
			r.Args = json.RawMessage(`{"vehicle_id":"another-vehicle","max_age_ms":1}`)
			return r
		},
		"unsigned": func() *forward.Request { return mk() },
	}
	want := 0.0
	for name, build := range cases {
		r := build()
		raw, _ := json.Marshal(r)
		if err := env.rdb.RPush(ctx, forward.Keys{Prefix: "e2e"}.Queue(env.a.name), raw).Err(); err != nil {
			t.Fatal(err)
		}
		want++
		deadline := time.Now().Add(5 * time.Second)
		for env.a.forwardTotal(forward.KindFreshState, "rejected") < want && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if got := env.a.forwardTotal(forward.KindFreshState, "rejected"); got < want {
			t.Errorf("%s: rejected count %v, want %v (the owner did not refuse it)", name, got, want)
		}
		if n, _ := env.rdb.Exists(ctx, forward.Keys{Prefix: "e2e"}.Reply(r.ID)).Result(); n != 0 {
			t.Errorf("%s: the owner answered a request it refused", name)
		}
	}
	if got := env.a.gw.count("GetVehicleState"); got != 0 {
		t.Errorf("forged requests cost %d Rivian calls, want 0", got)
	}
	// A genuine request still works afterwards: the refusals did not wedge the loop.
	if w := env.b.getState(t); w.Code != 200 {
		t.Errorf("a genuine read after the forgeries: %d %s", w.Code, w.Body)
	}
}
