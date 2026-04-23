package rivian

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// stubGateway spins up an httptest server that speaks the tiny subset
// of the Rivian gateway we care about. Responses are driven by
// operationName so a single server handles the whole auth + query
// flow.
type stubGateway struct {
	t               *testing.T
	srv             *httptest.Server
	mfaRequired     bool
	capturedReqs    []gatewayCall
	failCSRF        bool
	failLogin       bool
	badUserTypename bool
}

type gatewayCall struct {
	Operation string
	Headers   http.Header
	Body      map[string]any
}

func newStubGateway(t *testing.T) *stubGateway {
	t.Helper()
	g := &stubGateway{t: t}
	g.srv = httptest.NewServer(http.HandlerFunc(g.handle))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *stubGateway) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	op, _ := body["operationName"].(string)
	g.capturedReqs = append(g.capturedReqs, gatewayCall{
		Operation: op,
		Headers:   r.Header.Clone(),
		Body:      body,
	})

	// Always return application/json so the client's parser is happy.
	w.Header().Set("Content-Type", "application/json")

	switch op {
	case "CreateCSRFToken":
		if g.failCSRF {
			writeJSONErr(w, "csrf blew up")
			return
		}
		writeJSON(w, map[string]any{
			"data": map[string]any{
				"createCsrfToken": map[string]any{
					"__typename":      "CreateCsrfTokenResponse",
					"csrfToken":       "csrf-xyz",
					"appSessionToken": "a-sess-abc",
				},
			},
		})
	case "Login":
		if g.failLogin {
			writeJSONErr(w, "bad creds")
			return
		}
		if g.mfaRequired {
			writeJSON(w, map[string]any{
				"data": map[string]any{
					"login": map[string]any{
						"__typename": "MobileMFALoginResponse",
						"otpToken":   "otp-token-123",
					},
				},
			})
			return
		}
		writeJSON(w, map[string]any{
			"data": map[string]any{
				"login": map[string]any{
					"__typename":       "MobileLoginResponse",
					"accessToken":      "access-abc",
					"refreshToken":     "refresh-abc",
					"userSessionToken": "u-sess-abc",
				},
			},
		})
	case "LoginWithOTP":
		writeJSON(w, map[string]any{
			"data": map[string]any{
				"loginWithOTP": map[string]any{
					"__typename":       "MobileLoginResponse",
					"accessToken":      "access-otp",
					"refreshToken":     "refresh-otp",
					"userSessionToken": "u-sess-otp",
				},
			},
		})
	case "getUserInfo":
		writeJSON(w, map[string]any{
			"data": map[string]any{
				"currentUser": map[string]any{
					"id":        "user-1",
					"firstName": "Anton",
					"lastName":  "P",
					"email":     "anton@example.com",
					"vehicles": []any{
						map[string]any{
							"id":      "veh-a",
							"name":    "R1T",
							"vin":     "7FC000000000000A1",
							"vehicle": map[string]any{"model": "R1T"},
						},
						map[string]any{
							"id":      "veh-b",
							"name":    "",
							"vin":     "7FC000000000000B2",
							"vehicle": map[string]any{"model": "R1S"},
						},
					},
				},
			},
		})
	case "GetVehicleState":
		writeJSON(w, map[string]any{
			"data": map[string]any{
				"vehicleState": map[string]any{
					"__typename": "VehicleState",
					"gnssLocation": map[string]any{
						"latitude":  37.7749,
						"longitude": -122.4194,
						"timeStamp": "2026-04-23T18:00:00.000Z",
					},
					"gnssSpeed":                       map[string]any{"value": 0},
					"batteryLevel":                    map[string]any{"value": 72.5},
					"distanceToEmpty":                 map[string]any{"value": 310.0},
					"vehicleMileage":                  map[string]any{"value": 24500.0},
					"gearStatus":                      map[string]any{"value": "P"},
					"chargerState":                    map[string]any{"value": "charger_disconnected"},
					"chargerStatus":                   map[string]any{"value": "chrgr_sts_not_connected"},
					"batteryLimit":                    map[string]any{"value": 80.0},
					"cabinClimateInteriorTemperature": map[string]any{"value": 21.0},
					"powerState":                      map[string]any{"value": "sleep"},
					"doorFrontLeftLocked":             map[string]any{"value": "locked"},
					"doorFrontRightLocked":            map[string]any{"value": "locked"},
					"doorRearLeftLocked":              map[string]any{"value": "locked"},
					"doorRearRightLocked":             map[string]any{"value": "locked"},
					"closureFrunkLocked":              map[string]any{"value": "locked"},
					"closureLiftgateLocked":           map[string]any{"value": "locked"},
					"closureTonneauLocked":            map[string]any{"value": ""},
					"closureTailgateLocked":           map[string]any{"value": "locked"},
					"closureSideBinLeftLocked":        map[string]any{"value": "locked"},
					"closureSideBinRightLocked":       map[string]any{"value": "locked"},
				},
			},
		})
	default:
		http.Error(w, "unknown operation "+op, http.StatusBadRequest)
	}
}

