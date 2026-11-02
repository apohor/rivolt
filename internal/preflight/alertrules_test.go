package preflight

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func allLoaded() []AlertRule {
	var out []AlertRule
	for _, r := range RequiredAlertRules {
		q := "increase(x[5m]) > 0"
		if r.QueryHas != "" {
			q = `increase(rivolt_http_requests_total{route="/x", status=~"5.."}[15m]) > 0`
		}
		out = append(out, AlertRule{Name: r.Name, Query: q, Health: "ok"})
	}
	return out
}

func TestCheckAlertRulesFindsMissingStaleAndFailing(t *testing.T) {
	f := CheckAlertRules(allLoaded(), RequiredAlertRules)
	if len(f.Found) != len(RequiredAlertRules) || len(f.Missing)+len(f.Stale)+len(f.Failing) != 0 {
		t.Fatalf("all loaded: %+v", f)
	}
	loaded := allLoaded()
	var kept []AlertRule
	for _, r := range loaded {
		switch r.Name {
		case "RivoltStripeCancelStuck":
			continue // missing
		case "RivoltStripeWebhook5xx":
			r.Query = `increase(rivolt_http_requests_total{route="/api/billing/stripe/webhook", status="5xx"}[15m]) > 0` // the dead expression
		case "RivoltBillingNonUS":
			r.Health = "err"
		}
		kept = append(kept, r)
	}
	f = CheckAlertRules(kept, RequiredAlertRules)
	if strings.Join(f.Missing, ",") != "RivoltStripeCancelStuck" || strings.Join(f.Stale, ",") != "RivoltStripeWebhook5xx" || strings.Join(f.Failing, ",") != "RivoltBillingNonUS" {
		t.Errorf("findings = %+v", f)
	}
	if len(f.Found) != len(RequiredAlertRules)-3 {
		t.Errorf("found %d, want %d", len(f.Found), len(RequiredAlertRules)-3)
	}
}

func TestPreflightReportsRulesByNameAndIsNotReadyWhenOneIsMissing(t *testing.T) {
	rules := allLoaded()[1:] // the first required rule is not loaded
	rules[0].Query = "secret_looking_expression_" + "must_not_be_printed"
	in := Input{AlertRules: func(context.Context) ([]AlertRule, error) { return rules, nil }}
	rep := Run(context.Background(), in)
	var b strings.Builder
	rep.Print(&b)
	out := b.String()
	if rep.Ready() {
		t.Fatalf("a missing rule left the preflight ready:\n%s", out)
	}
	if !strings.Contains(out, "billing alert rules not loaded: "+RequiredAlertRules[0].Name) || !strings.Contains(out, "required rules loaded and evaluating") {
		t.Errorf("the report does not name the missing rule:\n%s", out)
	}
	if strings.Contains(out, "must_not_be_printed") || strings.Contains(out, "increase(") {
		t.Errorf("an expression was printed (names only):\n%s", out)
	}
}

func TestPreflightRulesAllLoadedAddsNoProblemAndAnErrorIsAProblem(t *testing.T) {
	problemsWith := func(f func(context.Context) ([]AlertRule, error)) []string {
		var p []string
		for _, s := range Run(context.Background(), Input{AlertRules: f}).Problems {
			if strings.Contains(s, "alert rules") {
				p = append(p, s)
			}
		}
		return p
	}
	if p := problemsWith(func(context.Context) ([]AlertRule, error) { return allLoaded(), nil }); len(p) != 0 {
		t.Errorf("all loaded: %v", p)
	}
	if p := problemsWith(func(context.Context) ([]AlertRule, error) { return nil, errors.New("Prometheus is unreachable") }); len(p) != 1 || !strings.Contains(p[0], "unverified") {
		t.Errorf("an unreadable listing must be a problem: %v", p)
	}
	if p := problemsWith(func(context.Context) ([]AlertRule, error) { return nil, nil }); len(p) != 1 {
		t.Errorf("an empty listing must name every rule as missing: %v", p)
	}
	if p := problemsWith(nil); len(p) != 0 {
		t.Errorf("skipped (nil) must not be a problem: %v", p)
	}
}

const rulesJSON = `{"status":"success","data":{"groups":[
 {"name":"billing","rules":[
  {"name":"RivoltStripeWebhook5xx","query":"increase(x{status=~\"5..\"}[15m]) > 0","type":"alerting","health":"ok"},
  {"name":"SomeRecordingRule","query":"sum(x)","type":"recording","health":"ok"}]},
 {"name":"accounts","rules":[{"name":"RivoltBillingNonUS","query":"y > 0","type":"alerting","health":"err"}]}]}}`

func TestHTTPAlertRulesReadsTheListingWithAGetOnly(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(rulesJSON))
	}))
	defer srv.Close()
	rules, err := HTTPAlertRules(srv.URL + "/")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/rules" || gotQuery != "type=alert" {
		t.Errorf("request = %s %s?%s", gotMethod, gotPath, gotQuery)
	}
	if len(rules) != 2 || rules[0].Name != "RivoltStripeWebhook5xx" || rules[1].Health != "err" {
		t.Errorf("rules = %+v (the recording rule must be left out)", rules)
	}
}

func TestHTTPAlertRulesRefusesWhatIsNotAListing(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"a 500":    func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
		"not json": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"an error": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"error","error":"x"}`)) },
		"an array": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) },
	} {
		srv := httptest.NewServer(handler)
		_, err := HTTPAlertRules(srv.URL)(context.Background())
		srv.Close()
		if err == nil {
			t.Errorf("%s was accepted", name)
		} else if strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "boom") {
			t.Errorf("%s: the error carries the address or the body: %v", name, err)
		}
	}
	for _, bad := range []string{"", "ftp://x", "not a url", "http://"} {
		if _, err := HTTPAlertRules(bad)(context.Background()); err == nil {
			t.Errorf("address %q was accepted", bad)
		}
	}
}
