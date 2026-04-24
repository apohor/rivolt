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

	// Ring buffer of recent raw ChargingSession WS frames for
	// debugging. Populated by runChargingSubscription, read via
	// RecentChargingFrames. Guarded by framesMu.
	framesMu     sync.Mutex
	recentFrames []ChargingFrame
	maxFrames    int

	// Shared WS connection multiplexer. Nil until the first
	// Subscribe* call, torn down when the last subscriber leaves
	// (or the connection dies). Guarded by muxMu.
	muxMu sync.Mutex
	mux   *wsMux
}

// ChargingFrame captures one raw ChargingSession push or lifecycle
// event for diagnostic inspection via the debug HTTP endpoint.
// Event is set for lifecycle markers ("open", "error", "close"); Raw
// carries the JSON payload for push frames.
type ChargingFrame struct {
	At        time.Time `json:"at"`
	VehicleID string    `json:"vehicle_id"`
	Event     string    `json:"event,omitempty"`
	Raw       string    `json:"raw,omitempty"`
	Err       string    `json:"err,omitempty"`
}

// RecordChargingFrame appends a raw frame to the ring buffer.
func (c *LiveClient) RecordChargingFrame(vehicleID string, raw []byte) {
	c.appendFrame(ChargingFrame{
		At:        time.Now().UTC(),
		VehicleID: vehicleID,
		Raw:       string(raw),
	})
}

// RecordChargingEvent appends a lifecycle marker to the ring buffer.
func (c *LiveClient) RecordChargingEvent(vehicleID, event, errMsg string) {
	c.appendFrame(ChargingFrame{
		At:        time.Now().UTC(),
		VehicleID: vehicleID,
		Event:     event,
		Err:       errMsg,
	})
}

func (c *LiveClient) appendFrame(f ChargingFrame) {
	c.framesMu.Lock()
	defer c.framesMu.Unlock()
	if c.maxFrames == 0 {
		c.maxFrames = 40
	}
	c.recentFrames = append(c.recentFrames, f)
	if len(c.recentFrames) > c.maxFrames {
		c.recentFrames = c.recentFrames[len(c.recentFrames)-c.maxFrames:]
	}
}

// RecentChargingFrames returns a copy of the ring buffer, most recent
// last. Filter by vehicleID if non-empty.
func (c *LiveClient) RecentChargingFrames(vehicleID string) []ChargingFrame {
	c.framesMu.Lock()
	defer c.framesMu.Unlock()
	out := make([]ChargingFrame, 0, len(c.recentFrames))
	for _, f := range c.recentFrames {
		if vehicleID != "" && f.VehicleID != vehicleID {
			continue
		}
		out = append(out, f)
	}
	return out
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
	return doGraphQLAt[T](ctx, c, c.endpoint, req, extraHeaders)
}

// doGraphQLAt is the same as doGraphQL but targets an arbitrary URL.
// Used for the charging endpoint (`/api/gql/chrg/user/graphql`) which
// hosts `getLiveSessionData` and `getRegisteredWallboxes` — separate
// from the main gateway but sharing the same auth headers.
func doGraphQLAt[T any](ctx context.Context, c *LiveClient, url string, req graphQLRequest, extraHeaders map[string]string) (T, error) {
	var zero T
	body, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("marshal %s: %w", req.OperationName, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
      vehicle {
        __typename
        model
        make
        modelYear
        mobileConfiguration {
          __typename
          trimOption { __typename optionId optionName }
        }
      }
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
				Model               string `json:"model"`
				Make                string `json:"make"`
				ModelYear           int    `json:"modelYear"`
				MobileConfiguration struct {
					TrimOption struct {
						OptionID   string `json:"optionId"`
						OptionName string `json:"optionName"`
					} `json:"trimOption"`
				} `json:"mobileConfiguration"`
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
		veh := Vehicle{
			ID:        v.ID,
			VIN:       v.VIN,
			Name:      v.Name,
			Model:     v.Vehicle.Model,
			Make:      v.Vehicle.Make,
			ModelYear: v.Vehicle.ModelYear,
			TrimID:    v.Vehicle.MobileConfiguration.TrimOption.OptionID,
			TrimName:  v.Vehicle.MobileConfiguration.TrimOption.OptionName,
		}
		veh.PackKWh = InferPackKWh(veh.Model, veh.TrimID, veh.ModelYear)
		out = append(out, veh)
	}
	return out, nil
}

