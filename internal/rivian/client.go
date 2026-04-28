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
	ID        string `json:"id"`
	VIN       string `json:"vin"`
	Name      string `json:"name"`
	Model     string `json:"model"` // "R1T" | "R1S"
	ModelYear int    `json:"model_year,omitempty"`
	Make      string `json:"make,omitempty"`
	TrimID    string `json:"trim_id,omitempty"`   // e.g. "PKG-ADV", "LRG-DM-PRFM"
	TrimName  string `json:"trim_name,omitempty"` // e.g. "Adventure Package"
	// PackKWh is the usable battery capacity inferred from model /
	// trim. Used by the live recorder to estimate energy from SoC
	// deltas when Rivian's live session feed doesn't report power.
	PackKWh float64 `json:"pack_kwh,omitempty"`
	// ImageURL is a pre-rendered 3/4 view of the configured vehicle,
	// populated lazily via GetVehicleImages. Empty string when not
	// yet fetched or when the Rivian image service didn't return one.
	// This is a convenience pick (hero image) — the full set of
	// rendered angles (front, rear, side, interior, etc.) is in
	// Images.
	ImageURL string `json:"image_url,omitempty"`
	// Images is the full set of configurator-rendered images Rivian
	// returns for this vehicle — typically 6-8 angles including
	// 3/4 front, side profile, rear, interior cabin, wheel detail,
	// etc. Each entry carries a `placement` tag describing the
	// camera angle so the UI can build a gallery or swap the hero
	// image on hover.
	Images []VehicleImage `json:"images,omitempty"`
}

// State is a point-in-time snapshot of one vehicle. Units are metric at
// the wire; the UI converts for display based on user preference.
type State struct {
	At              time.Time `json:"at"`
	VehicleID       string    `json:"vehicle_id"`
	BatteryLevelPct float64   `json:"battery_level_pct"` // 0..100
	// BatteryCapacityKWh is the vehicle's self-reported usable pack
	// capacity in kWh (batteryCapacity field on vehicleState). Zero
	// when the field wasn't included in the subscription push or
	// when an older firmware doesn't emit it.
	BatteryCapacityKWh float64 `json:"battery_capacity_kwh"`
	DistanceToEmpty    float64 `json:"distance_to_empty"` // kilometers
	OdometerKm         float64 `json:"odometer_km"`
	Gear               string  `json:"gear"`          // "P" | "R" | "N" | "D"
	DriveMode          string  `json:"drive_mode"`    // "everyday" | "sport" | ...
	ChargerState       string  `json:"charger_state"` // "charging_active" | "charger_disconnected" | ...
	ChargerPowerKW     float64 `json:"charger_power_kw"`
	ChargeTargetPct    float64 `json:"charge_target_pct"` // 0..100
	// ChargerStatus is the physical plug state ("chrgr_sts_connected_charging",
	// "chrgr_sts_not_connected", ...) — different from ChargerState which is
	// the session state.
	ChargerStatus           string  `json:"charger_status"`
	ChargePortState         string  `json:"charge_port_state"` // "open" | "closed" | ""
	RemoteChargingAvailable string  `json:"remote_charging_available"`
	Latitude                float64 `json:"latitude"`
	Longitude               float64 `json:"longitude"`
	SpeedKph                float64 `json:"speed_kph"`
	HeadingDeg              float64 `json:"heading_deg"`
	AltitudeM               float64 `json:"altitude_m"`
	// Aggregate closures / locks.
	Locked         bool `json:"locked"` // all LOCK_STATE_ENTITIES not unlocked
	DoorsClosed    bool `json:"doors_closed"`
	FrunkClosed    bool `json:"frunk_closed"`
	LiftgateClosed bool `json:"liftgate_closed"`
	TailgateClosed bool `json:"tailgate_closed"`
	TonneauClosed  bool `json:"tonneau_closed"`
	// Climate / power.
	CabinTempC                 float64 `json:"cabin_temp_c"`
	OutsideTempC               float64 `json:"outside_temp_c"`
	CabinPreconditioningStatus string  `json:"cabin_preconditioning_status"`
	// PowerState is the vehicle's high-level power mode reported by the
	// gateway: "sleep" | "standby" | "ready" | "go" | "vehicle_reset" | ""
	// (unknown). Mirrors home-assistant-rivian's powerState sensor.
	PowerState string `json:"power_state"`
	// Safety / maintenance.
	AlarmSoundStatus        string `json:"alarm_sound_status"`
	TwelveVoltBatteryHealth string `json:"twelve_volt_battery_health"`
	WiperFluidState         string `json:"wiper_fluid_state"`
	// Software (OTA). Available version may equal current when nothing is
	// pending. Install progress is 0..100.
	OtaCurrentVersion   string  `json:"ota_current_version"`
	OtaAvailableVersion string  `json:"ota_available_version"`
	OtaStatus           string  `json:"ota_status"`
	OtaInstallProgress  float64 `json:"ota_install_progress"`
	// Tires. Pressures reported in bar (per home-assistant-rivian's
	// native unit declaration). Status is a string like "Normal" /
	// "Low" / "" (unknown).
	TirePressureFLBar    float64 `json:"tire_pressure_fl_bar"`
	TirePressureFRBar    float64 `json:"tire_pressure_fr_bar"`
	TirePressureRLBar    float64 `json:"tire_pressure_rl_bar"`
	TirePressureRRBar    float64 `json:"tire_pressure_rr_bar"`
	TirePressureStatusFL string  `json:"tire_pressure_status_fl"`
	TirePressureStatusFR string  `json:"tire_pressure_status_fr"`
	TirePressureStatusRL string  `json:"tire_pressure_status_rl"`
	TirePressureStatusRR string  `json:"tire_pressure_status_rr"`
}

