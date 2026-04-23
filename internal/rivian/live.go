package rivian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultEndpoint is the Rivian Owner App GraphQL gateway used by the
// mobile app. This is an unofficial endpoint — it can and does break.
const DefaultEndpoint = "https://rivian.com/api/gql/gateway/graphql"

// DefaultClientName is the apollographql-client-name header value the
// Android app sends. Some Rivian endpoints gate behaviour on this.
const DefaultClientName = "com.rivian.android.consumer"

// ErrMFARequired is returned by Login when the account has MFA enabled
// and the server issued an OTP challenge. Callers should collect the
// one-time code from the user and call Login again with Credentials.OTP
// populated. Between the two calls, the same LiveClient instance must
// be reused — it holds the otpToken needed for the second step.
var ErrMFARequired = errors.New("rivian: MFA code required")

// LiveClient talks to the real Rivian Owner App GraphQL gateway.
//
// Thread-safety: all exported methods take the internal mutex while
// they touch tokens or hit the network. It is safe to call Login,
// Vehicles, and State concurrently from different goroutines, though
// in practice the server will rate-limit you long before it matters.
//
// This client is a best-effort reimplementation of the mobile app's
// auth flow based on the community docs at
// https://rivian-api.kaedenb.org. The happy path (CSRF → Login → GetVehicleState)
// is covered by unit tests; token refresh and some edge cases are
// TODO. Until those land, callers should expect to re-login when the
// session expires.
type LiveClient struct {
	httpClient *http.Client
	endpoint   string
	clientName string

	mu               sync.Mutex
	csrfToken        string
	appSessionToken  string // "a-sess" header
	userSessionToken string // "u-sess" header
	accessToken      string
	refreshToken     string
	email            string // owner's email, populated on successful Login
	pendingOTPToken  string // populated when the server returns an MFA challenge
	pendingOTPEmail  string
	authenticatedAt  time.Time
}

// NewLive returns a LiveClient with sane defaults. Pass a zero-value
// http.Client if you want — we set a reasonable timeout below.
func NewLive() *LiveClient {
	return &LiveClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		endpoint:   DefaultEndpoint,
		clientName: DefaultClientName,
	}
}

// WithEndpoint points the client at an alternate GraphQL URL. Used by
// the unit tests to redirect to an httptest server.
func (c *LiveClient) WithEndpoint(url string) *LiveClient {
	c.endpoint = url
	return c
}

// WithHTTPClient lets callers install a custom *http.Client (e.g. one
// with a proxy, or with logging transport wrapped for debugging).
func (c *LiveClient) WithHTTPClient(h *http.Client) *LiveClient {
	c.httpClient = h
	return c
}

// graphQLRequest is the JSON body shape the Rivian gateway expects for
// every GraphQL request.
type graphQLRequest struct {
	OperationName string `json:"operationName"`
	Query         string `json:"query"`
	Variables     any    `json:"variables"`
}

// graphQLError matches the per-error entry the Rivian gateway returns
// when a query fails validation or the server blows up.
type graphQLError struct {
	Message    string   `json:"message"`
	Path       []string `json:"path,omitempty"`
	Extensions struct {
		Code string `json:"code,omitempty"`
	} `json:"extensions"`
}

// graphQLResponse is the outer envelope for every GraphQL call.
type graphQLResponse[T any] struct {
	Data   T              `json:"data"`
	Errors []graphQLError `json:"errors,omitempty"`
}

