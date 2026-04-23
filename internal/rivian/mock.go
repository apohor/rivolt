package rivian

import (
	"context"
	"sync"
	"time"
)

// MockClient is a Client implementation backed by in-memory fixture
// data. It exists so the UI can be demoed without Rivian credentials
// and so handler-level tests can drive a predictable, offline Client.
//
// The defaults model a stationary Rivian R1T at ~72% SoC. Callers can
// swap the state out via SetState before serving requests.
type MockClient struct {
	mu             sync.Mutex
	loggedIn       bool
	vehicles       []Vehicle
	states         map[string]State
	LoginReturnErr error // if non-nil, Login returns this error
}

// NewMock returns a MockClient primed with one fake vehicle.
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
				At:              time.Now().UTC(),
				VehicleID:       id,
				BatteryLevelPct: 72,
				DistanceToEmpty: 310,
				OdometerKm:      24500,
				Gear:            "P",
				ChargerState:    "charger_disconnected",
				ChargerPowerKW:  0,
				ChargeTargetPct: 80,
				Latitude:        37.7749,
				Longitude:       -122.4194,
				Locked:          true,
				CabinTempC:      21,
				OutsideTempC:    18,
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

// Login accepts any credentials. Returns LoginReturnErr if set so
// tests can exercise failure paths without patching transport.
func (c *MockClient) Login(_ context.Context, _ Credentials) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.LoginReturnErr != nil {
		return c.LoginReturnErr
	}
	c.loggedIn = true
	return nil
}

// Vehicles returns the fixture list. Requires a prior Login call so
// tests that forget auth fail obviously.
func (c *MockClient) Vehicles(_ context.Context) ([]Vehicle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loggedIn {
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
	if !c.loggedIn {
		return nil, ErrNotAuthenticated
	}
	s, ok := c.states[vehicleID]
	if !ok {
		return nil, ErrVehicleNotFound
	}
	s.At = time.Now().UTC()
	return &s, nil
}

// Compile-time assertion: MockClient satisfies Client.
var _ Client = (*MockClient)(nil)
