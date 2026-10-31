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