// GetVehicleState returns a snapshot of the vehicle's state. The
// upstream object has ~100 timestamped fields; we pull the subset
// that's useful for a dashboard (location, battery, range, gear,
// charging, climate, closures, tires, OTA, safety, power state).
// Adding more is a matter of expanding the GraphQL selection and
// the parse struct below; field names come straight from
// home-assistant-rivian's entity map.
const qVehicleState = `query GetVehicleState($vehicleID: String!) {
  vehicleState(id: $vehicleID) {
    __typename
    gnssLocation { latitude longitude timeStamp }
    gnssSpeed { value }
    gnssBearing { value }
    gnssAltitude { value }
    batteryLevel { value }
    distanceToEmpty { value }
    vehicleMileage { value }
    gearStatus { value }
    driveMode { value }
    chargerState { value }
    chargerStatus { value }
    batteryLimit { value }
    chargePortState { value }
    remoteChargingAvailable { value }
    cabinClimateInteriorTemperature { value }
    cabinPreconditioningStatus { value }
    powerState { value }
    alarmSoundStatus { value }
    twelveVoltBatteryHealth { value }
    wiperFluidState { value }
    otaCurrentVersion { value }
    otaAvailableVersion { value }
    otaStatus { value }
    otaInstallProgress { value }
    tirePressureStatusFrontLeft { value }
    tirePressureStatusFrontRight { value }
    tirePressureStatusRearLeft { value }
    tirePressureStatusRearRight { value }
    doorFrontLeftClosed { value }
    doorFrontRightClosed { value }
    doorRearLeftClosed { value }
    doorRearRightClosed { value }
    closureFrunkClosed { value }
    closureLiftgateClosed { value }
    closureTailgateClosed { value }
    closureTonneauClosed { value }
    doorFrontLeftLocked { value }
    doorFrontRightLocked { value }
    doorRearLeftLocked { value }
    doorRearRightLocked { value }
    closureFrunkLocked { value }
    closureLiftgateLocked { value }
    closureTonneauLocked { value }
    closureTailgateLocked { value }
    closureSideBinLeftLocked { value }
    closureSideBinRightLocked { value }
  }
}`

type vsValue[T any] struct {
	Value     T      `json:"value"`
	TimeStamp string `json:"timeStamp"`
}

// permissiveString is a JSON scalar that accepts either a string or a
// number (or bool/null) and stores it as a string. Rivian's GraphQL
// schema occasionally reports what used to be a string field as a
// number (remoteChargingAvailable flipped from "true"/"false" to 0/1
// in April 2026). One silently-wrong field stopped decoding the
// entire GetVehicleState response, which left the cache stuck on
// whatever the WS subscription happened to push. Using this type for
// every stringly-typed vehicleState field isolates future flips to a
// single field's semantics instead of blowing up the whole decode.
type permissiveString string

func (p *permissiveString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*p = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*p = permissiveString(s)
		return nil
	}
	// Numbers, bools, everything else: store as the raw literal.
	*p = permissiveString(string(b))
	return nil
}

