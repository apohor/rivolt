package rivian

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// MockClient is a Client implementation backed by in-memory fixture
// data. It exists so the UI can be demoed without Rivian credentials
// and so handler-level tests can drive a predictable, offline Client.
//
// The defaults model a stationary Rivian R1T at ~72% SoC. Callers can
// swap the state out via SetState before serving requests.
//
// MockClient also implements Account — the same sign-in/MFA/logout
// surface LiveClient exposes — so the UI flow under RIVIAN_CLIENT=mock
// is identical to production. Any email + password combo succeeds,
// except:
//
//   - email containing "mfa" triggers an MFA challenge that any
//     non-empty OTP clears; this exercises the two-leg flow locally.
//   - email containing "fail" or "bad" rejects the password so the
//     error state is reachable without tripping real Rivian.
//
// Snapshots round-trip through JSON so settings.SaveRivianSession can
// persist them; on restart Restore brings the mock back to its
// authenticated state, matching the live client's behaviour.
type MockClient struct {
	mu              sync.Mutex
	email           string    // non-empty when authenticated
	pendingOTPEmail string    // non-empty during MFA challenge
	authenticatedAt time.Time // last successful login, for Snapshot
	vehicles        []Vehicle
	states          map[string]State
	LoginReturnErr  error // if non-nil, Login returns this error
}

// NewMock returns a MockClient primed with one fake vehicle. The
// client starts logged-out so the Settings UI shows the sign-in panel
// — call Login with any email+password (or Restore a Session) to
// unlock Vehicles/State.
func NewMock() *MockClient {
	id := "mock-vehicle-1"
	return &MockClient{
		vehicles: []Vehicle{{
			ID:    id,
			VIN:   "7FCTGAAL5NN000000",
			Name:  "Mock R1T",
			Model: "R1T",
		}},
		states: map[string]State{
			id: {
				At:                         time.Now().UTC(),
				VehicleID:                  id,
				BatteryLevelPct:            72,
				DistanceToEmpty:            310,
				OdometerKm:                 24500,
				Gear:                       "P",
				DriveMode:                  "everyday",
				ChargerState:               "charger_disconnected",
				ChargerPowerKW:             0,
				ChargeTargetPct:            80,
				ChargerStatus:              "chrgr_sts_not_connected",
				ChargePortState:            "closed",
				RemoteChargingAvailable:    "true",
				Latitude:                   37.7749,
				Longitude:                  -122.4194,
				SpeedKph:                   0,
				HeadingDeg:                 0,
				AltitudeM:                  52,
				Locked:                     true,
				DoorsClosed:                true,
				FrunkClosed:                true,
				LiftgateClosed:             true,
				TailgateClosed:             true,
				TonneauClosed:              true,
				CabinTempC:                 21,
				OutsideTempC:               18,
				CabinPreconditioningStatus: "inactive",
				PowerState:                 "sleep",
				AlarmSoundStatus:           "false",
				TwelveVoltBatteryHealth:    "good",
				WiperFluidState:            "normal",
				OtaCurrentVersion:          "2025.40.00",
				OtaAvailableVersion:        "2025.40.00",
				OtaStatus:                  "Idle",
				OtaInstallProgress:         0,
				TirePressureFLBar:          2.6,
				TirePressureFRBar:          2.6,
				TirePressureRLBar:          2.55,
				TirePressureRRBar:          2.55,
				TirePressureStatusFL:       "Normal",
				TirePressureStatusFR:       "Normal",
				TirePressureStatusRL:       "Normal",
				TirePressureStatusRR:       "Normal",
			},
		},
	}
}

// SetVehicles overwrites the fixture vehicle list. Useful for tests
// that want to assert list-rendering or multi-vehicle pickers.
func (c *MockClient) SetVehicles(vs []Vehicle) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vehicles = append(c.vehicles[:0:0], vs...)
}

// SetState replaces the fixture state for a vehicle ID. The timestamp
// is always refreshed to time.Now() so sequential polls look "live".
func (c *MockClient) SetState(vehicleID string, s State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.VehicleID = vehicleID
	s.At = time.Now().UTC()
	if c.states == nil {
		c.states = map[string]State{}
	}
	c.states[vehicleID] = s
}

// Login drives the two-leg auth dance. Returns LoginReturnErr if set
// so tests can exercise failure paths without patching transport.
func (c *MockClient) Login(_ context.Context, cr Credentials) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.LoginReturnErr != nil {
		return c.LoginReturnErr
	}
	// Second leg: OTP. Only valid while a challenge is pending.
	if cr.OTP != "" {
		if c.pendingOTPEmail == "" {
			return errors.New("mock: no MFA challenge in flight")
		}
		c.email = c.pendingOTPEmail
		c.pendingOTPEmail = ""
		c.authenticatedAt = time.Now().UTC()
		return nil
	}
	// First leg: email + password.
	email := strings.TrimSpace(cr.Email)
	if email == "" || cr.Password == "" {
		return errors.New("mock: email and password required")
	}
	lowered := strings.ToLower(email)
	if strings.Contains(lowered, "fail") || strings.Contains(lowered, "bad") {
		return errors.New("mock: invalid credentials")
	}
	if strings.Contains(lowered, "mfa") {
		c.pendingOTPEmail = email
		return ErrMFARequired
	}
	c.email = email
	c.pendingOTPEmail = ""
	c.authenticatedAt = time.Now().UTC()
	return nil
}

// Vehicles returns the fixture list. Requires a prior Login call so
// tests that forget auth fail obviously.
func (c *MockClient) Vehicles(_ context.Context) ([]Vehicle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.email == "" {
		return nil, ErrNotAuthenticated
	}
	out := make([]Vehicle, len(c.vehicles))
	copy(out, c.vehicles)
	return out, nil
}

// State returns the fixture snapshot. The timestamp is bumped to now
// on every call so consumers that rely on "recency" work.
func (c *MockClient) State(_ context.Context, vehicleID string) (*State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.email == "" {
		return nil, ErrNotAuthenticated
	}
	s, ok := c.states[vehicleID]
	if !ok {
		return nil, ErrVehicleNotFound
	}
	s.At = time.Now().UTC()
	return &s, nil
}

// Authenticated reports whether the mock client has completed a
// successful Login (or been Restored from a persisted session).
func (c *MockClient) Authenticated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.email != ""
}

// MFAPending reports whether Login returned ErrMFARequired and the
// client is waiting for an OTP submission.
func (c *MockClient) MFAPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingOTPEmail != ""
}

// Email returns the email the current session is authenticated as.
func (c *MockClient) Email() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.email
}

// Logout clears every authenticated-session field.
func (c *MockClient) Logout() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.email = ""
	c.pendingOTPEmail = ""
	c.authenticatedAt = time.Time{}
}

// Snapshot returns a serialisable copy of the current session so
// settings.SaveRivianSession can round-trip it through JSON. The
// mock uses UserSessionToken purely as the "we are logged in"
// sentinel — the contents are not real tokens.
func (c *MockClient) Snapshot() Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.email == "" {
		return Session{}
	}
	return Session{
		Email:            c.email,
		UserSessionToken: "mock-session",
		AuthenticatedAt:  c.authenticatedAt,
	}
}

// Restore hydrates the mock from a prior Snapshot.
func (c *MockClient) Restore(s Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.UserSessionToken == "" {
		c.email = ""
		c.authenticatedAt = time.Time{}
		return
	}
	c.email = s.Email
	c.authenticatedAt = s.AuthenticatedAt
	c.pendingOTPEmail = ""
}

// Compile-time assertion: MockClient satisfies Client.
var _ Client = (*MockClient)(nil)
