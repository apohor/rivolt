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
	ClearedBy string // the PR, "#1374" (several: "#1365,#1368"); required when Cleared
	// Proof is a Go test function name ("TestX") or a repo-relative path to a client test file; several, comma-separated. Required when Cleared.
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
	{ID: "gate-planner", What: "the multi-day planner and re-plan are Pro on the server",
		Cleared: true, ClearedBy: "#1381", Proof: "TestMultidayNeedsPro,TestProGateRoutesCarryTheirMiddleware,TestProFeatureGatesAgainstPostgres"},
	{ID: "gate-plan-vs-actual", What: "plan-vs-actual is Pro on the server",
		Cleared: true, ClearedBy: "#1381", Proof: "TestProGateRoutesCarryTheirMiddleware,TestProFeatureGatesAgainstPostgres"},
	{ID: "gate-saved-trips", What: "the saved-trips cap is 3 for free; extras stay readable and deletable, not editable",
		Cleared: true, ClearedBy: "#1381", Proof: "TestProFeatureGatesAgainstPostgres"},
	{ID: "gate-vehicles", What: "a free account shows 1 vehicle; the rest are Pro",
		Cleared: true, ClearedBy: "#1381", Proof: "TestVehicleFilterFreeVehicle,TestVehicleListShowsEveryVehicleAndMarksTheExtras,TestLedgerParamsLimitFreeVehicle,TestProFeatureGatesAgainstPostgres"},
	// Unbuilt Pro surfaces (trip postcard, long-form year page, widget pushes) are not listed: there is nothing to gate yet. The route
	// classification carries a permanent guard test that a new Pro surface cannot ship ungated.

	// Lifecycle: what a lapse, a trial end and an account deletion do.
	{ID: "lapse-stops-pushes", What: "on lapse or trial end each running Live Activity gets one end with its final state and its token is deleted, and no further Live Activity pushes are sent (widgets do not exist yet: the Pro-surface guard test covers them)",
		Cleared: true, ClearedBy: "#1381", Proof: "TestLiveActivityEndsOnLapse"},
	{ID: "founding-grant", What: "the founding-year grant job is idempotent and checks grants = accounts - demo; the 14-day trial starts once per account",
		Cleared: true, ClearedBy: "#1384", Proof: "TestFoundingGrantAgainstPostgres,TestNewAccountTrialAgainstPostgres"},
	{ID: "plan-card-fields", What: "/api/auth/me carries plan_cancels_at and plan_grace_ends, with a test for the cancelled and past_due states",
		Cleared: true, ClearedBy: "#1382", Proof: "TestCancelAndGraceStates"},

	// Money: Stripe and StoreKit.
	{ID: "stripe-webhook", What: "Stripe webhook signature is verified and handling is idempotent on the event id; a refund or revoke ends the entitlement and invalidates the plan cache",
		Cleared: true, ClearedBy: "#1390", Proof: "TestVerifySignatureRefuses,TestStripeWebhookAgainstPostgres,TestStripeWebhookRefusesAndRollsBack,TestStripeWebhookEventOrder"},
	{ID: "apple-jws", What: "Apple StoreKit notifications and transactions are verified (JWS chain) and idempotent on the notification id; a refund or revoke ends the entitlement and invalidates the plan cache",
		Cleared: true, ClearedBy: "#1399", Proof: "TestAppleRootIsThePublishedOne,TestVerifyRejects,TestDecide,TestAppleBillingAgainstPostgres"},
	{ID: "account-deletion-cancels-stripe", What: "account deletion cancels the Stripe subscription, with a retry queue and an alert when it cannot"},
	{ID: "billing-emails", What: "the transactional billing emails are sent: the purchase confirmation (renewal terms and how to cancel), the payment-failed / grace notice, and the yearly renewal reminder at least 30 days before renewal"},

	// Terms, consent and clients.
	{ID: "terms-served", What: "the previous Terms (2026-10-23) are served unchanged at /terms/2026-10-23 (the section 11 link) until the new Terms take effect"},
	{ID: "launch-notice", What: "the section 11 launch notice goes out by email AND in-app to every verified existing account, as a service email that ignores the engagement opt-out",
		Cleared: true, ClearedBy: "#1389", Proof: "TestLaunchNoticeApplyIsIdempotentAndEngagementBlind,TestLaunchNoticeAgainstPostgres,TestLaunchNoticeDryRunPrintsCountsOnly"},
	{ID: "founding-expiry-notice", What: "a dated job sends an email plus an in-app notice at least 30 days before each founding year ends",
		Cleared: true, ClearedBy: "#1391", Proof: "TestLateFoundingNoticeIsExtendedAndCounted,TestFoundingNoticeSendsOncePerAccount,TestDatedNoticesAgainstPostgres"},
	{ID: "budget-tier-blind", What: "a test asserts the Rivian rate-limit classes and the poller never consult entitlements, so Pro never means more Rivian calls",
		Cleared: true, ClearedBy: "#1388", Proof: "TestRivianSideNeverConsultsThePlan"},
	{ID: "checkout-consent", What: "the checkout consent line (\"first charge on <date>\") is generated from the Checkout session, with a test",
		Cleared: true, ClearedBy: "#1398", Proof: "TestConsentLine,TestCheckoutDuringATrial,TestBuildCheckoutForANewAccount"},
	{ID: "clients-gated", What: "web and iPhone show Pro UI only when billing_enabled is true (client tests are the proof)",
		Cleared: true, ClearedBy: "#1365,#1368,#1395", Proof: "web/src/lib/plan.test.ts,web/e2e/pro-plan.pw.ts,ios/RivoltTests/ProPaywallTests.swift"},
	{ID: "vehicle-scope-label", What: "web and iPhone show \"Showing <vehicle>. Pro includes all vehicles.\" on every response narrowed to the primary vehicle, read from the X-Rivolt-Vehicle-Scope header, so a narrowed total never reads as the whole fleet's (client tests are the proof)"},
	{ID: "delete-range-ui", What: "web and iPhone offer \"Delete history before a month\" (POST /api/data/delete-range), and the old drive and charge 402 screens keep per-row Delete reachable, so a free user has a path to the privacy exit"},
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
