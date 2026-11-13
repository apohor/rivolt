package api

import (
	"context"
	"encoding/json"
	"github.com/alicebob/miniredis/v2"
	"github.com/apohor/rivolt/internal/planleg"
	"github.com/redis/go-redis/v9"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

// A pod running with RIVOLT_NO_STANDING_TOKENS holds no client for a user whose group it does not hold: the registry answers a nil Account. Every
// API route, called as that user with every method the router lists, must not panic and must not answer a 5xx other than the retry-able 503.
func TestNoStandingTokensEveryRouteOnATokenlessPod(t *testing.T) {
	reg := rivian.NewLiveAccountRegistry(func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	g, ok := reg.(interface {
		SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool), func(uuid.UUID) *rivian.LiveClient)
	})
	if !ok {
		t.Fatal("the live registry cannot be gated")
	}
	g.SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool) { return false, true }, func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })

	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: reg})
	uid := uuid.New()
	if reg.For(uid) != nil {
		t.Fatal("the gated registry handed out a client")
	}

	pathOf := func(p string) string {
		for strings.Contains(p, "{") {
			i := strings.Index(p, "{")
			j := strings.Index(p[i:], "}")
			p = p[:i] + "x" + p[i+j+1:]
		}
		return strings.ReplaceAll(p, "*", "x")
	}
	n := 0
	err = chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/") {
			return nil
		}
		n++
		path := pathOf(route)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s %s panicked on a token-less pod: %v", method, route, r)
				}
			}()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "https://rivolt.dev"+path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "203.0.113.9:4000"
			ctx := auth.WithUser(req.Context(), uid)
			ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: "web"})
			router.ServeHTTP(rec, req.WithContext(ctx))
			// This router has no database: a route that reads Postgres answers its own 500 for that. Anything else at 5xx but the
			// retry-able 503 (a recovered panic reads "Internal Server Error") is the nil client.
			if rec.Code >= 500 && rec.Code != http.StatusServiceUnavailable && !noDB(rec.Body.String()) {
				t.Errorf("%s %s = %d %q on a token-less pod, want no panic and 503 at worst", method, route, rec.Code, rec.Body.String())
			}
		}()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 50 {
		t.Fatalf("walked %d routes, the router lists more than that", n)
	}
}

func noDB(body string) bool {
	for _, m := range []string{"nil db", "not initialized", "could not read the account"} {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

// The Rivian status route never reports a live session on a pod that does not hold the account (no client, no call to Rivian), and logout
// does not panic. With no database in this router the status read answers 503; with one it is the stored state.
func TestNoStandingTokensRivianStatusAndLogoutOnATokenlessPod(t *testing.T) {
	reg := rivian.NewLiveAccountRegistry(func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	reg.(interface {
		SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool), func(uuid.UUID) *rivian.LiveClient)
	}).SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool) { return false, true }, func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: reg})
	uid := uuid.New()
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		ctx := auth.WithSession(auth.WithUser(req.Context(), uid), auth.SessionRef{ID: uuid.New(), Kind: "web"})
		router.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	rec := do(http.MethodGet, "/api/settings/rivian/")
	if (rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable) || strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Fatalf("status on a token-less pod = %d %q, want the stored state or 503, never a live session", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, "/api/settings/rivian/logout"); rec.Code >= 500 && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("logout on a token-less pod = %d %q", rec.Code, rec.Body.String())
	}
}

// "Check again" on a pod that does not hold the account asks the owner to look the user up (the sign-in nudge) and answers the retry-able 503
// with Retry-After, so the client polls the stored answer instead of failing.
func TestRefreshVehiclesOnAnUnheldPodNudgesTheOwner(t *testing.T) {
	reg := rivian.NewLiveAccountRegistry(func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	reg.(interface {
		SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool), func(uuid.UUID) *rivian.LiveClient)
	}).SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool) { return false, true }, func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	uid := uuid.New()
	var nudged []uuid.UUID
	h := withSignInNudge(func(_ context.Context, u uuid.UUID) { nudged = append(nudged, u) })(handleRivianRefreshVehicles(reg, nil, nil, nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(auth.WithUser(req.Context(), uid)))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, Retry-After %q, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	if len(nudged) != 1 || nudged[0] != uid {
		t.Fatalf("nudged %v, want the user once", nudged)
	}
}

