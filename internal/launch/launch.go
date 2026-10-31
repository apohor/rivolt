// Package launch is Rivolt Pro's launch-readiness list. RIVOLT_BILLING may not be switched on while any item is pending: the server
// refuses to start (cmd/rivolt/main.go). Selling Pro with half of it missing sells a promise the server does not keep.
//
// Each item is cleared only by the PR that builds it, and a cleared item must name that PR and a proof: a Go test function that exists in
// the repo, or a client test file that exists (the web and iPhone items are client-side; the server list records them as cleared by the
// named PR with the client test as proof). launch_test.go enforces this, so an item cannot be flipped to cleared without its proof.
//
// To clear an item: in the PR that builds it, set Cleared, ClearedBy ("#1234") and Proof. Do not clear an item in a PR that does not
// contain the proof. DA's signed compliance sheet is a second, human gate beside this list; this list is the one the binary checks.
package launch

import "sort"

// Item is one launch requirement.
type Item struct {
	ID   string // stable key
	What string // what must be true before billing is on
	// Cleared is true once the PR that builds the item has merged with its proof.
	Cleared   bool
	ClearedBy string // the PR, "#1374"; required when Cleared
	// Proof is a Go test function name ("TestX") or a repo-relative path to a client test file; required when Cleared.
	Proof string
}

// Items is the list. Order is the order the work lands, not importance.
var Items = []Item{
	// The free plan's limits and the privacy exits.
	{ID: "history-window", What: "every route that returns history is windowed, Pro-only or exempt with a reason (the route classification has no todo)",
		Cleared: true, ClearedBy: "#1369", Proof: "TestEveryRouteIsClassified"},
	{ID: "delete-outside-window", What: "delete-range and per-row delete work on rows outside the free window, for every plan",
		Cleared: true, ClearedBy: "#1374", Proof: "TestDeleteRangeAgainstPostgres"},
	{ID: "history-floor", What: "deleted history is not written back by the receipts sync, catch-up, the drive and charge writers or an archive restore",
		Cleared: true, ClearedBy: "#1377", Proof: "TestHistoryFloor"},

	// Feature gates the paywall copy promises.
	{ID: "gate-planner", What: "the multi-day planner and re-plan are Pro on the server"},
	{ID: "gate-plan-vs-actual", What: "plan-vs-actual is Pro on the server"},
	{ID: "gate-saved-trips", What: "the saved-trips cap is 3 for free; extras stay readable and deletable, not editable"},
	{ID: "gate-vehicles", What: "a free account shows 1 vehicle; the rest are Pro"},
	{ID: "gate-share-cards", What: "the Pro-only share cards are refused to free accounts"},
	{ID: "gate-widgets-live-activity", What: "Pro widgets and Live Activity server pushes are sent to Pro accounts only"},

	// Lifecycle: what a lapse, a trial end and an account deletion do.
	{ID: "lapse-stops-pushes", What: "on lapse or trial end Live Activity pushes stop, that account's Live Activity tokens are deleted and widgets fall back to the free state"},
	{ID: "founding-grant", What: "the founding-year grant job is idempotent and checks grants = accounts - demo; the 14-day trial starts once per account"},
	{ID: "plan-card-fields", What: "/api/auth/me carries plan_cancels_at and plan_grace_ends, with a test for the cancelled and past_due states"},

	// Money: Stripe and StoreKit.
	{ID: "stripe-webhook", What: "Stripe webhook signature is verified and handling is idempotent on the event id"},
	{ID: "apple-jws", What: "Apple StoreKit notifications and transactions are verified (JWS chain) and idempotent on the notification id"},
	{ID: "account-deletion-cancels-stripe", What: "account deletion cancels the Stripe subscription, with a retry queue and an alert when it cannot"},
	{ID: "billing-emails", What: "transactional billing emails are sent, and the renewal reminder job runs at least 30 days before renewal"},

	// Terms, consent and clients.
	{ID: "terms-served", What: "the Terms version that covers paid plans (/terms/2026-10-23) is served"},
	{ID: "checkout-consent", What: "the checkout consent line (\"first charge on <date>\") is generated from the Checkout session, with a test"},
	{ID: "clients-gated", What: "web and iPhone show Pro UI only when billing_enabled is true (client tests are the proof)"},
}

// Pending returns the IDs of the items not yet cleared, in list order.
func Pending() []string {
	var out []string
	for _, it := range Items {
		if !it.Cleared {
			out = append(out, it.ID)
		}
	}
	return out
}

// Gate returns what stops the server from starting: nothing while billing is off, the pending items when it is on.
func Gate(billingOn bool) []string {
	if !billingOn {
		return nil
	}
	return Pending()
}

// Cleared reports whether the item with this ID is cleared; an unknown ID is not.
func Cleared(id string) bool {
	for _, it := range Items {
		if it.ID == id {
			return it.Cleared
		}
	}
	return false
}

// IDs returns every ID, sorted (tests).
func IDs() []string {
	var out []string
	for _, it := range Items {
		out = append(out, it.ID)
	}
	sort.Strings(out)
	return out
}
