// Package preflight is the read-only launch check behind `rivolt launch-preflight`: one report an operator reads before the Rivolt Pro
// flip (the launch runbook: rivolt-infra docs/launch-flip.md). It prints counts, names and status codes only, never a value of a setting, an id or an address, and it
// switches nothing on: the database part runs in a read-only transaction and the URL part is a GET of public pages.
package preflight

import (
	"context"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/applebill"
	"github.com/apohor/rivolt/internal/launch"
	"github.com/apohor/rivolt/internal/launchnotice"
)

// Founding is what the founding grant would do at the launch instant, counts only.
type Founding struct {
	Accounts int // every account
	Demo     int // 1 when the demo account exists
	Eligible int // accounts created at or before launch, the demo excluded
	Already  int // of those, with a founding row
	ToGrant  int
	After    int // accounts created after the launch instant (not granted)
}

// StripeFacts is what the Stripe API says, booleans and a mode only. Err is set when the key could not read it (sanitised, no key).
type StripeFacts struct {
	Err                 error
	Live                bool // the key and the prices are in live mode
	PricesActive        bool // both prices exist and are active
	PortalFound         bool
	TermsSet            bool     // the Customer Portal's Terms of service URL
	PrivacySet          bool     // and its Privacy policy URL
	PaymentMethodUpdate bool     // customers can update their payment method in the portal
	CheckoutMethods     []string // the payment method types the default configuration offers in Checkout (sorted)
	CheckoutRead        bool     // that configuration could be read at all
	TaxRead             bool     // Stripe Tax's settings were read (only asked when automatic tax is on)
	TaxActive           bool     // and Stripe Tax is active in this mode
}

// Input is everything the report reads, so a test drives it without a database, a network or the process environment.
type Input struct {
	Getenv func(string) string
	// Founding reads the counts; nil: the database part is skipped and the report says so.
	Founding func(ctx context.Context, launch time.Time) (Founding, error)
	// Fetch returns the status of a GET of url; nil skips the URL checks.
	Fetch func(ctx context.Context, url string) (int, error)
	// Webhook returns the status of an UNSIGNED, empty POST to url (Stripe's webhook endpoint); nil skips it.
	Webhook func(ctx context.Context, url string) (int, error)
	// RedisTime runs TIME as the app's Redis user, which is what the shared rate-limit script needs; nil skips it. It reports a denial
	// (the ACL) apart from the other failures.
	RedisTime func(ctx context.Context) (denied bool, err error)
	// Stripe reads the Stripe API with the configured key; nil skips it.
	Stripe func(ctx context.Context) StripeFacts
	// Pinned are the payment method types our Checkout sessions carry (stripebill.PinnedPaymentMethods): with them the default configuration
	// cannot put another method into our sessions. Empty means the sessions do not pin, and then the default configuration decides.
	Pinned []string
	Launch time.Time
}

// Report is the result: lines to print and whether the deployment is ready for the flip.
type Report struct {
	Lines []string
	// Problems are what stops the flip; Warnings are worth a look but do not.
	Problems []string
	Warnings []string
}

// Ready is true when nothing stops the flip.
func (r *Report) Ready() bool { return len(r.Problems) == 0 }

func (r *Report) line(format string, a ...any) { r.Lines = append(r.Lines, fmt.Sprintf(format, a...)) }
func (r *Report) problem(format string, a ...any) {
	m := fmt.Sprintf(format, a...)
	r.Problems = append(r.Problems, m)
	r.Lines = append(r.Lines, "  PROBLEM: "+m)
}
func (r *Report) warn(format string, a ...any) {
	m := fmt.Sprintf(format, a...)
	r.Warnings = append(r.Warnings, m)
	r.Lines = append(r.Lines, "  warning: "+m)
}

// Print writes the report.
func (r *Report) Print(w io.Writer) {
	for _, l := range r.Lines {
		fmt.Fprintln(w, l)
	}
	if r.Ready() {
		fmt.Fprintf(w, "\nREADY: nothing this report can see stops the flip (%d warning(s)); the manual steps above are still yours. Nothing was switched on or written.\n", len(r.Warnings))
	} else {
		fmt.Fprintf(w, "\nNOT READY: %d problem(s). Nothing was switched on or written.\n", len(r.Problems))
	}
	fmt.Fprintln(w, "These are production figures: share the report privately with DA and the lead; never in a GitHub PR, issue, commit or release note.")
}

