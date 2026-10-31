package main

import "testing"

func TestStripePortalConfigured(t *testing.T) {
	full := map[string]string{"RIVOLT_STRIPE_SECRET_KEY": "k", "RIVOLT_STRIPE_PRICE_MONTHLY": "m", "RIVOLT_STRIPE_PRICE_YEARLY": "y"}
	with := func(drop string) func(string) string {
		return func(n string) string {
			if n == drop {
				return ""
			}
			return full[n]
		}
	}
	for name, c := range map[string]struct {
		get  func(string) string
		base string
		want bool
	}{
		"everything":          {with(""), "https://rivolt.example", true},
		"a key and no prices": {func(n string) string { return map[string]string{"RIVOLT_STRIPE_SECRET_KEY": "k"}[n] }, "https://rivolt.example", false},
		"no yearly price":     {with("RIVOLT_STRIPE_PRICE_YEARLY"), "https://rivolt.example", false},
		"no monthly price":    {with("RIVOLT_STRIPE_PRICE_MONTHLY"), "https://rivolt.example", false},
		"no key":              {with("RIVOLT_STRIPE_SECRET_KEY"), "https://rivolt.example", false},
		"no base url":         {with(""), " ", false},
	} {
		if got := stripePortalConfigured(c.get, c.base); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