// The Nov 12 prod 503: GET /api/state/{id} through the real router, as a user whose group another pod holds, on a pod with no tokens. The
// request must reach the forwarder (the owner answers) instead of the unheld client refusing it.
func TestStateOnATokenlessPodIsForwardedThroughTheRouter(t *testing.T) {
	reg := rivian.NewLiveAccountRegistry(func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	reg.(interface {
		SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool), func(uuid.UUID) *rivian.LiveClient)
	}).SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool) { return false, true }, func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(rivian.State{VehicleID: fwdVehicle, PowerState: "ready", BatteryLevelPct: 61})
	fw := &fakeStateForwarder{res: forward.Result{Status: 200, Body: body}}
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: reg, StateForward: fw,
		GroupOfVehicle: func(string) (string, bool) { return fwdGroup, true }})

	req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/state/"+fwdVehicle, nil)
	req.RemoteAddr = "203.0.113.9:4000"
	ctx := auth.WithUser(req.Context(), uuid.New())
	ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: "web"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/state on a token-less pod = %d %q, want the owner's 200", rec.Code, rec.Body.String())
	}
	if len(fw.calls) != 1 || fw.calls[0].Kind != forward.KindFreshState {
		t.Fatalf("forwarder calls %+v, want one fresh_state", fw.calls)
	}
}

func gatedRegistry() rivian.AccountRegistry {
	reg := rivian.NewLiveAccountRegistry(func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	reg.(interface {
		SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool), func(uuid.UUID) *rivian.LiveClient)
	}).SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool) { return false, true }, func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	return reg
}

// Trip planning on a token-less pod: the client clientFor hands the route is a planner (no "live rivian client required" 404), and the leg a
// cache miss needs goes to the group owner.
func TestTripLegOnATokenlessPodIsForwardedToTheOwner(t *testing.T) {
	d := Deps{Accounts: gatedRegistry(), GroupOfVehicle: func(string) (string, bool) { return fwdGroup, true }}
	c := clientFor(d, uuid.New())
	if _, ok := tripPlannerOf(c); !ok {
		t.Fatal("the token-less pod's client is not a trip planner: the route answers 404")
	}
	fw := &planForwarder{res: ServePlanLegs(context.Background(), plannerClient{Client: rivian.NewStub(), TripPlanner: planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) { return samplePlan(), nil })}, sampleInput())}
	mr := miniredis.RunT(t)
	d.PlanLegs = planleg.New(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "t", nil)
	d.StateForward = fw
	planner, ok := tripPlannerOf(withLegCache(d, uuid.New(), c))
	if !ok {
		t.Fatal("no planner behind the leg cache")
	}
	plan, err := planner.PlanTrip(context.Background(), sampleInput())
	if err != nil || plan == nil || plan.Status != "OK" {
		t.Fatalf("plan %+v, err %v; want the owner's plan", plan, err)
	}
	if fw.count() != 1 {
		t.Errorf("%d forwards, want 1", fw.count())
	}
}

// Release notes on a token-less pod: the source is an OTASource, so the read is forwarded rather than skipped.
func TestOTANotesOnATokenlessPodAreForwardedToTheOwner(t *testing.T) {
	d := Deps{Accounts: gatedRegistry()}
	src, ok := clientFor(d, uuid.New()).(rivian.OTASource)
	if !ok {
		t.Fatal("the token-less pod's client is not an OTA source: notes are never asked of the owner")
	}
	body, _ := json.Marshal(otaNotesLinks{})
	fw := &fakeStateForwarder{res: forward.Result{Status: 200, Body: body}}
	sf := &stateForward{fw: fw, user: uuid.New(), groupOf: func(string) (string, bool) { return fwdGroup, true }}
	if _, err := sf.forwardOTANotes(context.Background(), src, fwdVehicle); err != nil {
		t.Fatal(err)
	}
	if len(fw.calls) != 1 || fw.calls[0].Kind != forward.KindOTANotes {
		t.Fatalf("forwarder calls %+v, want one ota_notes", fw.calls)
	}
}

