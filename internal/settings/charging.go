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
)

// DefaultHomeCurrency is the ISO-4217 code used when the operator
// hasn't set anything. Picked to match the app's target audience; the
// UI exposes the field so it can be changed.
const DefaultHomeCurrency = "USD"

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
}

// GetChargingConfig returns the stored home-charging cost settings,
// filling defaults where unset.
func GetChargingConfig(ctx context.Context, s *Store) (ChargingConfig, error) {
	cfg := ChargingConfig{HomeCurrency: DefaultHomeCurrency}
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
	if err := s.Set(ctx, keyChargingHomePricePerKWh,
		strconv.FormatFloat(cfg.HomePricePerKWh, 'f', -1, 64)); err != nil {
		return err
	}
	return s.Set(ctx, keyChargingHomeCurrency, cfg.HomeCurrency)
}
