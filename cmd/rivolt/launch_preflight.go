package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/preflight"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/stripebill"
)

// runLaunchPreflight prints the read-only report an operator reads before the Rivolt Pro flip (the launch runbook: rivolt-infra docs/launch-flip.md): the launch-readiness
// list, the founding grant's dry-run counts, whether the Terms and notice pages answer, which billing settings are present (names and
// state only, never a value) and whether the billing alert rules are loaded in Prometheus (rule names only; a missing, stale or failing one is a problem). It reads the process environment, so run it where the server's settings are (the app pod). It writes nothing
// and switches nothing on; exit 0 when nothing stops the flip, 1 otherwise, 2 for usage.
func runLaunchPreflight(args []string) int {
	fs := flag.NewFlagSet("launch-preflight", flag.ContinueOnError)
	launchFlag := fs.String("launch", "", "launch instant, RFC3339 (default: now): the founding grant counts accounts created at or before it")
	offline := fs.Bool("offline", false, "skip the URL checks (no network)")
	noDB := fs.Bool("no-db", false, "skip the founding dry run (no database)")
	promURL := fs.String("prometheus-url", "", "Prometheus base URL, to check the billing alert rules are loaded (default: RIVOLT_PROMETHEUS_URL)")
	noAlerts := fs.Bool("no-alert-check", false, "skip the loaded-alert-rules check (a deployment without Prometheus)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	launch := time.Now().UTC()
	if *launchFlag != "" {
		t, err := time.Parse(time.RFC3339, *launchFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "launch-preflight: --launch must be an RFC3339 time")
			return 2
		}
		launch = t
	}
	in := preflight.Input{Getenv: os.Getenv, Launch: launch}
	ctx := context.Background()
	if !*noDB {
		pool, err := db.OpenNoMigrate(ctx, postgresDSN())
		if err != nil {
			fmt.Fprintln(os.Stderr, "launch-preflight: postgres:", err)
			return 1
		}
		defer pool.Close()
		if err := db.RequireTables(ctx, pool, "users", "entitlements"); err != nil {
			fmt.Fprintln(os.Stderr, "launch-preflight:", err)
			return 1
		}
		in.Founding = preflight.FoundingFromDB(pool, demo.UserID())
	}
	if !*offline {
		in.Fetch = preflight.HTTPFetch
		in.Webhook = preflight.HTTPPostUnsigned
		in.Stripe = preflightStripe(os.Getenv)
		in.BillingAddress = stripebill.BillingAddressCollection
	}
	in.AlertRules = alertRulesSource(*promURL, os.Getenv("RIVOLT_PROMETHEUS_URL"), *offline || *noAlerts)
	if addr := strings.TrimSpace(os.Getenv("RIVOLT_REDIS_ADDR")); addr != "" && !*offline {
		rdb := redis.NewClient(redisOptions(addr, os.Getenv))
		defer func() { _ = rdb.Close() }()
		in.RedisTime = func(ctx context.Context) (bool, error) {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			// The rate-limit script reads the server's clock; if the app user's ACL denies it, every limiter falls back to its pod.
			err := rdb.Do(ctx, "TIME").Err()
			if err != nil && strings.Contains(err.Error(), "NOPERM") {
				return true, err
			}
			if err != nil {
				return false, fmt.Errorf("redis unreachable or refused")
			}
			return false, nil
		}
	}
	rep := preflight.Run(ctx, in)
	rep.Print(os.Stdout)
	if !rep.Ready() {
		return 1
	}
	return 0
}

// preflightStripe reads the Stripe API with the configured key (read-only GETs): the mode and activity of both prices and the default
// Customer Portal configuration's Terms and Privacy URLs and payment-method setting. A key that cannot read it yields an error with no
// key in it.
func preflightStripe(getenv func(string) string) func(context.Context) preflight.StripeFacts {
	key := strings.TrimSpace(getenv("RIVOLT_STRIPE_SECRET_KEY"))
	return func(ctx context.Context) preflight.StripeFacts {
		var f preflight.StripeFacts
		if key == "" {
			return f
		}
		c := &stripebill.Client{SecretKey: key}
		f.Live, f.PricesActive = true, true
		for _, name := range []string{"RIVOLT_STRIPE_PRICE_MONTHLY", "RIVOLT_STRIPE_PRICE_YEARLY"} {
			id := strings.TrimSpace(getenv(name))
			if id == "" {
				f.PricesActive = false
				continue
			}
			live, active, err := c.PriceMode(ctx, id)
			if err != nil {
				f.Err = err
				return f
			}
			f.Live = f.Live && live
			f.PricesActive = f.PricesActive && active
		}
		pf, err := c.PortalFacts(ctx)
		if err != nil {
			f.Err = err
			return f
		}
		f.PortalFound, f.TermsSet, f.PrivacySet, f.PaymentMethodUpdate = pf.Found, pf.TermsSet, pf.PrivacySet, pf.PaymentMethodUpdate
		// The Checkout payment methods are a separate permission: a key that cannot read them leaves CheckoutRead false, which the report
		// counts as unverified.
		if methods, found, err := c.EnabledCheckoutMethods(ctx); err == nil && found {
			f.CheckoutRead, f.CheckoutMethods = true, methods
		}
		// The configuration Checkout names (RIVOLT_STRIPE_PAYMENT_METHOD_CONFIG): found, active and what it offers.
		if id := strings.TrimSpace(getenv("RIVOLT_STRIPE_PAYMENT_METHOD_CONFIG")); id != "" {
			if cfg, found, err := c.PaymentMethodConfiguration(ctx, id); err == nil {
				f.PMCRead, f.PMCFound, f.PMCActive, f.PMCLive, f.PMCMethods = true, found, cfg.Active, cfg.Live, cfg.Methods
			}
		}
		// Stripe Tax is only asked about when automatic tax is on; a key that cannot read it leaves TaxRead false (unverified).
		if rivian.ParseBoolEnv(getenv("RIVOLT_STRIPE_AUTOMATIC_TAX")) {
			if active, err := c.TaxActive(ctx); err == nil {
				f.TaxRead, f.TaxActive = true, active
			}
		}
		return f
	}
}

// alertRulesSource picks where the loaded alert rules are read from: nil to skip the check (offline, or told to), the flag's address, else
// the environment's. With neither it fails closed: the rules are unverified, which the report counts as a problem, not a skip.
func alertRulesSource(flagAddr, envAddr string, skip bool) func(context.Context) ([]preflight.AlertRule, error) {
	if skip {
		return nil
	}
	addr := strings.TrimSpace(flagAddr)
	if addr == "" {
		addr = strings.TrimSpace(envAddr)
	}
	if addr == "" {
		return func(context.Context) ([]preflight.AlertRule, error) {
			return nil, fmt.Errorf("no Prometheus address (pass --prometheus-url, or --no-alert-check where there is no Prometheus)")
		}
	}
	return preflight.HTTPAlertRules(addr)
}
