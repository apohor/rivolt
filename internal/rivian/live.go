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
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// DefaultEndpoint is the Rivian Owner App GraphQL gateway. Unofficial.
const DefaultEndpoint = "https://rivian.com/api/gql/gateway/graphql"

// DefaultClientName identifies us as the Android Owner App. The
// iOS client name triggers server-side @defer of gnssLocation on
// the WS subscription, which our single-shot frame parser drops.
const DefaultClientName = "com.rivian.android.consumer"

// DefaultClientVersion is paired with DefaultClientName; keep them
// in sync to avoid a never-shipped hybrid fingerprint.
const DefaultClientVersion = "3.6.0-3989"

// DefaultUserAgent matches the iOS app verbatim; the gateway 403s on modified UAs.
const DefaultUserAgent = "RivianApp/4400 CFNetwork/1498.700.2 Darwin/23.6.0"

// DefaultAccept stays application/json. The iOS multipart/mixed value
// makes the gateway @defer fields like gnssLocation into separate parts
// our single-shot json.Unmarshal can't reassemble.
const DefaultAccept = "application/json"

// applyBaseHeaders sets the headers every Rivian-gateway request needs.
// Auth (a-sess / u-sess / csrf-token) layered on top via extraHeaders.
// Accept-Encoding is intentionally unset so net/http handles gzip.
func (c *LiveClient) applyBaseHeaders(h http.Header) {
	h.Set("Content-Type", "application/json")
	h.Set("Accept", DefaultAccept)
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("User-Agent", DefaultUserAgent)
	h.Set("apollographql-client-name", c.clientName)
	h.Set("apollographql-client-version", c.clientVersion)
	h.Set("X-Rivolt-Version", c.rivoltVersion)
}

// ErrMFARequired signals an OTP challenge; resubmit Login on the same
// client with Credentials.OTP populated.
var ErrMFARequired = errors.New("rivian: MFA code required")

// LiveClient talks to the real Rivian Owner App GraphQL gateway.
// All exported methods are safe to call concurrently.
type LiveClient struct {
	httpClient    *http.Client
	endpoint      string
	clientName    string
	clientVersion string
	// rivoltVersion is stamped into X-Rivolt-Version. Defaults to "dev".
	rivoltVersion string

	// upstreamGate, when non-nil, short-circuits outbound calls with its error.
	upstreamGate func(ctx context.Context) error

	// breaker, when non-nil, observes every classified outcome and
	// can short-circuit calls before they hit the network. Distinct
	// from upstreamGate so the kill switch (operator-driven) and
	// the breaker (failure-driven) compose without one needing to
	// know about the other.
	breaker *Breaker

	// reauthSink fires on classified UserAction errors so callers can
	// persist users.needs_reauth. Best-effort; failures are not surfaced.
	reauthSink func(ctx context.Context, reason string)

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

	// reauthState is an atomic mirror of needs_reauth read on every outbound
	// call; lock-free so doGraphQL can flip it without deadlocking c.mu.
	reauthState atomic.Pointer[reauthSnapshot]

	// Ring buffer of recent ChargingSession WS frames for debugging.
	framesMu     sync.Mutex
	recentFrames []ChargingFrame
	maxFrames    int

	// Shared WS multiplexer; lazily dialled, torn down on last release.
	muxMu sync.Mutex
	mux   *wsMux
}

// ChargingFrame is one raw ChargingSession push or lifecycle event.
// Event is set for lifecycle markers ("open", "error", "close");
// Raw carries the JSON payload for push frames.
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

// NewLive returns a LiveClient with sane defaults.
func NewLive() *LiveClient {
	return &LiveClient{
		// otelhttp.NewTransport produces no spans when tracing is
		// disabled (no-op TracerProvider) and a CHILD span of the
		// inbound request when it's enabled — so the trace
		// rivolt → Rivian gateway is one continuous tree in Tempo.
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
		endpoint:      DefaultEndpoint,
		clientName:    DefaultClientName,
		clientVersion: DefaultClientVersion,
		rivoltVersion: "dev",
	}
}

// WithRivoltVersion stamps the build version into X-Rivolt-Version.
func (c *LiveClient) WithRivoltVersion(v string) *LiveClient {
	v = strings.TrimSpace(v)
	if v == "" {
		v = "dev"
	}
	c.rivoltVersion = v
	return c
}

// ErrUpstreamPaused is returned when the operator kill switch is on.
var ErrUpstreamPaused = errors.New("rivian: upstream paused by operator")

// ErrNeedsReauth is returned when the per-user needs_reauth flag is set.
// Cleared on a successful Login.
var ErrNeedsReauth = errors.New("rivian: re-authentication required")

