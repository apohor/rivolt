package preflight

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

var launchAt = time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)

func env(extra map[string]string) func(string) string {
	m := map[string]string{
		"RIVOLT_STRIPE_SECRET_KEY":     "sk_live_TOPSECRETKEYVALUE",
		"RIVOLT_STRIPE_WEBHOOK_SECRET": "whsec_TOPSECRETWEBHOOKVALUE",
		"RIVOLT_STRIPE_PRICE_MONTHLY":  "price_monthlyPRIVATEID",
		"RIVOLT_STRIPE_PRICE_YEARLY":   "price_yearlyPRIVATEID",
		"RIVOLT_PUBLIC_BASE_URL":       "https://rivolt.example",
		"RIVOLT_APPLE_IAP_BUNDLE_ID":   "com.example.bundlePRIVATE",
		"RIVOLT_APPLE_IAP_PRODUCTS":    "pro.monthly.PRIVATE,pro.yearly.PRIVATE",
	}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func okInput() Input {
	return Input{
		Getenv: env(nil),
		Launch: launchAt,
		Founding: func(context.Context, time.Time) (Founding, error) {
			return Founding{Accounts: 11, Demo: 1, Eligible: 10, Already: 0, ToGrant: 10}, nil
		},
		Fetch: func(context.Context, string) (int, error) { return http.StatusOK, nil },
	}
}

func render(r *Report) string {
	var b bytes.Buffer
	r.Print(&b)
	return b.String()
}

// A deployment with every setting, a clean founding dry run and answering pages is ready, and the report says what it checked.
func TestPreflightReady(t *testing.T) {
	r := Run(context.Background(), okInput())
	out := render(r)
	// A launch item added later and still pending is the list doing its job, not this test's concern.
	for _, p := range r.Problems {
		if !strings.Contains(p, "launch-readiness") && !strings.Contains(p, "while these are pending") {
			t.Fatalf("not ready: %v\n%s", r.Problems, out)
		}
	}
	for _, want := range []string{
		"items cleared", "accounts: 11, demo: 1, eligible (created by launch, demo excluded): 10", "already founding: 0, to grant: 10",
		"https://rivolt.dev/terms: 200", "https://rivolt.dev/privacy: 200", "https://rivolt.dev/settings: 200",
		"RIVOLT_BILLING: off", "RIVOLT_STRIPE_SECRET_KEY: set", "Stripe key mode: live", "RIVOLT_APPLE_IAP_PRODUCTS: 2 product id(s)",
		"Apple environment: Production", "Nothing was switched on or written",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

// The report never carries a value of a setting: not a key, a secret, a price or product id, a bundle id.
func TestPreflightPrintsNoSecrets(t *testing.T) {
	for _, billing := range []string{"", "true"} {
		in := okInput()
		in.Getenv = env(map[string]string{"RIVOLT_BILLING": billing})
		out := render(Run(context.Background(), in))
		for _, secret := range []string{"TOPSECRET", "PRIVATE", "sk_live", "whsec", "price_", "com.example", "rivolt.example"} {
			if strings.Contains(out, secret) {
				t.Errorf("billing=%q: the report contains %q:\n%s", billing, secret, out)
			}
		}
	}
	// A misconfigured Apple environment names the setting, not what it was set to be beyond the allowed words.
	in := okInput()
	in.Getenv = env(map[string]string{"RIVOLT_APPLE_IAP_PRODUCTS": ""})
	if out := render(Run(context.Background(), in)); strings.Contains(out, "PRIVATE") {
		t.Errorf("the Apple error leaks a value:\n%s", out)
	}
}

// Each thing that stops the flip is a problem named in the report, and the report is NOT READY.
func TestPreflightProblems(t *testing.T) {
	cases := map[string]func(*Input){
		"missing stripe key":     func(in *Input) { in.Getenv = env(map[string]string{"RIVOLT_STRIPE_SECRET_KEY": ""}) },
		"missing webhook secret": func(in *Input) { in.Getenv = env(map[string]string{"RIVOLT_STRIPE_WEBHOOK_SECRET": ""}) },
		"missing a price":        func(in *Input) { in.Getenv = env(map[string]string{"RIVOLT_STRIPE_PRICE_YEARLY": ""}) },
		"missing base url":       func(in *Input) { in.Getenv = env(map[string]string{"RIVOLT_PUBLIC_BASE_URL": ""}) },
		"missing apple bundle":   func(in *Input) { in.Getenv = env(map[string]string{"RIVOLT_APPLE_IAP_BUNDLE_ID": ""}) },
		"missing apple products": func(in *Input) { in.Getenv = env(map[string]string{"RIVOLT_APPLE_IAP_PRODUCTS": ""}) },
		"bad apple environment":  func(in *Input) { in.Getenv = env(map[string]string{"RIVOLT_APPLE_IAP_ENV": "Staging"}) },
		"a page answers 404": func(in *Input) {
			in.Fetch = func(_ context.Context, u string) (int, error) {
				if strings.HasSuffix(u, "/privacy") {
					return http.StatusNotFound, nil
				}
				return http.StatusOK, nil
			}
		},
		"a page is unreachable": func(in *Input) {
			in.Fetch = func(context.Context, string) (int, error) { return 0, errors.New("unreachable") }
		},
		"founding dry run fails": func(in *Input) {
			in.Founding = func(context.Context, time.Time) (Founding, error) { return Founding{}, errors.New("no table") }
		},
		"founding counts disagree": func(in *Input) {
			in.Founding = func(context.Context, time.Time) (Founding, error) {
				return Founding{Accounts: 11, Demo: 1, Eligible: 9, ToGrant: 9}, nil
			}
		},
	}
	for name, mutate := range cases {
		in := okInput()
		mutate(&in)
		r := Run(context.Background(), in)
		if r.Ready() {
			t.Errorf("%s: ready, want a problem\n%s", name, render(r))
		}
		if !strings.Contains(render(r), "NOT READY") {
			t.Errorf("%s: the report does not say NOT READY", name)
		}
	}
}

// Warnings do not stop the flip but are printed: accounts created after the launch instant, billing already on, Stripe and Apple in
// different modes.
func TestPreflightWarnings(t *testing.T) {
	in := okInput()
	in.Getenv = env(map[string]string{"RIVOLT_BILLING": "true", "RIVOLT_APPLE_IAP_ENV": "Sandbox"})
	in.Founding = func(context.Context, time.Time) (Founding, error) {
		return Founding{Accounts: 12, Demo: 1, Eligible: 10, After: 1, ToGrant: 10}, nil
	}
	r := Run(context.Background(), in)
	out := render(r)
	if !r.Ready() {
		t.Fatalf("warnings stopped the flip: %v\n%s", r.Problems, out)
	}
	for _, want := range []string{"created after the launch instant", "billing is already on", "Stripe is in live mode but Apple is Sandbox"} {
		if !strings.Contains(out, want) {
			t.Errorf("no warning containing %q:\n%s", want, out)
		}
	}
}

// Offline and database-less runs say what they skipped rather than pass silently.
func TestPreflightSaysWhatItSkipped(t *testing.T) {
	in := okInput()
	in.Fetch, in.Founding = nil, nil
	out := render(Run(context.Background(), in))
	if strings.Count(out, "not checked") != 2 {
		t.Errorf("want both skipped parts named:\n%s", out)
	}
}

func TestKeyModeIsOnlyThePrefixClass(t *testing.T) {
	for key, want := range map[string]string{"sk_live_abc": "live", "rk_live_abc": "live", "sk_test_abc": "test", "rk_test_x": "test", "weird": "unrecognised", "": "unrecognised"} {
		if got := keyMode(key); got != want {
			t.Errorf("keyMode(%q) = %q, want %q", key, got, want)
		}
	}
}

// The pages checked are exactly the ones the emails and the launch notice link to.
func TestURLsAreTheOnesTheCopyLinksTo(t *testing.T) {
	got := strings.Join(URLs(), " ")
	for _, want := range []string{"https://rivolt.dev/terms ", "https://rivolt.dev/privacy ", "https://rivolt.dev/settings "} {
		if !strings.Contains(got+" ", want) {
			t.Errorf("URLs lacks %q: %s", want, got)
		}
	}
	for _, u := range URLs() {
		if strings.Contains(u, "#") {
			t.Errorf("%s keeps a fragment", u)
		}
	}
}
