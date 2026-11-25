package applebill

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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
	// AcceptSandbox (RIVOLT_APPLE_IAP_ACCEPT_SANDBOX, production only, on only while a version is in App Review): an Apple-signed Sandbox
	// transaction is accepted for the accounts in SandboxAccounts and for no one else. TestFlight purchases are Sandbox too, so
	// without the allowlist every tester would get Pro.
	AcceptSandbox bool
	// SandboxAccounts (RIVOLT_APPLE_IAP_SANDBOX_ACCOUNTS, required with AcceptSandbox): the account ids whose Sandbox purchases count, the review
	// account only. Set in the deployment's values (Git), not in a table, so it moves with the flag.
	SandboxAccounts map[uuid.UUID]bool
	// SandboxMinBuild (RIVOLT_APPLE_IAP_SANDBOX_MIN_BUILD, required with AcceptSandbox): the first app build that can buy. Builds before it (1.4.3
	// shows a paywall whenever billing_enabled and the products are present) are told neither, review or not.
	SandboxMinBuild int
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
	accept := env == EnvProduction && parseBool(getenv("RIVOLT_APPLE_IAP_ACCEPT_SANDBOX"))
	minBuild := 0
	if accept {
		n, err := strconv.Atoi(strings.TrimSpace(getenv("RIVOLT_APPLE_IAP_SANDBOX_MIN_BUILD")))
		if err != nil || n <= 0 {
			return nil, errors.New("RIVOLT_APPLE_IAP_SANDBOX_MIN_BUILD (the first app build with the purchase UI) is required with RIVOLT_APPLE_IAP_ACCEPT_SANDBOX")
		}
		minBuild = n
	}
	var accounts map[uuid.UUID]bool
	if accept {
		accounts = map[uuid.UUID]bool{}
		for _, f := range strings.Split(getenv("RIVOLT_APPLE_IAP_SANDBOX_ACCOUNTS"), ",") {
			if f = strings.TrimSpace(f); f == "" {
				continue
			}
			u, err := uuid.Parse(f)
			if err != nil {
				return nil, errors.New("RIVOLT_APPLE_IAP_SANDBOX_ACCOUNTS holds an id that is not a UUID")
			}
			accounts[u] = true
		}
		if len(accounts) == 0 {
			return nil, errors.New("RIVOLT_APPLE_IAP_SANDBOX_ACCOUNTS (the review account) is required with RIVOLT_APPLE_IAP_ACCEPT_SANDBOX")
		}
	}
	return &Config{BundleID: bundle, Products: products, Environment: env, AcceptSandbox: accept, SandboxMinBuild: minBuild, SandboxAccounts: accounts}, nil
}

// ErrRejected is a signed value that is genuine but is not for this deployment: another app, another environment, another product.
var ErrRejected = errors.New("applebill: not for this deployment")

// Check refuses a bundle, environment or product that is not this deployment's. An empty product (a notification with no transaction)
// is not checked here; product is checked only when a transaction is present. The notification route uses this: production never takes
// Sandbox notifications (the review account's purchase is reported by the app, and App Store Connect's Sandbox URL points at preview).
func (c *Config) Check(bundle, env, product string) error {
	if env != c.Environment {
		return fmt.Errorf("%w: environment", ErrRejected)
	}
	return c.CheckTransaction(bundle, env, product)
}

// CheckTransaction is Check for the app's own report, which also takes a Sandbox transaction when AcceptSandbox is on.
func (c *Config) CheckTransaction(bundle, env, product string) error {
	if bundle != c.BundleID {
		return fmt.Errorf("%w: bundle", ErrRejected)
	}
	if env != c.Environment && !c.Sandbox(env) {
		return fmt.Errorf("%w: environment", ErrRejected)
	}
	if product != "" && len(c.Products) > 0 && !c.Products[product] {
		return fmt.Errorf("%w: product", ErrRejected)
	}
	return nil
}

// Sandbox reports whether env is a Sandbox transaction this deployment accepts as such: a production deployment with AcceptSandbox on. The
// caller must then check the account against the allowlist (SandboxAllowed) before it records anything.
func (c *Config) Sandbox(env string) bool {
	return c.AcceptSandbox && c.Environment == EnvProduction && env == EnvSandbox
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// SandboxCap is how long a Sandbox entitlement can last from now, whatever Apple's sandbox expiry says.
const SandboxCap = 24 * time.Hour

// SandboxAllowed reports whether the account is one of the review accounts named in the deployment's values.
func (c *Config) SandboxAllowed(uid uuid.UUID) bool { return c.AcceptSandbox && c.SandboxAccounts[uid] }