// WithUpstreamGate installs a pre-flight hook that short-circuits outbound
// calls when it returns non-nil. Pass nil to remove.
func (c *LiveClient) WithUpstreamGate(gate func(ctx context.Context) error) *LiveClient {
	c.upstreamGate = gate
	return c
}

// WithBreaker attaches a circuit breaker. Every classified outcome
// from doGraphQLAt is reported to b; when b is open, checkUpstream
// returns ErrUpstreamBreakerOpen before we hit the network.
func (c *LiveClient) WithBreaker(b *Breaker) *LiveClient {
	c.breaker = b
	return c
}

// WithReauthSink installs a callback fired on ClassUserAction errors.
func (c *LiveClient) WithReauthSink(sink func(ctx context.Context, reason string)) *LiveClient {
	c.reauthSink = sink
	return c
}

// reauthSnapshot is the immutable value behind LiveClient.reauthState.
type reauthSnapshot struct {
	needs  bool
	reason string
}

// NeedsReauth reports the in-memory needs_reauth flag and reason.
func (c *LiveClient) NeedsReauth() (bool, string) {
	s := c.reauthState.Load()
	if s == nil {
		return false, ""
	}
	return s.needs, s.reason
}

// SetNeedsReauth sets the flag without firing the sink. Used to hydrate
// from storage at startup; runtime classification goes through doGraphQLAt.
func (c *LiveClient) SetNeedsReauth(needs bool, reason string) {
	if !needs {
		c.reauthState.Store(&reauthSnapshot{})
		return
	}
	c.reauthState.Store(&reauthSnapshot{needs: true, reason: reason})
}

// checkUpstream runs the configured gate; nil gate allows.
func (c *LiveClient) checkUpstream(ctx context.Context) error {
	// Per-user re-auth takes precedence over the global gate.
	if s := c.reauthState.Load(); s != nil && s.needs {
		return ErrNeedsReauth
	}
	// Operator kill switch beats failure-driven backpressure: if the
	// human told us to stop, stop. The breaker only adds an extra
	// gate on top.
	if c.upstreamGate != nil {
		if err := c.upstreamGate(ctx); err != nil {
			return err
		}
	}
	if c.breaker != nil {
		if err := c.breaker.Allow(ctx); err != nil {
			return err
		}
	}
	return nil
}

// markNeedsReauth publishes the flag and fires the sink on the rising edge.
func (c *LiveClient) markNeedsReauth(ctx context.Context, reason string) {
	next := &reauthSnapshot{needs: true, reason: reason}
	prev := c.reauthState.Swap(next)
	if prev != nil && prev.needs {
		return
	}
	if c.reauthSink != nil {
		c.reauthSink(ctx, reason)
	}
}

// clearNeedsReauth resets the flag on a successful Login.
func (c *LiveClient) clearNeedsReauth(ctx context.Context) {
	prev := c.reauthState.Swap(&reauthSnapshot{})
	if prev == nil || !prev.needs {
		return
	}
	if c.reauthSink != nil {
		// Empty reason signals "clear the flag" to the sink.
		c.reauthSink(ctx, "")
	}
}

// WithEndpoint points the client at an alternate GraphQL URL.
func (c *LiveClient) WithEndpoint(url string) *LiveClient {
	c.endpoint = url
	return c
}

// WithHTTPClient installs a custom *http.Client.
func (c *LiveClient) WithHTTPClient(h *http.Client) *LiveClient {
	c.httpClient = h
	return c
}

// graphQLRequest is the request body the gateway expects.
type graphQLRequest struct {
	OperationName string `json:"operationName"`
	Query         string `json:"query"`
	Variables     any    `json:"variables"`
}

// graphQLError is one entry from a failed GraphQL response.
type graphQLError struct {
	Message    string   `json:"message"`
	Path       []string `json:"path,omitempty"`
	Extensions struct {
		Code string `json:"code,omitempty"`
	} `json:"extensions"`
}

// graphQLResponse is the outer envelope.
type graphQLResponse[T any] struct {
	Data   T              `json:"data"`
	Errors []graphQLError `json:"errors,omitempty"`
}

// doGraphQL posts to c.endpoint; extraHeaders layer on top of the base set.
func doGraphQL[T any](ctx context.Context, c *LiveClient, req graphQLRequest, extraHeaders map[string]string) (T, error) {
	return doGraphQLAt[T](ctx, c, c.endpoint, req, extraHeaders)
}