// LiveSession is the snapshot of an in-progress charging session,
// pulled from Rivian's `chrg/user/graphql` endpoint via
// getLiveSessionData. All zero/empty when no session is active.
// Units: power in kW, energy in kWh, rate in km/h, SoC in percent,
// time fields in whole seconds.
type LiveSession struct {
	At                       time.Time `json:"at"`
	VehicleID                string    `json:"vehicle_id"`
	Active                   bool      `json:"active"`
	VehicleChargerState      string    `json:"vehicle_charger_state"`
	StartTime                string    `json:"start_time"`
	TimeElapsedSeconds       int64     `json:"time_elapsed_seconds"`
	TimeRemainingSeconds     int64     `json:"time_remaining_seconds"`
	PowerKW                  float64   `json:"power_kw"`
	KilometersChargedPerHour float64   `json:"kilometers_charged_per_hour"`
	RangeAddedKm             float64   `json:"range_added_km"`
	TotalChargedEnergyKWh    float64   `json:"total_charged_energy_kwh"`
	// PackKWh / ThermalKWh / OutletsKWh / SystemKWh come only from the
	// Parallax ChargingSessionLiveData breakdown. The regular
	// ChargingSession subscription leaves them at zero. Together they
	// approximate where the wall energy went: into the pack, into
	// thermal management, into 12V outlets / accessories, and into
	// other vehicle systems. ThermalKWh is the closest thing Rivian
	// gives us to a "battery temperature" signal — high values during
	// a session mean the BMS is working hard to heat or cool the pack.
	PackKWh         float64 `json:"pack_kwh"`
	ThermalKWh      float64 `json:"thermal_kwh"`
	OutletsKWh      float64 `json:"outlets_kwh"`
	SystemKWh       float64 `json:"system_kwh"`
	SoCPct          float64 `json:"soc_pct"`
	CurrentPrice    string  `json:"current_price"`
	CurrentCurrency string  `json:"current_currency"`
	IsFreeSession   bool    `json:"is_free_session"`
	IsRivianCharger bool    `json:"is_rivian_charger"`
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