type vehicleStateData struct {
	VehicleState struct {
		GNSSLocation struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			TimeStamp string  `json:"timeStamp"`
		} `json:"gnssLocation"`
		GNSSSpeed                       vsValue[float64]          `json:"gnssSpeed"`
		GNSSBearing                     vsValue[float64]          `json:"gnssBearing"`
		GNSSAltitude                    vsValue[float64]          `json:"gnssAltitude"`
		BatteryLevel                    vsValue[float64]          `json:"batteryLevel"`
		DistanceToEmpty                 vsValue[float64]          `json:"distanceToEmpty"`
		VehicleMileage                  vsValue[float64]          `json:"vehicleMileage"`
		GearStatus                      vsValue[permissiveString] `json:"gearStatus"`
		DriveMode                       vsValue[permissiveString] `json:"driveMode"`
		ChargerState                    vsValue[permissiveString] `json:"chargerState"`
		ChargerStatus                   vsValue[permissiveString] `json:"chargerStatus"`
		BatteryLimit                    vsValue[float64]          `json:"batteryLimit"`
		ChargePortState                 vsValue[permissiveString] `json:"chargePortState"`
		RemoteChargingAvailable         vsValue[permissiveString] `json:"remoteChargingAvailable"`
		CabinClimateInteriorTemperature vsValue[float64]          `json:"cabinClimateInteriorTemperature"`
		CabinPreconditioningStatus      vsValue[permissiveString] `json:"cabinPreconditioningStatus"`
		PowerState                      vsValue[permissiveString] `json:"powerState"`
		AlarmSoundStatus                vsValue[permissiveString] `json:"alarmSoundStatus"`
		TwelveVoltBatteryHealth         vsValue[permissiveString] `json:"twelveVoltBatteryHealth"`
		WiperFluidState                 vsValue[permissiveString] `json:"wiperFluidState"`
		OtaCurrentVersion               vsValue[permissiveString] `json:"otaCurrentVersion"`
		OtaAvailableVersion             vsValue[permissiveString] `json:"otaAvailableVersion"`
		OtaStatus                       vsValue[permissiveString] `json:"otaStatus"`
		OtaInstallProgress              vsValue[float64]          `json:"otaInstallProgress"`
		TirePressureFrontLeft           vsValue[float64]          `json:"tirePressureFrontLeft"`
		TirePressureFrontRight          vsValue[float64]          `json:"tirePressureFrontRight"`
		TirePressureRearLeft            vsValue[float64]          `json:"tirePressureRearLeft"`
		TirePressureRearRight           vsValue[float64]          `json:"tirePressureRearRight"`
		TirePressureStatusFrontLeft     vsValue[permissiveString] `json:"tirePressureStatusFrontLeft"`
		TirePressureStatusFrontRight    vsValue[permissiveString] `json:"tirePressureStatusFrontRight"`
		TirePressureStatusRearLeft      vsValue[permissiveString] `json:"tirePressureStatusRearLeft"`
		TirePressureStatusRearRight     vsValue[permissiveString] `json:"tirePressureStatusRearRight"`
		// Closures: "open" | "closed" | "".
		DoorFrontLeftClosed   vsValue[permissiveString] `json:"doorFrontLeftClosed"`
		DoorFrontRightClosed  vsValue[permissiveString] `json:"doorFrontRightClosed"`
		DoorRearLeftClosed    vsValue[permissiveString] `json:"doorRearLeftClosed"`
		DoorRearRightClosed   vsValue[permissiveString] `json:"doorRearRightClosed"`
		ClosureFrunkClosed    vsValue[permissiveString] `json:"closureFrunkClosed"`
		ClosureLiftgateClosed vsValue[permissiveString] `json:"closureLiftgateClosed"`
		ClosureTailgateClosed vsValue[permissiveString] `json:"closureTailgateClosed"`
		ClosureTonneauClosed  vsValue[permissiveString] `json:"closureTonneauClosed"`
		// Locks: "locked" | "unlocked" | "". Per home-assistant-rivian
		// LOCK_STATE_ENTITIES, the car is locked iff none of these
		// report "unlocked"; R1T/R1S return different subsets.
		DoorFrontLeftLocked       vsValue[permissiveString] `json:"doorFrontLeftLocked"`
		DoorFrontRightLocked      vsValue[permissiveString] `json:"doorFrontRightLocked"`
		DoorRearLeftLocked        vsValue[permissiveString] `json:"doorRearLeftLocked"`
		DoorRearRightLocked       vsValue[permissiveString] `json:"doorRearRightLocked"`
		ClosureFrunkLocked        vsValue[permissiveString] `json:"closureFrunkLocked"`
		ClosureLiftgateLocked     vsValue[permissiveString] `json:"closureLiftgateLocked"`
		ClosureTonneauLocked      vsValue[permissiveString] `json:"closureTonneauLocked"`
		ClosureTailgateLocked     vsValue[permissiveString] `json:"closureTailgateLocked"`
		ClosureSideBinLeftLocked  vsValue[permissiveString] `json:"closureSideBinLeftLocked"`
		ClosureSideBinRightLocked vsValue[permissiveString] `json:"closureSideBinRightLocked"`
	} `json:"vehicleState"`
}

