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

const qUser = `query user {
  user {
    userId
    email { email }
    firstName
    lastName
    vehicles { id vin __typename }
  }
}`

type userData struct {
	User struct {
		UserID    string `json:"userId"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Email     struct {
			Email string `json:"email"`
		} `json:"email"`
		Vehicles []struct {
			ID  string `json:"id"`
			VIN string `json:"vin"`
		} `json:"vehicles"`
	} `json:"user"`
}

// Vehicles lists the vehicles on the authenticated Rivian account.
// Currently returns id + vin; richer metadata (model, year, name) is a
// follow-up — the CurrentUserForLogin query returns model info but the
// response tree is very heavy and most of it we don't use.
func (c *LiveClient) Vehicles(ctx context.Context) ([]Vehicle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, errors.New("rivian: not authenticated; call Login first")
	}
	data, err := doGraphQL[userData](ctx, c, graphQLRequest{
		OperationName: "user",
		Query:         qUser,
		Variables:     struct{}{},
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("user: %w", err)
	}
	out := make([]Vehicle, 0, len(data.User.Vehicles))
	for _, v := range data.User.Vehicles {
		out = append(out, Vehicle{ID: v.ID, VIN: v.VIN})
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
    chargerPower { value }
    chargerDerateStatus { value }
    chargeTarget { value }
    cabinClimateInteriorTemperature { value }
    cabinClimateExteriorTemperature { value }
    closureFrontLeftLocked { value }
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
		ChargerPower                    vsValue[float64] `json:"chargerPower"`
		ChargeTarget                    vsValue[float64] `json:"chargeTarget"`
		CabinClimateInteriorTemperature vsValue[float64] `json:"cabinClimateInteriorTemperature"`
		CabinClimateExteriorTemperature vsValue[float64] `json:"cabinClimateExteriorTemperature"`
		ClosureFrontLeftLocked          vsValue[string]  `json:"closureFrontLeftLocked"`
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
		Gear:            vs.GearStatus.Value,
		ChargerState:    vs.ChargerState.Value,
		ChargerPowerKW:  vs.ChargerPower.Value,
		ChargeTargetPct: vs.ChargeTarget.Value,
		Latitude:        vs.GNSSLocation.Latitude,
		Longitude:       vs.GNSSLocation.Longitude,
		Locked:          strings.EqualFold(vs.ClosureFrontLeftLocked.Value, "locked"),
		CabinTempC:      vs.CabinClimateInteriorTemperature.Value,
		OutsideTempC:    vs.CabinClimateExteriorTemperature.Value,
	}, nil
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
