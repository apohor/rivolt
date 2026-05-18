package settings

import (
	"context"
	"strconv"
)

// Keys for charging cost settings. Kept in the shared app_settings
// KV table so they survive restarts and can be reconfigured from the
// UI without redeploying.
const (
	keyChargingHomePricePerKWh = "charging.home_price_per_kwh"
	keyChargingHomeCurrency    = "charging.home_currency"
	keyChargingGasPricePerGal  = "charging.gas_price_per_gallon"
	keyChargingComparisonMPG   = "charging.comparison_mpg"
)

// DefaultHomeCurrency is the ISO-4217 code used when the operator
// hasn't set anything. Picked to match the app's target audience; the
// UI exposes the field so it can be changed.
const DefaultHomeCurrency = "USD"

// Defaults for the gas-equivalent comparison chip on the trip planner.
// $4/gal and 20 MPG approximate a same-class ICE pickup; both are
// user-editable.
const (
	DefaultGasPricePerGallon = 4.0
	DefaultComparisonMPG     = 20.0
)

// ChargingConfig is the user-configurable cost-of-energy settings
// applied locally to estimate the price of sessions Rivian reports
// as free (home AC, L2 on non-RAN chargers, etc.).
type ChargingConfig struct {
	// HomePricePerKWh is the retail cost in HomeCurrency per kilowatt
	// hour. Zero means "not configured"; in that case estimated cost
	// is not computed.
	HomePricePerKWh float64 `json:"home_price_per_kwh"`
	// HomeCurrency is the ISO-4217 code displayed next to estimated
	// cost. Defaults to USD.
	HomeCurrency string `json:"home_currency"`
	// GasPricePerGallon and ComparisonMPG feed the gas-equivalent chip
	// on the trip planner. Zero on either disables the chip.
	GasPricePerGallon float64 `json:"gas_price_per_gallon"`
	ComparisonMPG     float64 `json:"comparison_mpg"`
}

// GetChargingConfig returns the stored home-charging cost settings,
// filling defaults where unset.
func GetChargingConfig(ctx context.Context, s *Store) (ChargingConfig, error) {
	cfg := ChargingConfig{
		HomeCurrency:      DefaultHomeCurrency,
		GasPricePerGallon: DefaultGasPricePerGallon,
		ComparisonMPG:     DefaultComparisonMPG,
	}
	if s == nil {
		return cfg, nil
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		return cfg, err
	}
	if v, ok := all[keyChargingHomePricePerKWh]; ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cfg.HomePricePerKWh = f
		}
	}
	if v, ok := all[keyChargingHomeCurrency]; ok && v != "" {
		cfg.HomeCurrency = v
	}
	if v, ok := all[keyChargingGasPricePerGal]; ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cfg.GasPricePerGallon = f
		}
	}
	if v, ok := all[keyChargingComparisonMPG]; ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cfg.ComparisonMPG = f
		}
	}
	return cfg, nil
}

// SetChargingConfig persists the provided cost settings. Negative
// prices are rejected by coercing to zero so the UI can't persist a
// nonsensical value.
func SetChargingConfig(ctx context.Context, s *Store, cfg ChargingConfig) error {
	if s == nil {
		return nil
	}
	if cfg.HomePricePerKWh < 0 {
		cfg.HomePricePerKWh = 0
	}
	if cfg.HomeCurrency == "" {
		cfg.HomeCurrency = DefaultHomeCurrency
	}
	if cfg.GasPricePerGallon < 0 {
		cfg.GasPricePerGallon = 0
	}
	if cfg.ComparisonMPG < 0 {
		cfg.ComparisonMPG = 0
	}
	if err := s.Set(ctx, keyChargingHomePricePerKWh,
		strconv.FormatFloat(cfg.HomePricePerKWh, 'f', -1, 64)); err != nil {
		return err
	}
	if err := s.Set(ctx, keyChargingHomeCurrency, cfg.HomeCurrency); err != nil {
		return err
	}
	if err := s.Set(ctx, keyChargingGasPricePerGal,
		strconv.FormatFloat(cfg.GasPricePerGallon, 'f', -1, 64)); err != nil {
		return err
	}
	return s.Set(ctx, keyChargingComparisonMPG,
		strconv.FormatFloat(cfg.ComparisonMPG, 'f', -1, 64))
}