// StateRaw returns the decoded vehicleState object from Rivian as
// generic JSON for debugging. Used by /api/state/:id/debug to verify
// which fields Rivian actually populates for a given vehicle.
func (c *LiveClient) StateRaw(ctx context.Context, vehicleID string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, errors.New("rivian: not authenticated; call Login first")
	}
	if vehicleID == "" {
		return nil, errors.New("rivian: vehicleID is required")
	}
	data, err := doGraphQL[map[string]any](ctx, c, graphQLRequest{
		OperationName: "GetVehicleState",
		Query:         qVehicleState,
		Variables:     map[string]any{"vehicleID": vehicleID},
	}, c.authHeaders())
	if err != nil {
		return nil, err
	}
	return data, nil
}

// DefaultChargingEndpoint is Rivian's separate GraphQL endpoint for
// charging-session data. Gated on the same auth tokens as the main
// gateway but served under /chrg/user/graphql. Hosts
// getLiveSessionData and getRegisteredWallboxes.
const DefaultChargingEndpoint = "https://rivian.com/api/gql/chrg/user/graphql"

const qLiveSession = `query getLiveSessionHistory($vehicleId: ID!) {
  getLiveSessionHistory(vehicleId: $vehicleId) {
    __typename
    chargerId
    currentCurrency
    currentPrice
    isFreeSession
    isRivianCharger
    locationId
    startTime
    timeElapsed
    current { __typename value updatedAt }
    currentMiles { __typename value updatedAt }
    kilometersChargedPerHour { __typename value updatedAt }
    power { __typename value updatedAt }
    rangeAddedThisSession { __typename value updatedAt }
    soc { __typename value updatedAt }
    timeRemaining { __typename value updatedAt }
    totalChargedEnergy { __typename value updatedAt }
    vehicleChargerState { __typename value updatedAt }
  }
}`

// valueRecord matches the __typename/value/updatedAt envelope Rivian
// wraps most live-session scalars in. T handles both FloatValueRecord
// (value is float64) and StringValueRecord (string).
type valueRecord[T any] struct {
	Value     T      `json:"value"`
	UpdatedAt string `json:"updatedAt"`
}

type liveSessionData struct {
	GetLiveSessionHistory struct {
		ChargerId                *string              `json:"chargerId"`
		CurrentCurrency          *string              `json:"currentCurrency"`
		CurrentPrice             *string              `json:"currentPrice"`
		IsFreeSession            *bool                `json:"isFreeSession"`
		IsRivianCharger          *bool                `json:"isRivianCharger"`
		LocationId               *string              `json:"locationId"`
		StartTime                *string              `json:"startTime"`
		TimeElapsed              *string              `json:"timeElapsed"`
		Current                  valueRecord[float64] `json:"current"`
		CurrentMiles             valueRecord[float64] `json:"currentMiles"`
		KilometersChargedPerHour valueRecord[float64] `json:"kilometersChargedPerHour"`
		Power                    valueRecord[float64] `json:"power"`
		RangeAddedThisSession    valueRecord[float64] `json:"rangeAddedThisSession"`
		Soc                      valueRecord[float64] `json:"soc"`
		TimeRemaining            valueRecord[string]  `json:"timeRemaining"`
		TotalChargedEnergy       valueRecord[float64] `json:"totalChargedEnergy"`
		VehicleChargerState      valueRecord[string]  `json:"vehicleChargerState"`
	} `json:"getLiveSessionHistory"`
}