// doGraphQLAt is doGraphQL targeted at an arbitrary URL (e.g. the charging endpoint).
func doGraphQLAt[T any](ctx context.Context, c *LiveClient, url string, req graphQLRequest, extraHeaders map[string]string) (result T, err error) {
	var zero T
	if err := c.checkUpstream(ctx); err != nil {
		return zero, err
	}

	// Wrap the call in a span named after the GraphQL operation so
	// Tempo shows "rivian.<Op>" in the trace tree instead of just
	// "HTTP POST". The otelhttp.Transport on c.httpClient still
	// produces a child HTTP span underneath this one — useful when
	// we eventually want to see TLS / connect timing separately.
	ctx, span := otel.Tracer("rivolt/rivian").Start(ctx, "rivian."+req.OperationName,
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("graphql.operation.name", req.OperationName),
			attribute.String("graphql.endpoint", url),
		),
	)
	defer func() {
		// Mark the span as errored when the call returns an
		// *UpstreamError so Tempo highlights the failed branch and
		// stamps the error class as a queryable attribute.
		if err != nil {
			var ue *UpstreamError
			if errors.As(err, &ue) {
				span.SetAttributes(
					attribute.String("rivian.error.class", ue.Class.String()),
					attribute.Int("rivian.http.status", ue.HTTPStatus),
				)
				if c.breaker != nil {
					c.breaker.Observe(ue.Class)
				}
			}
			span.RecordError(err)
		} else if c.breaker != nil {
			// Successful 2xx with no GraphQL errors: closes the
			// breaker if it was half-open.
			c.breaker.ObserveSuccess()
		}
		span.End()
	}()

	body, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("marshal %s: %w", req.OperationName, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("build request: %w", err)
	}
	c.applyBaseHeaders(httpReq.Header)
	for k, v := range extraHeaders {
		if v != "" {
			httpReq.Header.Set(k, v)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		class, reason := ClassifyNetwork(err)
		ue := &UpstreamError{Class: class, Op: req.OperationName, Reason: reason, Cause: err}
		if class == ClassUserAction {
			c.markNeedsReauth(ctx, reason)
		}
		return zero, ue
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return zero, &UpstreamError{
			Class:      ClassTransient,
			Op:         req.OperationName,
			HTTPStatus: resp.StatusCode,
			Reason:     "read body",
			Cause:      err,
		}
	}
	if resp.StatusCode >= 400 {
		class, reason := ClassifyHTTP(resp.StatusCode, string(raw))
		ue := &UpstreamError{
			Class:      class,
			Op:         req.OperationName,
			HTTPStatus: resp.StatusCode,
			Reason:     reason,
			Cause:      fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 256)),
		}
		if class == ClassUserAction {
			c.markNeedsReauth(ctx, reason)
		}
		return zero, ue
	}

	var out graphQLResponse[T]
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, &UpstreamError{
			Class:  ClassTransient,
			Op:     req.OperationName,
			Reason: "decode",
			Cause:  fmt.Errorf("%w: %s", err, truncate(string(raw), 256)),
		}
	}
	if len(out.Errors) > 0 {
		first := out.Errors[0]
		class, reason := ClassifyGraphQL(first.Extensions.Code, first.Message)
		msgs := make([]string, 0, len(out.Errors))
		for _, e := range out.Errors {
			msgs = append(msgs, e.Message)
		}
		ue := &UpstreamError{
			Class:   class,
			Op:      req.OperationName,
			ExtCode: first.Extensions.Code,
			Reason:  reason,
			Cause:   fmt.Errorf("%s", strings.Join(msgs, "; ")),
		}
		if class == ClassUserAction {
			c.markNeedsReauth(ctx, reason)
		}
		return zero, ue
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

// ensureCSRF populates csrfToken/appSessionToken if missing. Caller holds c.mu.
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

	// Second-step: caller already saw ErrMFARequired, now submitting OTP.
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
		c.clearNeedsReauth(ctx)
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
		c.clearNeedsReauth(ctx)
		return nil
	case "MobileMFALoginResponse":
		c.pendingOTPToken = data.Login.OTPToken
		c.pendingOTPEmail = creds.Email
		return ErrMFARequired
	default:
		return fmt.Errorf("Login: unexpected response __typename %q", data.Login.Typename)
	}
}

// authHeaders builds the a-sess / u-sess / csrf-token triple. Caller holds c.mu.
func (c *LiveClient) authHeaders() map[string]string {
	return map[string]string{
		"a-sess":     c.appSessionToken,
		"u-sess":     c.userSessionToken,
		"csrf-token": c.csrfToken,
	}
}

// ----- Data queries --------------------------------------------------

// qUser is a trimmed getUserInfo selecting only the fields Rivolt uses.
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

// Vehicles lists the vehicles on the authenticated account.
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

