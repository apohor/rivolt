package applebill

import (
	"errors"
	"fmt"
	"strings"
)

// Environments an App Store deployment can accept: exactly one per deployment.
const (
	EnvProduction = "Production"
	EnvSandbox    = "Sandbox"
)

// Config is the StoreKit configuration of this deployment. nil from ConfigFromEnv means StoreKit is not configured: no route exists.
type Config struct {
	BundleID    string
	Products    map[string]bool // the product ids Rivolt Pro is sold as
	Environment string
}

// ConfigFromEnv reads RIVOLT_APPLE_IAP_BUNDLE_ID, RIVOLT_APPLE_IAP_PRODUCTS (a comma list, REQUIRED when billing is on, so "any product under
// the bundle" cannot happen on prod by omission) and RIVOLT_APPLE_IAP_ENV (Production, the default, or Sandbox: one per deployment, never both).
// Without a bundle id it returns (nil, nil). A half-configured deployment is an error the caller must stop on.
func ConfigFromEnv(getenv func(string) string, billingOn bool) (*Config, error) {
	bundle := strings.TrimSpace(getenv("RIVOLT_APPLE_IAP_BUNDLE_ID"))
	if bundle == "" {
		return nil, nil
	}
	env := strings.TrimSpace(getenv("RIVOLT_APPLE_IAP_ENV"))
	if env == "" {
		env = EnvProduction
	}
	if env != EnvProduction && env != EnvSandbox {
		return nil, fmt.Errorf("RIVOLT_APPLE_IAP_ENV must be Production or Sandbox, got %q", env)
	}
	products := map[string]bool{}
	for _, p := range strings.Split(getenv("RIVOLT_APPLE_IAP_PRODUCTS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			products[p] = true
		}
	}
	if billingOn && len(products) == 0 {
		return nil, errors.New("RIVOLT_APPLE_IAP_PRODUCTS is required when billing is on (the product ids Rivolt Pro is sold as)")
	}
	return &Config{BundleID: bundle, Products: products, Environment: env}, nil
}

// ErrRejected is a signed value that is genuine but is not for this deployment: another app, another environment, another product.
var ErrRejected = errors.New("applebill: not for this deployment")

// Check refuses a bundle, environment or product that is not this deployment's. An empty product (a notification with no transaction)
// is not checked here; product is checked only when a transaction is present.
func (c *Config) Check(bundle, env, product string) error {
	if bundle != c.BundleID {
		return fmt.Errorf("%w: bundle", ErrRejected)
	}
	if env != c.Environment {
		return fmt.Errorf("%w: environment", ErrRejected)
	}
	if product != "" && len(c.Products) > 0 && !c.Products[product] {
		return fmt.Errorf("%w: product", ErrRejected)
	}
	return nil
}
