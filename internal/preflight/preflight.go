// Package preflight is the read-only launch check behind `rivolt launch-preflight`: one report an operator reads before the Rivolt Pro
// flip (docs/LAUNCH_FLIP.md). It prints counts, names and status codes only, never a value of a setting, an id or an address, and it
// switches nothing on: the database part runs in a read-only transaction and the URL part is a GET of public pages.
package preflight

import (
	"context"
	"fmt"
	"io"
	"net/http"
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

// Input is everything the report reads, so a test drives it without a database, a network or the process environment.
type Input struct {
	Getenv func(string) string
	// Founding reads the counts; nil: the database part is skipped and the report says so.
	Founding func(ctx context.Context, launch time.Time) (Founding, error)
	// Fetch returns the status of a GET of url; nil skips the URL checks.
	Fetch  func(ctx context.Context, url string) (int, error)
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
		fmt.Fprintf(w, "\nREADY: nothing stops the flip (%d warning(s)). Nothing was switched on or written.\n", len(r.Warnings))
		return
	}
	fmt.Fprintf(w, "\nNOT READY: %d problem(s). Nothing was switched on or written.\n", len(r.Problems))
}

// URLs are the pages the launch notice and the billing emails link to, checked as the emails state them.
func URLs() []string {
	return []string{
		"https://rivolt.dev/terms",
		"https://rivolt.dev/terms/2026-10-23", // the previous Terms, which the launch notice says apply until the new ones take effect
		"https://rivolt.dev/privacy",
		stripFragment(launchnotice.PlanURL),
		stripFragment(launchnotice.ExportURL),
	}
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

	r.line("== Terms and notice URLs")
	if in.Fetch == nil {
		r.warn("not checked (offline)")
	} else {
		for _, u := range URLs() {
			code, err := in.Fetch(ctx, u)
			switch {
			case err != nil:
				r.problem("%s: %v", u, err)
			case code != http.StatusOK:
				r.problem("%s: HTTP %d, want 200", u, code)
			default:
				r.line("  %s: 200", u)
			}
		}
	}

	r.line("== Billing settings (names and state only, never values)")
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
	if strings.TrimSpace(get("RIVOLT_PUBLIC_BASE_URL")) == "" && strings.TrimSpace(get("RIVOLT_BASE_URL")) == "" {
		r.problem("RIVOLT_PUBLIC_BASE_URL (or RIVOLT_BASE_URL) is not set; Stripe Checkout stays off without it")
	} else {
		r.line("  RIVOLT_PUBLIC_BASE_URL: set")
	}
	r.line("  RIVOLT_STRIPE_AUTOMATIC_TAX: %s", map[bool]string{true: "on", false: "off"}[parseBool(get("RIVOLT_STRIPE_AUTOMATIC_TAX"))])
	stripeMode := ""
	if hasKey {
		stripeMode = keyMode(get("RIVOLT_STRIPE_SECRET_KEY"))
		r.line("  Stripe key mode: %s", stripeMode)
	}
	cfg, err := applebill.ConfigFromEnv(get, true)
	switch {
	case err != nil:
		r.problem("StoreKit is misconfigured: %v", err)
	case cfg == nil:
		r.problem("RIVOLT_APPLE_IAP_BUNDLE_ID is not set; the iPhone app cannot subscribe")
	default:
		r.line("  RIVOLT_APPLE_IAP_BUNDLE_ID: set")
		r.line("  RIVOLT_APPLE_IAP_PRODUCTS: %d product id(s)", len(cfg.Products))
		r.line("  Apple environment: %s", cfg.Environment)
		if (stripeMode == "live" && cfg.Environment == applebill.EnvSandbox) || (stripeMode == "test" && cfg.Environment == applebill.EnvProduction) {
			r.warn("Stripe is in %s mode but Apple is %s", stripeMode, cfg.Environment)
		}
	}
	return r
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// keyMode is "live" or "test" from a Stripe key's public prefix; never any other part of it.
func keyMode(key string) string {
	k := strings.TrimSpace(key)
	for _, p := range []string{"sk_live_", "rk_live_"} {
		if strings.HasPrefix(k, p) {
			return "live"
		}
	}
	for _, p := range []string{"sk_test_", "rk_test_"} {
		if strings.HasPrefix(k, p) {
			return "test"
		}
	}
	return "unrecognised"
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
