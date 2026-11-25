package applebill

import (
	"errors"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigFromEnv(t *testing.T) {
	if c, err := ConfigFromEnv(env(nil), true); c != nil || err != nil {
		t.Errorf("no bundle id: %v %v, want inert", c, err)
	}
	c, err := ConfigFromEnv(env(map[string]string{"RIVOLT_APPLE_IAP_BUNDLE_ID": "dev.example.test", "RIVOLT_APPLE_IAP_PRODUCTS": "pro.year, pro.month"}), true)
	if err != nil || c.Environment != EnvProduction || !c.Products["pro.year"] || !c.Products["pro.month"] {
		t.Fatalf("%+v %v", c, err)
	}
	// Billing on without a product list: refuse, so "any product under the bundle" cannot happen on prod by omission.
	if _, err := ConfigFromEnv(env(map[string]string{"RIVOLT_APPLE_IAP_BUNDLE_ID": "dev.example.test"}), true); err == nil {
		t.Error("billing on and no product list was accepted")
	}
	if c, err := ConfigFromEnv(env(map[string]string{"RIVOLT_APPLE_IAP_BUNDLE_ID": "dev.example.test"}), false); err != nil || c == nil {
		t.Errorf("billing off, no product list: %v %v", c, err)
	}
	if _, err := ConfigFromEnv(env(map[string]string{"RIVOLT_APPLE_IAP_BUNDLE_ID": "dev.example.test", "RIVOLT_APPLE_IAP_PRODUCTS": "p", "RIVOLT_APPLE_IAP_ENV": "both"}), true); err == nil {
		t.Error("an environment other than Production or Sandbox was accepted")
	}
	s, err := ConfigFromEnv(env(map[string]string{"RIVOLT_APPLE_IAP_BUNDLE_ID": "dev.example.test", "RIVOLT_APPLE_IAP_PRODUCTS": "p", "RIVOLT_APPLE_IAP_ENV": "Sandbox"}), true)
	if err != nil || s.Environment != EnvSandbox {
		t.Errorf("%+v %v", s, err)
	}
}

func TestConfigCheck(t *testing.T) {
	c := &Config{BundleID: "dev.example.test", Products: map[string]bool{"pro.year": true}, Environment: EnvProduction}
	if err := c.Check("dev.example.test", EnvProduction, "pro.year"); err != nil {
		t.Error(err)
	}
	for name, args := range map[string][3]string{
		"other bundle":      {"dev.other.test", EnvProduction, "pro.year"},
		"other environment": {"dev.example.test", EnvSandbox, "pro.year"},
		"other product":     {"dev.example.test", EnvProduction, "pro.other"},
	} {
		if err := c.Check(args[0], args[1], args[2]); !errors.Is(err, ErrRejected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// One environment per deployment: a Sandbox deployment refuses Production, too.
	sb := &Config{BundleID: "dev.example.test", Products: map[string]bool{"pro.year": true}, Environment: EnvSandbox}
	if err := sb.Check("dev.example.test", EnvProduction, "pro.year"); !errors.Is(err, ErrRejected) {
		t.Errorf("sandbox deployment accepted production: %v", err)
	}
}

// A bundle id with no product list must stop a billing-on start: Check would otherwise accept any product under the bundle.
func TestConfigFromEnvBundleWithoutProductsNeedsBillingOff(t *testing.T) {
	e := env(map[string]string{"RIVOLT_APPLE_IAP_BUNDLE_ID": "dev.example.test"})
	if _, err := ConfigFromEnv(e, true); err == nil {
		t.Fatal("bundle id without products with billing on must be an error")
	}
	if c, err := ConfigFromEnv(e, false); err != nil || c == nil {
		t.Fatalf("billing off: %v %v", c, err)
	}
}

// The Sandbox switch (App Review buys in the Sandbox against production): only a production deployment with the flag on accepts a
// Sandbox-signed transaction, and only as Sandbox; a Sandbox deployment is unchanged.
func TestConfigAcceptSandbox(t *testing.T) {
	base := map[string]string{"RIVOLT_APPLE_IAP_BUNDLE_ID": "dev.example.test", "RIVOLT_APPLE_IAP_PRODUCTS": "p"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range base {
			m[a] = b
		}
		m[k] = v
		return m
	}
	withMin := func(m map[string]string) map[string]string { m["RIVOLT_APPLE_IAP_SANDBOX_MIN_BUILD"] = "100"; return m }
	if _, err := ConfigFromEnv(env(with("RIVOLT_APPLE_IAP_ACCEPT_SANDBOX", "true")), true); err == nil {
		t.Error("the flag without the first purchase build was accepted")
	}
	off, _ := ConfigFromEnv(env(base), true)
	if off.AcceptSandbox || off.Sandbox(EnvSandbox) {
		t.Error("Sandbox accepted with the flag off")
	}
	if err := off.CheckTransaction("dev.example.test", EnvSandbox, "p"); !errors.Is(err, ErrRejected) {
		t.Errorf("flag off, Sandbox transaction: %v", err)
	}
	on, err := ConfigFromEnv(env(withMin(with("RIVOLT_APPLE_IAP_ACCEPT_SANDBOX", "true"))), true)
	if err != nil || !on.AcceptSandbox {
		t.Fatalf("%+v %v", on, err)
	}
	if err := on.CheckTransaction("dev.example.test", EnvSandbox, "p"); err != nil || on.SandboxMinBuild != 100 {
		t.Errorf("flag on, Sandbox transaction: %v (min build %d)", err, on.SandboxMinBuild)
	}
	if err := on.Check("dev.example.test", EnvSandbox, "p"); !errors.Is(err, ErrRejected) {
		t.Errorf("the notification route took a Sandbox notification: %v", err)
	}
	if on.Sandbox(EnvProduction) {
		t.Error("a Production transaction counted as Sandbox")
	}
	if err := on.Check("dev.example.test", EnvProduction, "p"); err != nil {
		t.Errorf("flag on must still accept Production: %v", err)
	}
	// The flag means nothing on a Sandbox deployment (preview): everything there is Sandbox already and keeps its real expiry.
	pv := with("RIVOLT_APPLE_IAP_ENV", "Sandbox")
	pv["RIVOLT_APPLE_IAP_ACCEPT_SANDBOX"] = "true"
	pv["RIVOLT_APPLE_IAP_SANDBOX_MIN_BUILD"] = "100"
	sb, _ := ConfigFromEnv(env(pv), true)
	if sb.AcceptSandbox || sb.Sandbox(EnvSandbox) {
		t.Error("the flag must not turn a Sandbox deployment's transactions into review purchases")
	}
	if err := sb.Check("dev.example.test", EnvProduction, "p"); !errors.Is(err, ErrRejected) {
		t.Errorf("sandbox deployment accepted production: %v", err)
	}
}