// The web polls live-session every 10 s: on a token-less pod it is served from the pod's monitor (a zeroed payload when it has none), not a 404.
func TestLiveSessionOnATokenlessPodIsNotA404(t *testing.T) {
	h := handleLiveSession(rivian.NewUnheldClient(), rivian.NewStateMonitor(nil, nil), nil)
	if rec := stateGet(h); rec.Code != http.StatusOK {
		t.Fatalf("live-session with a monitor = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if rec := stateGet(handleLiveSession(rivian.NewUnheldClient(), nil, nil)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("live-session with no monitor = %d, want the retry-able 503", rec.Code)
	}
}

// A refusal that is the pod's own (it holds no tokens) is never sent bare: every route that ends in it carries Retry-After. Through the real
// router with full URLs: the state read with no forwarder to hand it to (a soft fall answered here) and the fresh read.
func TestTokenlessPodRefusalsCarryRetryAfter(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: gatedRegistry()})
	for _, path := range []string{"/api/state/" + fwdVehicle, "/api/state/" + fwdVehicle + "/fresh"} {
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev"+path, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		ctx := auth.WithUser(req.Context(), uuid.New())
		ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: "web"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Errorf("GET %s = %d, Retry-After %q (%s); want 503 with Retry-After", path, rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
		}
	}
}

// The raw probes keep their guard: an unheld client is not a live client, so they answer 404 instead of calling into one.
func TestRawProbesRefuseAnUnheldClient(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"charging schema":    handleChargingSchemaProbe(rivian.NewUnheldClient()),
		"battery temp":       handleBatteryTempProbe(rivian.NewUnheldClient()),
		"parallax raw":       handleParallaxRawProbe(rivian.NewUnheldClient()),
		"supported features": handleSupportedFeaturesProbe(rivian.NewUnheldClient()),
		"charging field":     handleChargingFieldProbe(rivian.NewUnheldClient()),
		"charging frames":    handleChargingFrames(rivian.NewUnheldClient()),
	} {
		if rec := stateGet(h); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", name, rec.Code)
		}
	}
}

// "Check again" through the real router on a pod that holds no tokens: the owner is nudged once and the answer is the retry-able 503 with
// Retry-After, never the bare notHeld error.
func TestRefreshVehiclesRouteOnATokenlessPod(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	uid := uuid.New()
	var nudged []uuid.UUID
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: gatedRegistry(),
		SignInNudge: func(_ context.Context, u uuid.UUID) { nudged = append(nudged, u) }})
	req := httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/settings/rivian/refresh-vehicles", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:4000"
	ctx := auth.WithUser(req.Context(), uid)
	ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: "web"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("POST refresh-vehicles = %d, Retry-After %q (%s); want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	if len(nudged) != 1 || nudged[0] != uid {
		t.Fatalf("nudged %v, want the user once", nudged)
	}
}

// A second "Check again" inside the owner's lookup window, from any pod, is a 429 the app can tell apart from "the owner was asked" (503); the
// owner is nudged only for the first tap.
func TestRefreshVehiclesOnATokenlessPodIsLimitedAfterTheFirstLookup(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	uid := uuid.New()
	var nudged int
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: gatedRegistry(), Redis: rdb, RedisPrefix: "t", FailLimiterKey: []byte("0123456789abcdef0123456789abcdef"),
		SignInNudge: func(context.Context, uuid.UUID) { nudged++ }})
	tap := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/settings/rivian/refresh-vehicles", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.9:4000"
		ctx := auth.WithUser(req.Context(), uid)
		ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: "web"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	if rec := tap(); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("first tap = %d (%s), want 503", rec.Code, rec.Body.String())
	}
	rec := tap()
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("second tap = %d, Retry-After %q (%s); want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "limited" {
		t.Fatalf("second tap body %s, want error limited", rec.Body.String())
	}
	if _, has := body["class"]; has {
		t.Fatalf("429 body carries a class: %s", rec.Body.String())
	}
	if nudged != 1 {
		t.Fatalf("nudged %d times, want once", nudged)
	}
}

