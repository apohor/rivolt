// Package rivian is Rivolt's adapter to the unofficial Rivian owner-app
// GraphQL API. The public surface is intentionally narrow so we can swap
// the implementation if the upstream breaks.
//
// NOTE: this is a stub. Credentials flow, token refresh, and the real
// GraphQL calls are implemented in follow-up commits once we vendor or
// reimplement a community Rivian client in Go.
package rivian

import (
	"context"
	"errors"
	"time"
)

// Credentials is what the operator submits via Settings → Rivian account.
// Stored encrypted at rest; never logged.
type Credentials struct {
	Email    string
	Password string
	OTP      string // optional: populated only for the initial MFA challenge
}

// Vehicle is a minimum-useful projection of a Rivian vehicle. More
// fields will be added as we flesh out the client.
type Vehicle struct {
	ID    string `json:"id"`
	VIN   string `json:"vin"`
	Name  string `json:"name"`
	Model string `json:"model"` // "R1T" | "R1S"
}

// State is a point-in-time snapshot of one vehicle. Units are metric at
// the wire; the UI converts for display based on user preference.
type State struct {
	At              time.Time `json:"at"`
	VehicleID       string    `json:"vehicle_id"`
	BatteryLevelPct float64   `json:"battery_level_pct"` // 0..100
	DistanceToEmpty float64   `json:"distance_to_empty"` // kilometers
	OdometerKm      float64   `json:"odometer_km"`
	Gear            string    `json:"gear"`          // "P" | "R" | "N" | "D"
	ChargerState    string    `json:"charger_state"` // "charging" | "charging_complete" | "disconnected" | ...
	ChargerPowerKW  float64   `json:"charger_power_kw"`
	ChargeTargetPct float64   `json:"charge_target_pct"` // 0..100
	Latitude        float64   `json:"latitude"`
	Longitude       float64   `json:"longitude"`
	Locked          bool      `json:"locked"`
	CabinTempC      float64   `json:"cabin_temp_c"`
	OutsideTempC    float64   `json:"outside_temp_c"`
	// PowerState is the vehicle's high-level power mode reported by the
	// gateway: "sleep" | "standby" | "ready" | "go" | "vehicle_reset" | ""
	// (unknown). Mirrors home-assistant-rivian's powerState sensor.
	PowerState string `json:"power_state"`
}

// Client is the high-level API surface Rivolt uses against Rivian.
// Concrete implementations live in live.go (real upstream) and
// mock.go (offline fixture for tests and demos).
type Client interface {
	Login(ctx context.Context, creds Credentials) error
	Vehicles(ctx context.Context) ([]Vehicle, error)
	State(ctx context.Context, vehicleID string) (*State, error)
}

// ErrNotImplemented is returned by the stub. Every method that hits the
// network returns this until we land the real client. Callers should
// treat ErrNotImplemented as "fine for boot, unusable at runtime."
var ErrNotImplemented = errors.New("rivian: client not implemented yet")

// ErrNotAuthenticated means the caller invoked a data method before
// Login succeeded (or after the session was cleared).
var ErrNotAuthenticated = errors.New("rivian: not authenticated; call Login first")

// ErrVehicleNotFound means the caller asked for state on a vehicle ID
// the client does not know about.
var ErrVehicleNotFound = errors.New("rivian: vehicle not found")

// StubClient is a safe-to-boot placeholder so cmd/rivolt compiles and
// the HTTP server comes up even before the real client is wired.
type StubClient struct{}

// NewStub returns a StubClient. Use this in main.go until the live
// client is in.
func NewStub() *StubClient { return &StubClient{} }

// Login returns ErrNotImplemented.
func (*StubClient) Login(context.Context, Credentials) error { return ErrNotImplemented }

// Vehicles returns ErrNotImplemented.
func (*StubClient) Vehicles(context.Context) ([]Vehicle, error) { return nil, ErrNotImplemented }

// State returns ErrNotImplemented.
func (*StubClient) State(context.Context, string) (*State, error) { return nil, ErrNotImplemented }

// Compile-time assertion: StubClient satisfies Client.
var _ Client = (*StubClient)(nil)
