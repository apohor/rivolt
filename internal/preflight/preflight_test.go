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
		"RIVOLT_REDIS_ADDR":            "redis.example:6379",
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
		Fetch:     func(context.Context, string) (int, error) { return http.StatusOK, nil },
		Webhook:   func(context.Context, string) (int, error) { return http.StatusBadRequest, nil },
		RedisTime: func(context.Context) (bool, error) { return false, nil },
		Stripe: func(context.Context) StripeFacts {
			return StripeFacts{Live: true, PricesActive: true, PortalFound: true, TermsSet: true, PrivacySet: true, PaymentMethodUpdate: true, CheckoutRead: true, CheckoutMethods: []string{"apple_pay", "card", "google_pay", "link"}}
		},
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
		"host: rivolt.example", "https://rivolt.example/terms: reachable (HTTP 200)", "https://rivolt.example/privacy: reachable (HTTP 200)", "https://rivolt.example/settings: reachable (HTTP 200)", "unsigned POST to rivolt.example: 400",
		"RIVOLT_BILLING: off", "RIVOLT_STRIPE_SECRET_KEY: set", "RIVOLT_APPLE_IAP_PRODUCTS: set",
		"TIME as the app user: allowed", "Checkout offers: apple_pay, card, google_pay, link", "mode: live", "portal: Terms URL set, Privacy URL set, payment-method update on",
		"Nothing was switched on or written",
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
		for _, secret := range []string{"TOPSECRET", "PRIVATE", "sk_", "whsec", "price_", "com.example", "redis.example", "Production", "Sandbox"} {
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
		"webhook redirects": func(in *Input) {
			in.Webhook = func(context.Context, string) (int, error) { return http.StatusFound, nil }
		},
		"webhook blocked": func(in *Input) {
			in.Webhook = func(context.Context, string) (int, error) { return http.StatusForbidden, nil }
		},
		"webhook unreachable": func(in *Input) {
			in.Webhook = func(context.Context, string) (int, error) { return 0, errors.New("unreachable") }
		},
		"redis denies TIME": func(in *Input) {
			in.RedisTime = func(context.Context) (bool, error) { return true, errors.New("NOPERM") }
		},
		"no stripe terms url": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts {
				return StripeFacts{PricesActive: true, PortalFound: true, PrivacySet: true, PaymentMethodUpdate: true}
			}
		},
		"no stripe privacy url": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts {
				return StripeFacts{PricesActive: true, PortalFound: true, TermsSet: true, PaymentMethodUpdate: true}
			}
		},
		"no payment method update": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts {
				return StripeFacts{PricesActive: true, PortalFound: true, TermsSet: true, PrivacySet: true}
			}
		},
		"checkout offers no card": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts {
				return StripeFacts{PricesActive: true, PortalFound: true, TermsSet: true, PrivacySet: true, PaymentMethodUpdate: true, CheckoutRead: true, CheckoutMethods: []string{"apple_pay"}}
			}
		},
		"checkout offers klarna": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts {
				return StripeFacts{PricesActive: true, PortalFound: true, TermsSet: true, PrivacySet: true, PaymentMethodUpdate: true, CheckoutRead: true, CheckoutMethods: []string{"card", "klarna"}}
			}
		},
		"checkout offers a bank method": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts {
				return StripeFacts{PricesActive: true, PortalFound: true, TermsSet: true, PrivacySet: true, PaymentMethodUpdate: true, CheckoutRead: true, CheckoutMethods: []string{"card", "us_bank_account"}}
			}
		},
		"checkout methods unreadable": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts {
				return StripeFacts{PricesActive: true, PortalFound: true, TermsSet: true, PrivacySet: true, PaymentMethodUpdate: true}
			}
		},
		"stripe unreadable is not ready": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts { return StripeFacts{Err: errors.New("stripe: 403 permission")} }
		},
		"no portal configuration": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts { return StripeFacts{PricesActive: true} }
		},
		"a price is inactive": func(in *Input) {
			in.Stripe = func(context.Context) StripeFacts {
				return StripeFacts{PortalFound: true, TermsSet: true, PrivacySet: true, PaymentMethodUpdate: true}
			}
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
	in.RedisTime = func(context.Context) (bool, error) { return false, errors.New("timeout") }
	in.Founding = func(context.Context, time.Time) (Founding, error) {
		return Founding{Accounts: 12, Demo: 1, Eligible: 10, After: 1, ToGrant: 10}, nil
	}
	r := Run(context.Background(), in)
	out := render(r)
	if !r.Ready() {
		t.Fatalf("warnings stopped the flip: %v\n%s", r.Problems, out)
	}
	for _, want := range []string{"created after the launch instant", "billing is already on", "could not run TIME"} {
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
	if strings.Count(out, "not checked") < 2 {
		t.Errorf("want both skipped parts named:\n%s", out)
	}
}

// Stripe live against Apple Sandbox (or the reverse) is a warning that names neither value.
func TestPreflightModeMismatchIsAWarning(t *testing.T) {
	in := okInput()
	in.Getenv = env(map[string]string{"RIVOLT_APPLE_IAP_ENV": "Sandbox"})
	r := Run(context.Background(), in)
	out := render(r)
	if !strings.Contains(out, "the Stripe mode and the Apple environment disagree") || strings.Contains(out, "Sandbox") {
		t.Errorf("mismatch not reported as a bare warning:\n%s", out)
	}
}

// The pages checked are the ones the copy links to, on the deployment's own base URL; the Plan card and the export page are one path.
func TestURLsAreTheOnesTheCopyLinksTo(t *testing.T) {
	got := strings.Join(URLs("https://preview.example/"), " ")
	for _, want := range []string{"https://preview.example/terms ", "https://preview.example/privacy ", "https://preview.example/settings"} {
		if !strings.Contains(got+" ", want) {
			t.Errorf("URLs lacks %q: %s", want, got)
		}
	}
	if strings.Count(got, "/settings") != 1 {
		t.Errorf("the settings path is listed twice: %s", got)
	}
	for _, u := range URLs("https://preview.example") {
		if strings.Contains(u, "#") || strings.Contains(u, "rivolt.dev") {
			t.Errorf("%s keeps a fragment or the production host", u)
		}
	}
}

// The probes go to this deployment (preview checks preview, not production), and the report says which host the webhook probe hit.
func TestPreflightProbesTheConfiguredBase(t *testing.T) {
	in := okInput()
	in.Getenv = env(map[string]string{"RIVOLT_PUBLIC_BASE_URL": "https://preview.example"})
	var fetched, posted []string
	in.Fetch = func(_ context.Context, u string) (int, error) {
		fetched = append(fetched, u)
		return http.StatusOK, nil
	}
	in.Webhook = func(_ context.Context, u string) (int, error) {
		posted = append(posted, u)
		return http.StatusBadRequest, nil
	}
	out := render(Run(context.Background(), in))
	for _, u := range fetched {
		if !strings.HasPrefix(u, "https://preview.example/") {
			t.Errorf("a page check went to %s, want the configured base", u)
		}
	}
	if len(fetched) == 0 || len(posted) != 1 || posted[0] != "https://preview.example/api/billing/stripe/webhook" {
		t.Errorf("fetched %v, posted %v", fetched, posted)
	}
	for _, want := range []string{"host: preview.example", "unsigned POST to preview.example: 400"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	// No base URL: the pages are not probed (the missing setting is already a problem), never a production fallback.
	in.Getenv = env(map[string]string{"RIVOLT_PUBLIC_BASE_URL": ""})
	fetched = nil
	if r := Run(context.Background(), in); r.Ready() || len(fetched) != 0 {
		t.Errorf("without a base URL: ready=%v, fetched %v; want not ready and nothing probed", r.Ready(), fetched)
	}
}

// Several disallowed Checkout methods are ONE problem line listing them, not one line each.
func TestPreflightOneLineForExtraCheckoutMethods(t *testing.T) {
	in := okInput()
	in.Stripe = func(context.Context) StripeFacts {
		return StripeFacts{PricesActive: true, PortalFound: true, TermsSet: true, PrivacySet: true, PaymentMethodUpdate: true, CheckoutRead: true,
			CheckoutMethods: []string{"affirm", "card", "klarna", "us_bank_account"}}
	}
	r := Run(context.Background(), in)
	n := 0
	for _, p := range r.Problems {
		if strings.Contains(p, "beyond cards and wallets") {
			n++
			for _, m := range []string{"affirm", "klarna", "us_bank_account"} {
				if !strings.Contains(p, m) {
					t.Errorf("the problem line lacks %s: %s", m, p)
				}
			}
		}
	}
	if n != 1 || r.Ready() {
		t.Errorf("%d method problem lines (ready=%v), want exactly one", n, r.Ready())
	}
}