func writeJSON(w http.ResponseWriter, body any) {
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONErr(w http.ResponseWriter, msg string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":   nil,
		"errors": []any{map[string]any{"message": msg}},
	})
}

// --- Tests -----------------------------------------------------------

// Happy path: CSRF → Login → user → GetVehicleState.
func TestLiveClientHappyPath(t *testing.T) {
	g := newStubGateway(t)
	c := NewLive().WithEndpoint(g.srv.URL)

	ctx := context.Background()
	if err := c.Login(ctx, Credentials{Email: "a@b.c", Password: "pw"}); err != nil {
		t.Fatalf("Login: %v", err)
	}

	// After login we should have captured exactly CSRF then Login, and
	// the Login call must carry a-sess and csrf-token headers.
	if len(g.capturedReqs) != 2 {
		t.Fatalf("len(capturedReqs)=%d, want 2", len(g.capturedReqs))
	}
	if g.capturedReqs[0].Operation != "CreateCSRFToken" {
		t.Errorf("first op = %q, want CreateCSRFToken", g.capturedReqs[0].Operation)
	}
	loginCall := g.capturedReqs[1]
	if loginCall.Operation != "Login" {
		t.Errorf("second op = %q, want Login", loginCall.Operation)
	}
	if got := loginCall.Headers.Get("a-sess"); got != "a-sess-abc" {
		t.Errorf("Login a-sess header = %q, want a-sess-abc", got)
	}
	if got := loginCall.Headers.Get("csrf-token"); got != "csrf-xyz" {
		t.Errorf("Login csrf-token header = %q, want csrf-xyz", got)
	}
	if got := loginCall.Headers.Get("apollographql-client-name"); got != DefaultClientName {
		t.Errorf("Login client-name header = %q, want %q", got, DefaultClientName)
	}

	vs, err := c.Vehicles(ctx)
	if err != nil {
		t.Fatalf("Vehicles: %v", err)
	}
	if len(vs) != 2 || vs[0].ID != "veh-a" || vs[1].VIN != "7FC000000000000B2" {
		t.Errorf("Vehicles = %+v, want 2 vehicles [veh-a, veh-b]", vs)
	}
	// Authenticated call must carry all three tokens.
	userCall := g.capturedReqs[len(g.capturedReqs)-1]
	if userCall.Headers.Get("u-sess") != "u-sess-abc" {
		t.Errorf("user u-sess header = %q, want u-sess-abc", userCall.Headers.Get("u-sess"))
	}

	st, err := c.State(ctx, "veh-a")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if st.VehicleID != "veh-a" || st.BatteryLevelPct != 72.5 ||
		st.Latitude != 37.7749 || st.OdometerKm != 24500 || st.Gear != "P" ||
		!st.Locked {
		t.Errorf("State = %+v, unexpected", *st)
	}
	if st.At.IsZero() {
		t.Errorf("State.At should be parsed from timeStamp, got zero")
	}
}

// MFA flow: first Login returns ErrMFARequired; second Login with OTP
// completes the flow against LoginWithOTP.
func TestLiveClientMFAFlow(t *testing.T) {
	g := newStubGateway(t)
	g.mfaRequired = true
	c := NewLive().WithEndpoint(g.srv.URL)

	ctx := context.Background()
	err := c.Login(ctx, Credentials{Email: "a@b.c", Password: "pw"})
	if !errors.Is(err, ErrMFARequired) {
		t.Fatalf("first Login err = %v, want ErrMFARequired", err)
	}

	// After MFA challenge we should have OTP token stashed but no user
	// session yet; Vehicles must refuse.
	if _, err := c.Vehicles(ctx); err == nil {
		t.Error("Vehicles before OTP should fail, got nil")
	}

	// Second call supplies the OTP. The email can be omitted — we
	// stash it during the first attempt.
	if err := c.Login(ctx, Credentials{OTP: "123456"}); err != nil {
		t.Fatalf("second Login: %v", err)
	}

	// Expect CSRF, Login, LoginWithOTP.
	ops := make([]string, 0, len(g.capturedReqs))
	for _, call := range g.capturedReqs {
		ops = append(ops, call.Operation)
	}
	if strings.Join(ops, ",") != "CreateCSRFToken,Login,LoginWithOTP" {
		t.Errorf("ops = %v, want [CreateCSRFToken Login LoginWithOTP]", ops)
	}
	otpCall := g.capturedReqs[2]
	vars, _ := otpCall.Body["variables"].(map[string]any)
	if vars["otpToken"] != "otp-token-123" || vars["otpCode"] != "123456" ||
		vars["email"] != "a@b.c" {
		t.Errorf("LoginWithOTP variables = %+v, missing expected fields", vars)
	}

	// Now vehicles should work.
	vs, err := c.Vehicles(ctx)
	if err != nil {
		t.Fatalf("Vehicles after OTP: %v", err)
	}
	if len(vs) != 2 {
		t.Errorf("len(vehicles)=%d, want 2", len(vs))
	}
}