// A nudge that reached no owner must not cost the user ten minutes of 429 with no lookup made.
func TestRefreshVehiclesOnATokenlessPodHandsTheSlotBackWhenTheNudgeIsLost(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	uid := uuid.New()
	var nudged int
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: gatedRegistry(), Redis: rdb, RedisPrefix: "t", FailLimiterKey: []byte("0123456789abcdef0123456789abcdef"),
		RefreshNudge: func(_ context.Context, _ uuid.UUID, lost func()) { nudged++; lost() }})
	tap := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://rivolt.dev/api/settings/rivian/refresh-vehicles", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.9:4000"
		ctx := auth.WithUser(req.Context(), uid)
		ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: "web"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	for i := 0; i < 2; i++ {
		if rec := tap(); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("tap %d after a lost nudge = %d (%s), want 503: the slot was handed back", i+1, rec.Code, rec.Body.String())
		}
	}
	if nudged != 2 {
		t.Fatalf("nudged %d times, want 2", nudged)
	}
}

// liveSessionGet drives GET /api/live-session/{id} through the real router on a token-less pod whose forwarder is fw.
func liveSessionGet(t *testing.T, fw StateForwarder) *httptest.ResponseRecorder {
	t.Helper()
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: gatedRegistry(), StateForward: fw,
		GroupOfVehicle: func(string) (string, bool) { return fwdGroup, true }})
	req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/live-session/"+fwdVehicle, nil)
	req.RemoteAddr = "203.0.113.9:4000"
	ctx := auth.WithUser(req.Context(), uuid.New())
	ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: "web"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// The api pod holds no monitor: the live panel's session totals come from the owner's, through a live_session forward.
func TestLiveSessionRouteOnATokenlessPodAnswersWithTheOwnersTotals(t *testing.T) {
	body, _ := json.Marshal(wireOf(&rivian.LiveSession{VehicleID: fwdVehicle, Active: true, PowerKW: 11.2, TotalChargedEnergyKWh: 18.5},
		rivian.ChargeNow{EnergyKWh: 18.5, PeakKW: 11.2}))
	fw := &fakeStateForwarder{res: forward.Result{Status: 200, Body: body}}
	rec := liveSessionGet(t, fw)
	if rec.Code != http.StatusOK {
		t.Fatalf("live-session = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var got struct {
		Active bool    `json:"active"`
		Total  float64 `json:"total_charged_energy_kwh"`
		Power  float64 `json:"power_kw"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.Active || got.Total != 18.5 || got.Power != 11.2 {
		t.Fatalf("live-session body %s (%v), want the owner's totals", rec.Body.String(), err)
	}
	if len(fw.calls) != 1 || fw.calls[0].Kind != forward.KindLiveSession || fw.calls[0].Group != fwdGroup {
		t.Fatalf("forwarder calls %+v, want one live_session to the vehicle's group", fw.calls)
	}
}

// No owner to ask (the forward comes back unavailable): a retry-able 503, never a zeroed 200 the panel would show as an empty charge.
func TestLiveSessionRouteOnATokenlessPodWithNoOwnerIsRetryable(t *testing.T) {
	rec := liveSessionGet(t, &fakeStateForwarder{res: forward.UnavailableResult()})
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("live-session with no owner = %d, Retry-After %q (%s); want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

// The owner's runner side: its monitor's session and charge-now go back in the wire shape the requester decodes.
func TestServeLiveSessionReturnsTheMonitorsRead(t *testing.T) {
	res := ServeLiveSession(rivian.NewStateMonitor(nil, nil), fwdVehicle)
	if res.Status != http.StatusOK {
		t.Fatalf("ServeLiveSession = %d, want 200", res.Status)
	}
	var w liveSessionWire
	if err := json.Unmarshal(res.Body, &w); err != nil {
		t.Fatal(err)
	}
	if got := ServeLiveSession(nil, fwdVehicle); got.Status != http.StatusServiceUnavailable {
		t.Fatalf("ServeLiveSession without a monitor = %d, want 503", got.Status)
	}
}