// LiveSession returns the in-progress charging session for vehicleID,
// or a zero/inactive LiveSession when no session exists. The server
// still returns a 200 with most fields nulled when nothing is
// plugged in; we treat that as inactive.
func (c *LiveClient) LiveSession(ctx context.Context, vehicleID string) (*LiveSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, errors.New("rivian: not authenticated; call Login first")
	}
	if vehicleID == "" {
		return nil, errors.New("rivian: vehicleID is required")
	}
	data, err := doGraphQLAt[liveSessionData](ctx, c, DefaultChargingEndpoint, graphQLRequest{
		OperationName: "getLiveSessionHistory",
		Query:         qLiveSession,
		Variables:     map[string]any{"vehicleId": vehicleID},
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("getLiveSessionHistory: %w", err)
	}
	d := data.GetLiveSessionHistory
	// vehicleChargerState drives the "active" flag. Rivian reports
	// "charging_active" while energy is flowing and other values
	// ("charging_complete", "charging_ready") at session boundaries —
	// home-assistant-rivian treats charging_active + charging_connecting
	// as "on". Anything else (empty, complete, disconnected) → inactive.
	cs := strings.ToLower(strings.TrimSpace(d.VehicleChargerState.Value))
	active := cs == "charging_active" || cs == "charging_connecting"

	out := &LiveSession{
		At:                       time.Now().UTC(),
		VehicleID:                vehicleID,
		Active:                   active,
		VehicleChargerState:      d.VehicleChargerState.Value,
		PowerKW:                  d.Power.Value,
		KilometersChargedPerHour: d.KilometersChargedPerHour.Value,
		RangeAddedKm:             d.RangeAddedThisSession.Value,
		TotalChargedEnergyKWh:    d.TotalChargedEnergy.Value,
		SoCPct:                   d.Soc.Value,
	}
	if d.StartTime != nil {
		out.StartTime = *d.StartTime
	}
	if d.TimeElapsed != nil {
		// TimeElapsed is a stringified seconds count in both
		// upstream clients. Not worth decoding for the UI today;
		// we pass it through and parse at the API layer if needed.
		if n, convErr := parseSecondsString(*d.TimeElapsed); convErr == nil {
			out.TimeElapsedSeconds = n
		}
	}
	if n, convErr := parseSecondsString(d.TimeRemaining.Value); convErr == nil {
		out.TimeRemainingSeconds = n
	}
	if d.CurrentPrice != nil {
		out.CurrentPrice = *d.CurrentPrice
	}
	if d.CurrentCurrency != nil {
		out.CurrentCurrency = *d.CurrentCurrency
	}
	if d.IsFreeSession != nil {
		out.IsFreeSession = *d.IsFreeSession
	}
	if d.IsRivianCharger != nil {
		out.IsRivianCharger = *d.IsRivianCharger
	}
	return out, nil
}

func parseSecondsString(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// ChargingSchemaProbe runs an introspection query against the
// charging GraphQL endpoint to discover which top-level fields exist
// and which arguments they accept. Used to recover after Rivian
// renames/removes a field (as happened when getLiveSessionData was
// replaced with getSessionStatus/getLiveSessionHistory).
//
// Returns the raw `__schema.queryType.fields` array so callers can
// introspect field names and arg shapes.
func (c *LiveClient) ChargingSchemaProbe(ctx context.Context) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, errors.New("rivian: not authenticated; call Login first")
	}
	const q = `query __Introspect {
  __schema {
    queryType {
      fields {
        name
        args { name type { name kind ofType { name kind } } }
        type { name kind ofType { name kind } }
      }
    }
  }
}`
	data, err := doGraphQLAt[map[string]any](ctx, c, DefaultChargingEndpoint, graphQLRequest{
		OperationName: "__Introspect",
		Query:         q,
	}, c.authHeaders())
	if err != nil {
		return nil, err
	}
	return data, nil
}