// Not-authenticated calls short-circuit locally (no HTTP).
func TestLiveClientRefusesUnauthenticated(t *testing.T) {
	g := newStubGateway(t)
	c := NewLive().WithEndpoint(g.srv.URL)
	if _, err := c.Vehicles(context.Background()); err == nil {
		t.Error("Vehicles without Login should fail, got nil")
	}
	if _, err := c.State(context.Background(), "veh-a"); err == nil {
		t.Error("State without Login should fail, got nil")
	}
	if len(g.capturedReqs) != 0 {
		t.Errorf("captured %d HTTP calls, want 0 — pre-auth should not hit server", len(g.capturedReqs))
	}
}

// Server-returned GraphQL errors bubble up as Go errors.
func TestLiveClientPropagatesGraphQLErrors(t *testing.T) {
	g := newStubGateway(t)
	g.failCSRF = true
	c := NewLive().WithEndpoint(g.srv.URL)

	err := c.Login(context.Background(), Credentials{Email: "a", Password: "b"})
	if err == nil || !strings.Contains(err.Error(), "csrf blew up") {
		t.Errorf("err=%v, want containing 'csrf blew up'", err)
	}
}

// Re-login is idempotent wrt CSRF — we cache the token and re-use it.
func TestLiveClientReusesCSRF(t *testing.T) {
	g := newStubGateway(t)
	c := NewLive().WithEndpoint(g.srv.URL)

	ctx := context.Background()
	if err := c.Login(ctx, Credentials{Email: "a@b.c", Password: "pw"}); err != nil {
		t.Fatalf("Login 1: %v", err)
	}
	if err := c.Login(ctx, Credentials{Email: "a@b.c", Password: "pw"}); err != nil {
		t.Fatalf("Login 2: %v", err)
	}
	var csrfCount int32
	for _, call := range g.capturedReqs {
		if call.Operation == "CreateCSRFToken" {
			atomic.AddInt32(&csrfCount, 1)
		}
	}
	if csrfCount != 1 {
		t.Errorf("CreateCSRFToken calls = %d, want 1 (should be cached)", csrfCount)
	}
}

// --- Mock client tests ----------------------------------------------

func TestMockClientRequiresLogin(t *testing.T) {
	c := NewMock()
	if _, err := c.Vehicles(context.Background()); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("Vehicles pre-login err = %v, want ErrNotAuthenticated", err)
	}
	if _, err := c.State(context.Background(), "mock-vehicle-1"); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("State pre-login err = %v, want ErrNotAuthenticated", err)
	}
}

func TestMockClientHappyPath(t *testing.T) {
	c := NewMock()
	ctx := context.Background()
	if err := c.Login(ctx, Credentials{Email: "x", Password: "y"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	vs, err := c.Vehicles(ctx)
	if err != nil || len(vs) != 1 {
		t.Fatalf("Vehicles: %v len=%d", err, len(vs))
	}
	st, err := c.State(ctx, vs[0].ID)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if st.VehicleID != vs[0].ID || st.BatteryLevelPct == 0 {
		t.Errorf("State=%+v, unexpected", *st)
	}
	if _, err := c.State(ctx, "no-such-vehicle"); !errors.Is(err, ErrVehicleNotFound) {
		t.Errorf("State(unknown) err=%v, want ErrVehicleNotFound", err)
	}
}

func TestMockClientSurfacesLoginFailure(t *testing.T) {
	c := NewMock()
	c.LoginReturnErr = errors.New("boom")
	if err := c.Login(context.Background(), Credentials{}); err == nil || err.Error() != "boom" {
		t.Errorf("Login err = %v, want boom", err)
	}
}