// doGraphQL posts a GraphQL request and decodes the response into out.
// The extraHeaders map is merged over the base set — callers use it to
// attach a-sess / u-sess / csrf-token depending on which stage of auth
// they're in.
func doGraphQL[T any](ctx context.Context, c *LiveClient, req graphQLRequest, extraHeaders map[string]string) (T, error) {
	var zero T
	body, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("marshal %s: %w", req.OperationName, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("apollographql-client-name", c.clientName)
	httpReq.Header.Set("User-Agent", "rivolt/0.1 (+https://github.com/apohor/rivolt)")
	for k, v := range extraHeaders {
		if v != "" {
			httpReq.Header.Set(k, v)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return zero, fmt.Errorf("post %s: %w", req.OperationName, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return zero, fmt.Errorf("read %s response: %w", req.OperationName, err)
	}
	if resp.StatusCode >= 400 {
		return zero, fmt.Errorf("rivian %s: HTTP %d: %s", req.OperationName, resp.StatusCode, truncate(string(raw), 512))
	}

	var out graphQLResponse[T]
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("decode %s: %w: %s", req.OperationName, err, truncate(string(raw), 256))
	}
	if len(out.Errors) > 0 {
		msgs := make([]string, 0, len(out.Errors))
		for _, e := range out.Errors {
			msgs = append(msgs, e.Message)
		}
		return zero, fmt.Errorf("rivian %s: %s", req.OperationName, strings.Join(msgs, "; "))
	}
	return out.Data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ----- Auth queries ---------------------------------------------------

const (
	qCreateCSRF = `mutation CreateCSRFToken { createCsrfToken { __typename csrfToken appSessionToken } }`

	qLogin = `mutation Login($email: String!, $password: String!) {
  login(email: $email, password: $password) {
    __typename
    ... on MobileLoginResponse { accessToken refreshToken userSessionToken }
    ... on MobileMFALoginResponse { otpToken }
  }
}`

	qLoginOTP = `mutation LoginWithOTP($email: String!, $otpCode: String!, $otpToken: String!) {
  loginWithOTP(email: $email, otpCode: $otpCode, otpToken: $otpToken) {
    __typename accessToken refreshToken userSessionToken
  }
}`
)

type createCSRFData struct {
	CreateCsrfToken struct {
		CSRFToken       string `json:"csrfToken"`
		AppSessionToken string `json:"appSessionToken"`
	} `json:"createCsrfToken"`
}

type loginData struct {
	Login struct {
		Typename         string `json:"__typename"`
		AccessToken      string `json:"accessToken"`
		RefreshToken     string `json:"refreshToken"`
		UserSessionToken string `json:"userSessionToken"`
		OTPToken         string `json:"otpToken"`
	} `json:"login"`
}

type loginOTPData struct {
	LoginWithOTP struct {
		AccessToken      string `json:"accessToken"`
		RefreshToken     string `json:"refreshToken"`
		UserSessionToken string `json:"userSessionToken"`
	} `json:"loginWithOTP"`
}

// ensureCSRF populates csrfToken and appSessionToken if they aren't
// already. Must be called with c.mu held.
func (c *LiveClient) ensureCSRF(ctx context.Context) error {
	if c.csrfToken != "" && c.appSessionToken != "" {
		return nil
	}
	data, err := doGraphQL[createCSRFData](ctx, c, graphQLRequest{
		OperationName: "CreateCSRFToken",
		Query:         qCreateCSRF,
		Variables:     struct{}{},
	}, nil)
	if err != nil {
		return fmt.Errorf("CreateCSRFToken: %w", err)
	}
	c.csrfToken = data.CreateCsrfToken.CSRFToken
	c.appSessionToken = data.CreateCsrfToken.AppSessionToken
	if c.csrfToken == "" || c.appSessionToken == "" {
		return errors.New("CreateCSRFToken: empty token in response")
	}
	return nil
}

// Login performs the email/password → MFA dance. See ErrMFARequired for
// the two-call flow.
func (c *LiveClient) Login(ctx context.Context, creds Credentials) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureCSRF(ctx); err != nil {
		return err
	}

	// Second-step: the caller has already seen ErrMFARequired and is
	// now submitting the OTP. Use LoginWithOTP.
	if creds.OTP != "" && c.pendingOTPToken != "" {
		email := creds.Email
		if email == "" {
			email = c.pendingOTPEmail
		}
		data, err := doGraphQL[loginOTPData](ctx, c, graphQLRequest{
			OperationName: "LoginWithOTP",
			Query:         qLoginOTP,
			Variables: map[string]any{
				"email":    email,
				"otpCode":  creds.OTP,
				"otpToken": c.pendingOTPToken,
			},
		}, map[string]string{
			"a-sess":     c.appSessionToken,
			"csrf-token": c.csrfToken,
		})
		if err != nil {
			return fmt.Errorf("LoginWithOTP: %w", err)
		}
		if data.LoginWithOTP.UserSessionToken == "" {
			return errors.New("LoginWithOTP: empty userSessionToken")
		}
		c.accessToken = data.LoginWithOTP.AccessToken
		c.refreshToken = data.LoginWithOTP.RefreshToken
		c.userSessionToken = data.LoginWithOTP.UserSessionToken
		c.email = email
		c.pendingOTPToken = ""
		c.pendingOTPEmail = ""
		c.authenticatedAt = time.Now()
		return nil
	}

	// First-step: email + password.
	if creds.Email == "" || creds.Password == "" {
		return errors.New("Login: Email and Password are required")
	}
	data, err := doGraphQL[loginData](ctx, c, graphQLRequest{
		OperationName: "Login",
		Query:         qLogin,
		Variables: map[string]any{
			"email":    creds.Email,
			"password": creds.Password,
		},
	}, map[string]string{
		"a-sess":     c.appSessionToken,
		"csrf-token": c.csrfToken,
	})
	if err != nil {
		return fmt.Errorf("Login: %w", err)
	}
	switch data.Login.Typename {
	case "MobileLoginResponse":
		if data.Login.UserSessionToken == "" {
			return errors.New("Login: empty userSessionToken")
		}
		c.accessToken = data.Login.AccessToken
		c.refreshToken = data.Login.RefreshToken
		c.userSessionToken = data.Login.UserSessionToken
		c.email = creds.Email
		c.authenticatedAt = time.Now()
		return nil
	case "MobileMFALoginResponse":
		c.pendingOTPToken = data.Login.OTPToken
		c.pendingOTPEmail = creds.Email
		return ErrMFARequired
	default:
		return fmt.Errorf("Login: unexpected response __typename %q", data.Login.Typename)
	}
}

// authHeaders builds the a-sess / u-sess / csrf-token triple used by
// every authenticated call. Must be called with c.mu held.
func (c *LiveClient) authHeaders() map[string]string {
	return map[string]string{
		"a-sess":     c.appSessionToken,
		"u-sess":     c.userSessionToken,
		"csrf-token": c.csrfToken,
	}
}

// ----- Data queries --------------------------------------------------

// qUser is the getUserInfo query from the Rivian mobile app. The root
// field is currentUser; an earlier version of this code tried `user`
// and hit GRAPHQL_VALIDATION_FAILED on every deployment. We only
// select the fields Rivolt actually needs — the real mobile-app
// query pulls 200+ lines of configuration + phone enrolment that the
// UI doesn't render.
const qUser = `query getUserInfo {
  currentUser {
    __typename
    id
    firstName
    lastName
    email
    vehicles {
      __typename
      id
      name
      vin
      vehicle { __typename model }
    }
  }
}`

type userData struct {
	CurrentUser struct {
		ID        string `json:"id"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Email     string `json:"email"`
		Vehicles  []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			VIN     string `json:"vin"`
			Vehicle struct {
				Model string `json:"model"`
			} `json:"vehicle"`
		} `json:"vehicles"`
	} `json:"currentUser"`
}

// Vehicles lists the vehicles on the authenticated Rivian account.
// Returns id, vin, user-assigned name (may be empty), and model.
func (c *LiveClient) Vehicles(ctx context.Context) ([]Vehicle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, errors.New("rivian: not authenticated; call Login first")
	}
	data, err := doGraphQL[userData](ctx, c, graphQLRequest{
		OperationName: "getUserInfo",
		Query:         qUser,
		Variables:     struct{}{},
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("getUserInfo: %w", err)
	}
	out := make([]Vehicle, 0, len(data.CurrentUser.Vehicles))
	for _, v := range data.CurrentUser.Vehicles {
		out = append(out, Vehicle{
			ID:    v.ID,
			VIN:   v.VIN,
			Name:  v.Name,
			Model: v.Vehicle.Model,
		})
	}
	return out, nil
}

// GetVehicleState returns a narrow subset of the vast VehicleState
// object — just the fields Rivolt actually uses today (location,
// battery, range, gear, charging, cabin/outside temp). The real
// response contains 80+ timestamped values; adding more is a matter of
// expanding the GraphQL selection and the parse struct.
const qVehicleState = `query GetVehicleState($vehicleID: String!) {
  vehicleState(id: $vehicleID) {
    __typename
    gnssLocation { latitude longitude timeStamp }
    gnssSpeed { value }
    batteryLevel { value }
    distanceToEmpty { value }
    vehicleMileage { value }
    gearStatus { value }
    chargerState { value }
    chargerStatus { value }
    batteryLimit { value }
    cabinClimateInteriorTemperature { value }
    doorFrontLeftLocked { value }
  }
}`

type vsValue[T any] struct {
	Value     T      `json:"value"`
	TimeStamp string `json:"timeStamp"`
}

type vehicleStateData struct {
	VehicleState struct {
		GNSSLocation struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			TimeStamp string  `json:"timeStamp"`
		} `json:"gnssLocation"`
		GNSSSpeed                       vsValue[float64] `json:"gnssSpeed"`
		BatteryLevel                    vsValue[float64] `json:"batteryLevel"`
		DistanceToEmpty                 vsValue[float64] `json:"distanceToEmpty"`
		VehicleMileage                  vsValue[float64] `json:"vehicleMileage"`
		GearStatus                      vsValue[string]  `json:"gearStatus"`
		ChargerState                    vsValue[string]  `json:"chargerState"`
		ChargerStatus                   vsValue[string]  `json:"chargerStatus"`
		BatteryLimit                    vsValue[float64] `json:"batteryLimit"`
		CabinClimateInteriorTemperature vsValue[float64] `json:"cabinClimateInteriorTemperature"`
		DoorFrontLeftLocked             vsValue[string]  `json:"doorFrontLeftLocked"`
	} `json:"vehicleState"`
}

// State returns the current snapshot for a vehicle. Units are what the
// server gave us: battery in percent, distances in kilometers, temps
// in Celsius. The odometer field is exposed as-is (kilometers); the
// samples table stores miles, so callers converting for storage need
// to * 0.621371.
func (c *LiveClient) State(ctx context.Context, vehicleID string) (*State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, errors.New("rivian: not authenticated; call Login first")
	}
	if vehicleID == "" {
		return nil, errors.New("rivian: vehicleID is required")
	}
	data, err := doGraphQL[vehicleStateData](ctx, c, graphQLRequest{
		OperationName: "GetVehicleState",
		Query:         qVehicleState,
		Variables:     map[string]any{"vehicleID": vehicleID},
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("GetVehicleState: %w", err)
	}
	vs := data.VehicleState
	at := parseTimeOrNow(vs.GNSSLocation.TimeStamp)
	return &State{
		At:              at,
		VehicleID:       vehicleID,
		BatteryLevelPct: vs.BatteryLevel.Value,
		DistanceToEmpty: vs.DistanceToEmpty.Value,
		OdometerKm:      vs.VehicleMileage.Value,
		Gear:            normalizeGear(vs.GearStatus.Value),
		ChargerState:    vs.ChargerState.Value,
		// ChargerPowerKW: the GetVehicleState schema no longer
		// exposes a live-power field. Kilowatts are available via
		// getLiveSessionData (chrg/user/graphql) — wire that in a
		// follow-up when we render a live charging panel.
		ChargerPowerKW:  0,
		ChargeTargetPct: vs.BatteryLimit.Value,
		Latitude:        vs.GNSSLocation.Latitude,
		Longitude:       vs.GNSSLocation.Longitude,
		Locked:          strings.EqualFold(vs.DoorFrontLeftLocked.Value, "locked"),
		CabinTempC:      vs.CabinClimateInteriorTemperature.Value,
		OutsideTempC:    0,
	}, nil
}

// normalizeGear maps Rivian's gearStatus values ("park", "drive",
// "reverse", "neutral", and occasionally an empty string while the
// car is asleep) to the single-letter contract exposed by State.Gear.
func normalizeGear(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "park", "p":
		return "P"
	case "drive", "d":
		return "D"
	case "reverse", "r":
		return "R"
	case "neutral", "n":
		return "N"
	default:
		return ""
	}
}

func parseTimeOrNow(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t.UTC()
}

// Compile-time assertion: LiveClient satisfies Client.
var _ Client = (*LiveClient)(nil)

// Session is the persisted subset of LiveClient state — enough to
// restore an authenticated session across restarts without asking the
// user to log in again. MFA is stored for a single in-flight challenge
// only (the token is short-lived server-side).
type Session struct {
	Email            string    `json:"email"`
	CSRFToken        string    `json:"csrf_token"`
	AppSessionToken  string    `json:"app_session_token"`
	UserSessionToken string    `json:"user_session_token"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AuthenticatedAt  time.Time `json:"authenticated_at"`
}

// Snapshot returns a copy of the currently-authenticated session, or
// the zero value if nothing is logged in. Safe to persist as JSON.
func (c *LiveClient) Snapshot() Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Session{
		Email:            c.email,
		CSRFToken:        c.csrfToken,
		AppSessionToken:  c.appSessionToken,
		UserSessionToken: c.userSessionToken,
		AccessToken:      c.accessToken,
		RefreshToken:     c.refreshToken,
		AuthenticatedAt:  c.authenticatedAt,
	}
}

// Restore hydrates the client from a prior Snapshot. No network I/O.
// Intended to be called once at startup; subsequent calls overwrite
// everything including any pending OTP state.
func (c *LiveClient) Restore(s Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.csrfToken = s.CSRFToken
	c.appSessionToken = s.AppSessionToken
	c.userSessionToken = s.UserSessionToken
	c.accessToken = s.AccessToken
	c.refreshToken = s.RefreshToken
	c.authenticatedAt = s.AuthenticatedAt
	c.email = s.Email
	c.pendingOTPEmail = ""
	c.pendingOTPToken = ""
}

// Authenticated reports whether the client currently has a valid
// userSessionToken. Does not probe the server — only checks local
// state. Use a short /user query to verify liveness.
func (c *LiveClient) Authenticated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userSessionToken != ""
}

// Email returns the email the current session is authenticated as, or
// "" if no session is active.
func (c *LiveClient) Email() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.email
}

// MFAPending reports whether Login returned ErrMFARequired and the
// client is waiting for an OTP submission. Allows the UI to restore a
// half-completed login across page reloads.
func (c *LiveClient) MFAPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingOTPToken != ""
}

// Logout clears every authenticated-session field but keeps the CSRF
// token (it survives logout server-side and saves a round-trip on the
// next login).
func (c *LiveClient) Logout() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.userSessionToken = ""
	c.accessToken = ""
	c.refreshToken = ""
	c.email = ""
	c.pendingOTPToken = ""
	c.pendingOTPEmail = ""
	c.authenticatedAt = time.Time{}
}