// qVehicleState pulls the dashboard subset of vehicleState (~50 fields).
// Field names from home-assistant-rivian's entity map.
const qVehicleState = `query GetVehicleState($vehicleID: String!) {
  vehicleState(id: $vehicleID) {
    __typename
    gnssLocation { latitude longitude timeStamp }
    gnssSpeed { value }
    gnssBearing { value }
    gnssAltitude { value }
    batteryLevel { value }
    batteryCapacity { value }
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

// permissiveString accepts string|number|bool|null and stores it as text,
// so a single Rivian schema flip can't blow up the whole decode.
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
		BatteryCapacity                 vsValue[float64]          `json:"batteryCapacity"`
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
		// Locks: "locked" | "unlocked" | "". Locked iff none report "unlocked".
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

// StateRaw returns the decoded vehicleState as generic JSON for debugging.
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

// DefaultChargingEndpoint hosts getLiveSessionHistory and friends.
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

// valueRecord wraps the {value, updatedAt} envelope on live-session scalars.
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

// LiveSession returns the in-progress charging session, or an inactive one.
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
	// Active iff charger state is charging_active or charging_connecting
	// (matches home-assistant-rivian).
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
		// Stringified seconds count.
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

// ChargingSchemaProbe runs an introspection query against the charging
// endpoint, returning __schema.queryType.fields for diagnostics.
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

// ChargingFieldProbe fires a deliberately malformed query for diagnostics.
func (c *LiveClient) ChargingFieldProbe(ctx context.Context, field, vehicleID string) (map[string]any, error) {
	return c.ChargingFieldProbeWithSelection(ctx, field, vehicleID, "")
}

// ChargingFieldProbeWithSelection probes a field with a custom selection set.
func (c *LiveClient) ChargingFieldProbeWithSelection(ctx context.Context, field, vehicleID, selection string) (map[string]any, error) {
	if err := c.checkUpstream(ctx); err != nil {
		return nil, err
	}
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
	// Raw POST so error bodies surface instead of being swallowed by doGraphQLAt.
	body, _ := json.Marshal(graphQLRequest{OperationName: "Probe", Query: q, Variables: vars})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, DefaultChargingEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.applyBaseHeaders(httpReq.Header)
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

// State returns the current vehicle snapshot. Battery %, distances km, temps °C.
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
	ps := func(s permissiveString) string { return string(s) }
	return &State{
		At:                 at,
		VehicleID:          vehicleID,
		BatteryLevelPct:    vs.BatteryLevel.Value,
		BatteryCapacityKWh: vs.BatteryCapacity.Value,
		DistanceToEmpty:    vs.DistanceToEmpty.Value,
		// vehicleMileage is in meters.
		OdometerKm:   vs.VehicleMileage.Value / 1000,
		Gear:         normalizeGear(ps(vs.GearStatus.Value)),
		DriveMode:    ps(vs.DriveMode.Value),
		ChargerState: ps(vs.ChargerState.Value),
		// Live kW is on the charging endpoint, not vehicleState.
		ChargerPowerKW:          0,
		ChargeTargetPct:         vs.BatteryLimit.Value,
		ChargerStatus:           ps(vs.ChargerStatus.Value),
		ChargePortState:         ps(vs.ChargePortState.Value),
		RemoteChargingAvailable: ps(vs.RemoteChargingAvailable.Value),
		Latitude:                vs.GNSSLocation.Latitude,
		Longitude:               vs.GNSSLocation.Longitude,
		// gnssSpeed is m/s; convert to kph at the boundary.
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

// aggregateLocked: locked iff none of vs equals "unlocked". All-empty → locked.
func aggregateLocked(vs ...string) bool {
	for _, v := range vs {
		if strings.EqualFold(strings.TrimSpace(v), "unlocked") {
			return false
		}
	}
	return true
}

// aggregateClosed: closed iff none of vs equals "open".
func aggregateClosed(vs ...string) bool {
	for _, v := range vs {
		if strings.EqualFold(strings.TrimSpace(v), "open") {
			return false
		}
	}
	return true
}

// isClosed: empty → closed (trim lacks that panel).
func isClosed(v string) bool {
	return !strings.EqualFold(strings.TrimSpace(v), "open")
}

// normalizeGear maps gearStatus to the P/D/R/N contract.
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

// Session is the persistable subset of LiveClient state.
type Session struct {
	Email            string    `json:"email"`
	CSRFToken        string    `json:"csrf_token"`
	AppSessionToken  string    `json:"app_session_token"`
	UserSessionToken string    `json:"user_session_token"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AuthenticatedAt  time.Time `json:"authenticated_at"`
}

// Snapshot returns the current Session, or zero if not authenticated.
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

// Restore hydrates the client from a Snapshot. No network I/O.
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

// Authenticated reports whether a userSessionToken is set locally.
func (c *LiveClient) Authenticated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userSessionToken != ""
}

// Email returns the authenticated email, or "" if not logged in.
func (c *LiveClient) Email() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.email
}

// MFAPending reports whether Login is awaiting an OTP submission.
func (c *LiveClient) MFAPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingOTPToken != ""
}

// Logout clears session fields but keeps the CSRF token (it survives logout).
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
