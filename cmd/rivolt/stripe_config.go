package main

import "strings"

// stripePortalConfigured is the one condition for Stripe's Checkout and Customer Portal routes to exist: a secret key, both prices and the
// public base URL. The plan fields use it too (manage_subscription), so a server with a key but no prices never tells the client to show a
// Manage button whose route is not mounted.
func stripePortalConfigured(getenv func(string) string, publicBaseURL string) bool {
	has := func(name string) bool { return strings.TrimSpace(getenv(name)) != "" }
	return has("RIVOLT_STRIPE_SECRET_KEY") && has("RIVOLT_STRIPE_PRICE_MONTHLY") && has("RIVOLT_STRIPE_PRICE_YEARLY") && strings.TrimSpace(publicBaseURL) != ""
}
