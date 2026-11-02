package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AlertRule is one alerting rule Prometheus has loaded: its name, its expression and whether the last evaluation worked.
type AlertRule struct {
	Name   string
	Query  string
	Health string // "ok", "err" or "unknown"
}

// RequiredRule is a billing alert the flip needs live. QueryHas, when set, must appear in the loaded expression: the same name with an
// older expression is not the fixed rule. (The Stripe webhook 5xx alert existed for a while matching status="5xx" against series whose
// status label is the numeric code, so it could never fire: the name was there, the rule was dead.)
type RequiredRule struct {
	Name     string
	QueryHas string
}

// RequiredAlertRules are the billing alerts that must be loaded for a launch (rivolt-infra rivolt-alerts/manifests billing.yaml and
// accounts.yaml). Names only are ever printed.
var RequiredAlertRules = []RequiredRule{
	{Name: "RivoltBillingOnNoFoundingGrant"},
	{Name: "RivoltEntitlementCountsMissing"},
	{Name: "RivoltStripeWebhookRejected"},
	{Name: "RivoltStripeWebhookUnmapped"},
	{Name: "RivoltStripeWebhookUnmappedExpired"},
	{Name: "RivoltStripeWebhookLag"},
	{Name: "RivoltStripeWebhook5xx", QueryHas: `status=~"5..`},
	{Name: "RivoltStripeRefundApplied"},
	{Name: "RivoltBillingPaymentFailures"},
	{Name: "RivoltBillingNonUS"},
	{Name: "RivoltStripeCancelPending"},
	{Name: "RivoltStripeCancelStuck"},
	{Name: "RivoltDatedNoticeFailed"},
	{Name: "RivoltFoundingNoticeLate"},
	{Name: "RivoltAppleNotificationsRejected"},
	{Name: "RivoltAppleNotificationsUnmapped"},
	{Name: "RivoltBillingStartFailing", QueryHas: `5..`},
}

// AlertRuleFindings is the outcome of comparing the loaded rules with RequiredAlertRules; names only.
type AlertRuleFindings struct {
	Found   []string // loaded, with the expected expression, evaluating
	Missing []string // not loaded at all
	Stale   []string // loaded, but the expression is not the fixed one
	Failing []string // loaded, but the last evaluation failed
}

// CheckAlertRules compares the loaded rules with the required ones.
func CheckAlertRules(loaded []AlertRule, required []RequiredRule) AlertRuleFindings {
	by := map[string]AlertRule{}
	for _, r := range loaded {
		by[r.Name] = r
	}
	var f AlertRuleFindings
	for _, req := range required {
		r, ok := by[req.Name]
		switch {
		case !ok:
			f.Missing = append(f.Missing, req.Name)
		case req.QueryHas != "" && !strings.Contains(r.Query, req.QueryHas):
			f.Stale = append(f.Stale, req.Name)
		case strings.EqualFold(r.Health, "err"):
			f.Failing = append(f.Failing, req.Name)
		default:
			f.Found = append(f.Found, req.Name)
		}
	}
	return f
}

// maxRulesBody bounds what is read from Prometheus' rules endpoint.
const maxRulesBody = 8 << 20

// HTTPAlertRules reads the loaded alerting rules from a Prometheus server (GET /api/v1/rules?type=alert; read-only). The error never carries
// the address or the body.
func HTTPAlertRules(base string) func(context.Context) ([]AlertRule, error) {
	return func(ctx context.Context) ([]AlertRule, error) {
		u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("the Prometheus address is not an http(s) URL")
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String()+"/api/v1/rules?type=alert", nil)
		if err != nil {
			return nil, errors.New("could not build the request")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, errors.New("Prometheus is unreachable")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Prometheus answered %d", resp.StatusCode)
		}
		return parseAlertRules(io.LimitReader(resp.Body, maxRulesBody))
	}
}

func parseAlertRules(r io.Reader) ([]AlertRule, error) {
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Groups []struct {
				Rules []struct {
					Name   string `json:"name"`
					Query  string `json:"query"`
					Type   string `json:"type"`
					Health string `json:"health"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil || body.Status != "success" {
		return nil, errors.New("Prometheus' answer is not a rules listing")
	}
	var out []AlertRule
	for _, g := range body.Data.Groups {
		for _, ru := range g.Rules {
			if ru.Type == "alerting" || ru.Type == "" {
				out = append(out, AlertRule{Name: ru.Name, Query: ru.Query, Health: ru.Health})
			}
		}
	}
	return out, nil
}

// alertRulesSection writes the report lines for the loaded-rules check.
func (r *Report) alertRulesSection(ctx context.Context, in Input) {
	r.line("== Billing alert rules loaded in Prometheus (names only)")
	if in.AlertRules == nil {
		r.line("  skipped (offline, or --no-alert-check)")
		return
	}
	loaded, err := in.AlertRules(ctx)
	if err != nil {
		r.problem("could not read the loaded alert rules, so they are unverified: %v", err)
		return
	}
	req := in.RequiredAlertRules
	if req == nil {
		req = RequiredAlertRules
	}
	f := CheckAlertRules(loaded, req)
	r.line("  %d of %d required rules loaded and evaluating", len(f.Found), len(req))
	if len(f.Found) > 0 {
		sort.Strings(f.Found)
		r.line("  found: %s", strings.Join(f.Found, ", "))
	}
	if len(f.Missing) > 0 {
		r.problem("billing alert rules not loaded: %s", strings.Join(f.Missing, ", "))
	}
	if len(f.Stale) > 0 {
		r.problem("billing alert rules loaded with an old expression (the fix is not live): %s", strings.Join(f.Stale, ", "))
	}
	if len(f.Failing) > 0 {
		r.problem("billing alert rules loaded but failing to evaluate: %s", strings.Join(f.Failing, ", "))
	}
}