// ChargingFieldProbe fires a deliberately malformed query for the
// named top-level field against the charging endpoint.
func (c *LiveClient) ChargingFieldProbe(ctx context.Context, field, vehicleID string) (map[string]any, error) {
	selection := ""
	return c.ChargingFieldProbeWithSelection(ctx, field, vehicleID, selection)
}

// ChargingFieldProbeWithSelection probes a top-level field and
// supplies a selection set so the server's validator proceeds to
// verify subfields. When selection is empty we emit an empty
// selection { __typename } and rely on arg-validation errors
// instead.
func (c *LiveClient) ChargingFieldProbeWithSelection(ctx context.Context, field, vehicleID, selection string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, errors.New("rivian: not authenticated; call Login first")
	}
	if field == "" {
		return nil, errors.New("field required")
	}
	sel := selection
	if sel == "" {
		sel = "__typename"
	}
	q := fmt.Sprintf(`query Probe { %s { %s } }`, field, sel)
	vars := map[string]any{}
	if vehicleID != "" {
		q = fmt.Sprintf(`query Probe($vehicleId: ID!) { %s(vehicleId: $vehicleId) { %s } }`, field, sel)
		vars["vehicleId"] = vehicleID
	}
	// Use a raw POST so we surface the error body instead of
	// failing out in doGraphQLAt's HTTP 400 handler.
	body, _ := json.Marshal(graphQLRequest{OperationName: "Probe", Query: q, Variables: vars})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, DefaultChargingEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("apollographql-client-name", c.clientName)
	httpReq.Header.Set("User-Agent", "rivolt/0.1 (+https://github.com/apohor/rivolt)")
	for k, v := range c.authHeaders() {
		if v != "" {
			httpReq.Header.Set(k, v)
		}
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	var out map[string]any
	if jerr := json.Unmarshal(raw, &out); jerr != nil {
		return map[string]any{"status": resp.StatusCode, "raw": string(raw)}, nil
	}
	out["_status"] = resp.StatusCode
	return out, nil
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
	// ps converts a permissiveString (which carries a possibly-numeric
	// or -boolean scalar as text) to the string the State struct and
	// helper functions expect.
	ps := func(s permissiveString) string { return string(s) }
	return &State{
		At:              at,
		VehicleID:       vehicleID,
		BatteryLevelPct: vs.BatteryLevel.Value,
		DistanceToEmpty: vs.DistanceToEmpty.Value,
		// vehicleMileage is reported in METERS despite what the old
		// comment above claimed — confirmed on a real account the
		// field comes back as ~5.7e7 for a ~35k-mile vehicle.
		OdometerKm:   vs.VehicleMileage.Value / 1000,
		Gear:         normalizeGear(ps(vs.GearStatus.Value)),
		DriveMode:    ps(vs.DriveMode.Value),
		ChargerState: ps(vs.ChargerState.Value),
		// ChargerPowerKW: the GetVehicleState schema no longer
		// exposes a live-power field. Kilowatts are available via
		// getLiveSessionData (chrg/user/graphql) — wire that in a
		// follow-up when we render a live charging panel.
		ChargerPowerKW:          0,
		ChargeTargetPct:         vs.BatteryLimit.Value,
		ChargerStatus:           ps(vs.ChargerStatus.Value),
		ChargePortState:         ps(vs.ChargePortState.Value),
		RemoteChargingAvailable: ps(vs.RemoteChargingAvailable.Value),
		Latitude:                vs.GNSSLocation.Latitude,
		Longitude:               vs.GNSSLocation.Longitude,
		// gnssSpeed is reported in meters-per-second (standard GNSS), not
		// kph — the field name on our State struct predates the discovery.
		// Convert at the boundary so downstream conversions (kphToMi etc.)
		// produce correct mph. Without this, a 50 mph drive was logging ~15
		// mph max speed because 22.35 m/s was being treated as kph.
		SpeedKph:   vs.GNSSSpeed.Value * 3.6,
		HeadingDeg: vs.GNSSBearing.Value,
		AltitudeM:  vs.GNSSAltitude.Value,
		Locked: aggregateLocked(
			ps(vs.DoorFrontLeftLocked.Value),
			ps(vs.DoorFrontRightLocked.Value),
			ps(vs.DoorRearLeftLocked.Value),
			ps(vs.DoorRearRightLocked.Value),
			ps(vs.ClosureFrunkLocked.Value),
			ps(vs.ClosureLiftgateLocked.Value),
			ps(vs.ClosureTonneauLocked.Value),
			ps(vs.ClosureTailgateLocked.Value),
			ps(vs.ClosureSideBinLeftLocked.Value),
			ps(vs.ClosureSideBinRightLocked.Value),
		),
		DoorsClosed: aggregateClosed(
			ps(vs.DoorFrontLeftClosed.Value),
			ps(vs.DoorFrontRightClosed.Value),
			ps(vs.DoorRearLeftClosed.Value),
			ps(vs.DoorRearRightClosed.Value),
		),
		FrunkClosed:                isClosed(ps(vs.ClosureFrunkClosed.Value)),
		LiftgateClosed:             isClosed(ps(vs.ClosureLiftgateClosed.Value)),
		TailgateClosed:             isClosed(ps(vs.ClosureTailgateClosed.Value)),
		TonneauClosed:              isClosed(ps(vs.ClosureTonneauClosed.Value)),
		CabinTempC:                 vs.CabinClimateInteriorTemperature.Value,
		OutsideTempC:               0,
		CabinPreconditioningStatus: ps(vs.CabinPreconditioningStatus.Value),
		PowerState:                 strings.ToLower(strings.TrimSpace(ps(vs.PowerState.Value))),
		AlarmSoundStatus:           ps(vs.AlarmSoundStatus.Value),
		TwelveVoltBatteryHealth:    ps(vs.TwelveVoltBatteryHealth.Value),
		WiperFluidState:            ps(vs.WiperFluidState.Value),
		OtaCurrentVersion:          ps(vs.OtaCurrentVersion.Value),
		OtaAvailableVersion:        ps(vs.OtaAvailableVersion.Value),
		OtaStatus:                  ps(vs.OtaStatus.Value),
		OtaInstallProgress:         vs.OtaInstallProgress.Value,
		TirePressureFLBar:          vs.TirePressureFrontLeft.Value,
		TirePressureFRBar:          vs.TirePressureFrontRight.Value,
		TirePressureRLBar:          vs.TirePressureRearLeft.Value,
		TirePressureRRBar:          vs.TirePressureRearRight.Value,
		TirePressureStatusFL:       ps(vs.TirePressureStatusFrontLeft.Value),
		TirePressureStatusFR:       ps(vs.TirePressureStatusFrontRight.Value),
		TirePressureStatusRL:       ps(vs.TirePressureStatusRearLeft.Value),
		TirePressureStatusRR:       ps(vs.TirePressureStatusRearRight.Value),
	}, nil
}

// aggregateLocked follows home-assistant-rivian's LOCK_STATE_ENTITIES
// convention: the car is locked iff none of the per-door/closure
// values equals "unlocked" (case-insensitive). Empty values — which
// the gateway emits for closures a given trim doesn't have — are
// ignored, and an all-empty response is treated as unknown→locked so
// we don't falsely claim the car is wide open.
func aggregateLocked(vs ...string) bool {
	for _, v := range vs {
		if strings.EqualFold(strings.TrimSpace(v), "unlocked") {
			return false
		}
	}
	return true
}

// aggregateClosed is the mirror for closure/door booleans: all are
// closed iff none of the per-panel values equals "open".
func aggregateClosed(vs ...string) bool {
	for _, v := range vs {
		if strings.EqualFold(strings.TrimSpace(v), "open") {
			return false
		}
	}
	return true
}

// isClosed handles a single closure field; empty string → closed
// (the trim doesn't have that panel, so we can't meaningfully show
// it as "open").
func isClosed(v string) bool {
	return !strings.EqualFold(strings.TrimSpace(v), "open")
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