// URLs are the pages the launch notice and the billing emails link to, on this deployment's own public base URL (the emails themselves carry
// rivolt.dev). Fragments are dropped; the Plan card and the export page are one path.
func URLs(base string) []string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	var out []string
	seen := map[string]bool{}
	for _, p := range []string{"/terms", "/terms/2026-10-23" /* the previous Terms, which the launch notice says apply until the new ones take effect */, "/privacy", pathOf(launchnotice.PlanURL), pathOf(launchnotice.ExportURL)} {
		if u := base + p; !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// EmailURLs are the pages the emails and the launch notice link to, exactly as they state them (rivolt.dev), fragments dropped.
func EmailURLs() []string { return URLs("https://rivolt.dev") }

// pathOf is the path of an absolute URL, without its fragment.
func pathOf(u string) string {
	p, err := neturl.Parse(u)
	if err != nil {
		return ""
	}
	return p.Path
}

func stripFragment(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}

// Run builds the report.
func Run(ctx context.Context, in Input) *Report {
	r := &Report{}
	get := in.Getenv
	if get == nil {
		get = func(string) string { return "" }
	}
	billingOn := parseBool(get("RIVOLT_BILLING"))
	baseURL := strings.TrimRight(strings.TrimSpace(firstNonEmpty(get("RIVOLT_PUBLIC_BASE_URL"), get("RIVOLT_BASE_URL"))), "/")

	r.line("== Launch-readiness list (internal/launch)")
	pending := launch.Pending()
	r.line("  %d of %d items cleared", len(launch.Items)-len(pending), len(launch.Items))
	if len(pending) > 0 {
		r.problem("the server refuses RIVOLT_BILLING=true while these are pending: %s", strings.Join(pending, ", "))
	}

	r.line("== Founding grant (dry run, launch %s)", in.Launch.UTC().Format(time.RFC3339))
	switch {
	case in.Founding == nil:
		r.warn("not checked (no database)")
	default:
		f, err := in.Founding(ctx, in.Launch)
		if err != nil {
			r.problem("the founding dry run failed: %v", err)
			break
		}
		r.line("  accounts: %d, demo: %d, eligible (created by launch, demo excluded): %d", f.Accounts, f.Demo, f.Eligible)
		r.line("  already founding: %d, to grant: %d, founding rows after the grant: %d", f.Already, f.ToGrant, f.Eligible)
		if f.Accounts-f.Demo != f.Eligible+f.After {
			r.problem("accounts minus the demo (%d) is not eligible (%d) plus created after launch (%d)", f.Accounts-f.Demo, f.Eligible, f.After)
		}
		if f.After > 0 {
			r.warn("%d account(s) were created after the launch instant and would not be granted; use a launch time at or after the newest account", f.After)
		}
		if f.Eligible == 0 {
			r.warn("no eligible accounts")
		}
	}

	// The SPA answers 200 for ANY path, so a 200 here proves the site answers through Cloudflare, not that the page exists: the routes
	// themselves are covered by tests (the frozen Terms hash and the terms-served launch item). A bot challenge from Cloudflare (403)
	// shows as NOT READY, which fails closed.
	r.line("== Deployment reachable (RIVOLT_PUBLIC_BASE_URL; the SPA answers 200 for any path, so this proves reachability only; the routes are covered by tests)")
	if in.Fetch == nil {
		r.warn("not checked (offline)")
	} else if baseURL == "" {
		r.warn("not checked (no public base URL)")
	} else {
		r.line("  host: %s", hostOf(baseURL))
		for _, u := range URLs(baseURL) {
			code, err := in.Fetch(ctx, u)
			switch {
			case err != nil:
				r.problem("%s: %v", u, err)
			case code != http.StatusOK:
				r.problem("%s: HTTP %d, want 200 (a 403 is usually Cloudflare challenging this client)", u, code)
			default:
				r.line("  %s: reachable (HTTP 200)", u)
			}
		}
		if code, err := in.Fetch(ctx, baseURL+"/preflight-no-such-page"); err == nil && code == http.StatusOK {
			r.line("  a made-up path also answers 200: as expected for the SPA, so the lines above prove reachability only")
		}
	}

	// The emails and the launch notice link to rivolt.dev whatever the deployment's own base URL is, so those links are checked as stated.
	r.line("== Email links (as the emails state them: %s)", hostOf(EmailURLs()[0]))
	if in.Fetch == nil {
		r.warn("not checked (offline)")
	} else {
		for _, u := range EmailURLs() {
			code, err := in.Fetch(ctx, u)
			switch {
			case err != nil:
				r.problem("%s: %v", u, err)
			case code != http.StatusOK:
				r.problem("%s: HTTP %d, want 200", u, code)
			default:
				r.line("  %s: reachable (HTTP 200)", u)
			}
		}
	}

	r.line("== Billing settings (names and present or absent only, never values)")
	r.line("  RIVOLT_BILLING: %s", map[bool]string{true: "on", false: "off"}[billingOn])
	if billingOn {
		r.warn("billing is already on")
	}
	present := func(name string, required bool) bool {
		ok := strings.TrimSpace(get(name)) != ""
		switch {
		case ok:
			r.line("  %s: set", name)
		case required:
			r.problem("%s is not set", name)
		default:
			r.line("  %s: not set (optional)", name)
		}
		return ok
	}
	hasKey := present("RIVOLT_STRIPE_SECRET_KEY", true)
	present("RIVOLT_STRIPE_WEBHOOK_SECRET", true)
	present("RIVOLT_STRIPE_PRICE_MONTHLY", true)
	present("RIVOLT_STRIPE_PRICE_YEARLY", true)
	if baseURL == "" {
		r.problem("RIVOLT_PUBLIC_BASE_URL (or RIVOLT_BASE_URL) is not set; Stripe Checkout stays off without it")
	} else {
		r.line("  RIVOLT_PUBLIC_BASE_URL: set")
	}
	r.line("  RIVOLT_STRIPE_AUTOMATIC_TAX: %s", map[bool]string{true: "on", false: "off"}[parseBool(get("RIVOLT_STRIPE_AUTOMATIC_TAX"))])
	cfg, err := applebill.ConfigFromEnv(get, true)
	switch {
	case err != nil:
		r.problem("StoreKit is misconfigured: %v", err)
	case cfg == nil:
		r.problem("RIVOLT_APPLE_IAP_BUNDLE_ID is not set; the iPhone app cannot subscribe")
	default:
		r.line("  RIVOLT_APPLE_IAP_BUNDLE_ID: set")
		r.line("  RIVOLT_APPLE_IAP_PRODUCTS: set")
		r.line("  RIVOLT_APPLE_IAP_ENV: set")
	}

	r.line("== Stripe webhook endpoint")
	switch {
	case in.Webhook == nil:
		r.warn("not checked (offline)")
	case baseURL == "":
		r.warn("not checked (no public base URL)")
	default:
		// An unsigned POST must reach the handler and be refused for its signature: 400. A redirect (302) or a block (403) means Stripe's
		// events would never arrive.
		code, err := in.Webhook(ctx, baseURL+"/api/billing/stripe/webhook")
		switch {
		case err != nil:
			r.problem("the webhook endpoint is unreachable: %v", err)
		case code != http.StatusBadRequest:
			r.problem("an unsigned POST to the webhook endpoint got HTTP %d, want 400 (a 302 or 403 means Cloudflare is redirecting or blocking Stripe: Stripe's events would never arrive)", code)
		default:
			r.line("  unsigned POST to %s: 400 (the handler refuses it for its signature)", hostOf(baseURL))
		}
	}

	r.line("== Redis ACL for the app user")
	switch {
	case strings.TrimSpace(get("RIVOLT_REDIS_ADDR")) == "":
		r.warn("RIVOLT_REDIS_ADDR is not set: the limiters count per pod")
	case in.RedisTime == nil:
		r.warn("not checked (no Redis client)")
	default:
		denied, err := in.RedisTime(ctx)
		switch {
		case denied:
			r.problem("the app user's ACL denies TIME (the rate-limit script needs +time): every limiter would fall back to per-pod buckets")
		case err != nil:
			r.warn("could not run TIME: %v", err)
		default:
			r.line("  TIME as the app user: allowed")
		}
	}

	r.line("== Stripe account (read through the API with the configured key)")
	switch {
	case !hasKey:
		r.line("  skipped: no secret key")
	case in.Stripe == nil:
		r.warn("not checked (offline)")
	default:
		f := in.Stripe(ctx)
		if f.Err != nil {
			// Not verified is not ready: a restricted key needs read access on Prices, Customer Portal configurations and Payment method configurations.
			r.problem("could not read the Stripe account, so it is unverified: %v (a restricted key needs read access on Prices, Customer Portal configurations and Payment method configurations)", f.Err)
			break
		}
		r.line("  mode: %s", map[bool]string{true: "live", false: "test"}[f.Live])
		if !f.PricesActive {
			r.problem("a configured price is missing or not active")
		}
		if !f.PortalFound {
			r.problem("no default Customer Portal configuration (Plan card -> Update card / Cancel would fail)")
		} else {
			if !f.TermsSet {
				r.problem("the Customer Portal has no Terms of service URL")
			}
			if !f.PrivacySet {
				r.problem("the Customer Portal has no Privacy policy URL")
			}
			if !f.PaymentMethodUpdate {
				r.problem("the Customer Portal does not let customers update their payment method")
			}
			if f.TermsSet && f.PrivacySet && f.PaymentMethodUpdate {
				r.line("  portal: Terms URL set, Privacy URL set, payment-method update on")
			}
		}
		r.checkoutMethods(f, in.Pinned)
		if parseBool(get("RIVOLT_STRIPE_AUTOMATIC_TAX")) {
			switch {
			case !f.TaxRead:
				r.problem("automatic tax is on but Stripe Tax's settings could not be read, so they are unverified (a restricted key needs read access on Tax settings)")
			case !f.TaxActive:
				r.problem("automatic tax is on (RIVOLT_STRIPE_AUTOMATIC_TAX) but Stripe Tax is not active in this mode: live Checkout would fail")
			default:
				r.line("  Stripe Tax: active")
			}
		}
		if cfg != nil && ((f.Live && cfg.Environment == applebill.EnvSandbox) || (!f.Live && cfg.Environment == applebill.EnvProduction)) {
			r.warn("the Stripe mode and the Apple environment disagree (one live, one not)")
		}
	}
	r.line("== Manual steps (this report cannot see these; none of them is counted as a pass)")
	for _, m := range ManualSteps() {
		r.line("  [ ] %s", m)
	}
	return r
}

// allowedCheckoutMethods are the payment method types Checkout may offer: cards and wallets. ACH, buy-now-pay-later and the delayed or
// redirect methods have refund and dispute flows that do not match the 14-day refund and dispute copy.
var allowedCheckoutMethods = map[string]bool{"card": true, "apple_pay": true, "google_pay": true, "link": true}

func (r *Report) checkoutMethods(f StripeFacts, pinned []string) {
	pinOK := len(pinned) > 0
	for _, m := range pinned {
		if !allowedCheckoutMethods[m] {
			pinOK = false
		}
	}
	if !pinOK {
		r.problem("our Checkout sessions do not pin payment_method_types to cards and wallets (card, link): the Dashboard's default configuration would decide what Checkout offers")
	} else {
		r.line("  Checkout sessions pin payment methods: %s", strings.Join(pinned, ", "))
	}
	if !f.CheckoutRead {
		r.warn("could not read the default payment method configuration (a restricted key needs read access on Payment method configurations); it does not decide our sessions while they pin")
		return
	}
	r.line("  default configuration offers: %s", strings.Join(f.CheckoutMethods, ", "))
	card := false
	var extra []string
	for _, m := range f.CheckoutMethods {
		if m == "card" {
			card = true
		}
		if !allowedCheckoutMethods[m] {
			extra = append(extra, m)
		}
	}
	if len(extra) > 0 {
		msg := fmt.Sprintf("the default configuration offers %d method(s) beyond cards and wallets: %s; our Checkout sessions pin %s", len(extra), strings.Join(extra, ", "), strings.Join(pinned, ", "))
		if pinOK {
			r.warn("%s", msg)
		} else {
			r.problem("%s", msg)
		}
	}
	if !card {
		r.problem("the default configuration does not offer cards, which our Checkout sessions pin")
	}
}

// ManualSteps are the launch prerequisites that live in the Stripe Dashboard, Cloudflare and the Valkey pod, where this command has no view.
func ManualSteps() []string {
	return []string{
		"Stripe Dashboard, public business details: the Terms of service URL is set (Checkout answers 400 without it); open a test-mode Checkout to see it",
		"Stripe Dashboard, Customer Portal: configured as above, with cancel, plan switch and payment-method update on",
		"Stripe Dashboard, Developers: the webhook endpoint is subscribed to checkout.session.completed, customer.subscription.created, customer.subscription.updated, customer.subscription.deleted and charge.refunded",
		"Cloudflare: a skip (allow) rule lets Stripe's servers reach /api/billing/stripe/webhook on rivolt.dev (a managed challenge or WAF block there is the 302/403 case above)",
		"Stripe Tax is set up in live mode: origin address, and registrations where required (Texas first), since RIVOLT_STRIPE_AUTOMATIC_TAX is on; with no live tax setup, live Checkout fails",
		"Valkey: ask ops to confirm the app user's rule with ACL DRYRUN <app user> TIME (the TIME line above runs it as the app user, which is the same answer)",
		"before the flip: the Stripe key is in live mode on prod and the Apple environment is the production one (the mode line above comes from the Stripe API)",
	}
}

// hostOf is the host of a URL: which deployment a probe hit. A host is not a secret.
func hostOf(u string) string {
	p, err := neturl.Parse(u)
	if err != nil || p.Host == "" {
		return "(unparsed)"
	}
	return p.Host
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// HTTPFetch GETs url with a short timeout and reports the status after redirects; the body is not read.
func HTTPFetch(ctx context.Context, url string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("unreachable")
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// HTTPPostUnsigned POSTs an empty body to url without following redirects and reports the status: what Stripe would get for an event with
// no signature.
func HTTPPostUnsigned(ctx context.Context, url string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return 0, fmt.Errorf("unreachable")
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
