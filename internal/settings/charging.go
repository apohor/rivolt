package settings

import (
	"context"
	"strconv"
	"strings"
)

// Keys for charging cost settings. Kept in the shared app_settings
// KV table so they survive restarts and can be reconfigured from the
// UI without redeploying.
const (
	keyChargingHomePricePerKWh = "charging.home_price_per_kwh"
	keyChargingHomeCurrency    = "charging.home_currency"
	keyChargingHomePriceSource = "charging.home_price_source"
	keyChargingGasPricePerGal  = "charging.gas_price_per_gallon"
	keyChargingComparisonMPG   = "charging.comparison_mpg"
)

// Where the home rate came from (ChargingConfig.HomePriceSource). The guided setup offers the state average as a one-tap default; Money marks
// charges priced with it as estimated until the user sets their own rate.
const (
	PriceSourceUser         = "user"
	PriceSourceStateAverage = "state_average"
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
	// HomePriceSource is "state_average" (the setup's default, estimated) or "user" (the user typed it); "" when no rate is set. A rate
	// stored before this key existed reads as "user".
	HomePriceSource string `json:"home_price_source"`
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
	if cfg.HomePricePerKWh > 0 {
		cfg.HomePriceSource = PriceSourceUser
		if v := all[keyChargingHomePriceSource]; v == PriceSourceStateAverage {
			cfg.HomePriceSource = v
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
	// The full overwrite from the web does not know the source: a rate left as stored keeps its source, a changed or sent one is the user's.
	source := PriceSourceUser
	if cfg.HomePricePerKWh <= 0 {
		source = ""
	} else if cfg.HomePriceSource == PriceSourceStateAverage {
		source = PriceSourceStateAverage
	} else if cfg.HomePriceSource == "" {
		if cur, err := GetChargingConfig(ctx, s); err == nil && cur.HomePricePerKWh == cfg.HomePricePerKWh {
			source = cur.HomePriceSource
		}
	}
	return s.SetMany(ctx, map[string]string{
		keyChargingHomePricePerKWh: strconv.FormatFloat(cfg.HomePricePerKWh, 'f', -1, 64),
		keyChargingHomePriceSource: source,
		keyChargingHomeCurrency:    cfg.HomeCurrency,
		keyChargingGasPricePerGal:  strconv.FormatFloat(cfg.GasPricePerGallon, 'f', -1, 64),
		keyChargingComparisonMPG:   strconv.FormatFloat(cfg.ComparisonMPG, 'f', -1, 64),
	})
}

// MaxHomePricePerKWh is the dearest home rate a patch accepts, per kilowatt hour.
const MaxHomePricePerKWh = 5.0

// HomeCurrencies are the currencies a patch accepts for the home rate: the set the
// cost calculator and the lease calculator already offer.
var HomeCurrencies = []string{"USD", "CAD", "EUR", "GBP", "AUD", "NZD", "CHF", "SEK", "NOK", "DKK"}

// NormalizeHomeCurrency returns c upper-cased when it is one of HomeCurrencies.
func NormalizeHomeCurrency(c string) (string, bool) {
	c = strings.ToUpper(strings.TrimSpace(c))
	for _, ok := range HomeCurrencies {
		if c == ok {
			return c, true
		}
	}
	return "", false
}

// Bounds a patch accepts for the gas-equivalent chip: a gas price per gallon from 0 to
// MaxGasPricePerGallon, a comparison MPG of 0 (the web's "hide the chip") or from
// MinComparisonMPG to MaxComparisonMPG.
const (
	MaxGasPricePerGallon = 20.0
	MinComparisonMPG     = 1.0
	MaxComparisonMPG     = 200.0
)

// PatchChargingConfig writes only the fields that are set (nil leaves the stored value
// alone). The caller has validated the values.
func PatchChargingConfig(ctx context.Context, s *Store, pricePerKWh *float64, currency *string, gasPrice, comparisonMPG *float64) error {
	return PatchChargingConfigSource(ctx, s, pricePerKWh, nil, currency, gasPrice, comparisonMPG)
}

// PatchChargingConfigSource is PatchChargingConfig with the rate's source. A rate written without one is the user's ("user"); the source is
// stored in the same write as the rate, so the two never disagree. The caller has checked that a source comes with a rate.
func PatchChargingConfigSource(ctx context.Context, s *Store, pricePerKWh *float64, source *string, currency *string, gasPrice, comparisonMPG *float64) error {
	if s == nil {
		return nil
	}
	kv := map[string]string{}
	if pricePerKWh != nil {
		kv[keyChargingHomePricePerKWh] = strconv.FormatFloat(*pricePerKWh, 'f', -1, 64)
		src := PriceSourceUser
		if source != nil {
			src = *source
		}
		if *pricePerKWh <= 0 {
			src = ""
		}
		kv[keyChargingHomePriceSource] = src
	}
	if currency != nil {
		kv[keyChargingHomeCurrency] = *currency
	}
	if gasPrice != nil {
		kv[keyChargingGasPricePerGal] = strconv.FormatFloat(*gasPrice, 'f', -1, 64)
	}
	if comparisonMPG != nil {
		kv[keyChargingComparisonMPG] = strconv.FormatFloat(*comparisonMPG, 'f', -1, 64)
	}
	return s.SetMany(ctx, kv)
}
