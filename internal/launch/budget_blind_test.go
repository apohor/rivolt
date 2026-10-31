package launch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Pro never means more Rivian calls. The Rivian call budget (the shared token buckets and their classes), the poll cadence, the recorder and
// the leases that decide which pod talks to Rivian are the same for a free and a Pro account: the plan decides what the app SHOWS and which
// app features run, never how often Rivolt calls Rivian. internal/entitlements/blind_test.go (#1388) scans the Rivian-side packages for any
// reference to the plan. This file adds the other directions: the set of packages that can read the plan at all is a fixed list a new reader
// has to be added to (and so reviewed), and main.go hands the plan service only to the places that list allows. The behavioural proof is in
// internal/ratelimit (TestBudgetDecisionsDoNotDependOnWhoIsAsking).

// budgetPackages spend or ration the Rivian budget, or decide who talks to Rivian: a plan reader may never be one of them.
var budgetPackages = []string{
	"internal/rivian", "internal/ratelimit", "internal/leases", "internal/fence", "internal/vehiclelease", "internal/breaker",
}

// planReaders are the only non-test packages that may import internal/entitlements, i.e. read an account's plan. A new reader is a decision
// DA reviews: add it here in the same PR, with the reason.
var planReaders = map[string]string{
	"cmd/rivolt":            "builds the plan service and hands it to the API, the Stripe and Apple billing handlers and the push service's Live Activity check",
	"internal/api":          "the plan fields of /api/auth/me, the history window, the Pro feature gates and the billing routes",
	"internal/applebill":    "writes Apple entitlements",
	"internal/stripebill":   "writes Stripe entitlements; reads the plan to refuse a double subscription",
	"internal/billingmail":  "the grace length for the payment-failed email",
	"internal/entitlements": "itself",
}

func goFiles(t *testing.T, root, dir string, includeTests bool) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && (includeTests || !strings.HasSuffix(p, "_test.go")) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func importsOf(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var out []string
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		out = append(out, p)
	}
	return out
}

const modulePath = "github.com/apohor/rivolt/"

// Who can read the plan at all is a fixed list.
func TestOnlyKnownPackagesImportTheEntitlements(t *testing.T) {
	root := repoRoot(t)
	seen := map[string]bool{}
	for _, top := range []string{"internal", "cmd"} {
		for _, f := range goFiles(t, root, top, false) {
			for _, imp := range importsOf(t, f) {
				if imp == modulePath+"internal/entitlements" {
					dir, _ := filepath.Rel(root, filepath.Dir(f))
					seen[filepath.ToSlash(dir)] = true
				}
			}
		}
	}
	for dir := range seen {
		if _, ok := planReaders[dir]; !ok {
			t.Errorf("%s imports internal/entitlements but is not a known plan reader: add it to planReaders in internal/launch/budget_blind_test.go with the reason, and have DA review that it is not on the Rivian budget path", dir)
		}
	}
	for dir := range planReaders {
		for _, b := range budgetPackages {
			if dir == b || strings.HasPrefix(dir, b+"/") {
				t.Errorf("%s is both a plan reader and a budget package", dir)
			}
		}
	}
	if !seen["internal/api"] || !seen["cmd/rivolt"] {
		t.Errorf("the scan found %v: it no longer sees the known readers, so it proves nothing", seen)
	}
}

// main.go gives the plan service only to the places the list allows. A reference on any other line (a Rivian client, the limiter,
// the monitor registry, the leases) fails.
func TestMainHandsThePlanOnlyToTheKnownConsumers(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "cmd/rivolt/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	allowed := []*regexp.Regexp{
		regexp.MustCompile(`^\s*entitlementsSvc := entitlements\.New\(`),
		regexp.MustCompile(`Invalidate: entitlementsSvc\.Invalidate`),       // the Stripe and Apple webhooks drop a cached plan
		regexp.MustCompile(`Plans: entitlementsSvc`),                        // Stripe Checkout refuses a double subscription
		regexp.MustCompile(`pushSvc\.WithProCheck\(entitlementsSvc\.Pro\)`), // Live Activity updates are Pro
		regexp.MustCompile(`^\s*Entitlements:\s+entitlementsSvc,`),          // the API's plan fields, window and feature gates
		regexp.MustCompile(`^\s*//`),                                        // a comment
	}
	refs := 0
	for i, line := range strings.Split(string(src), "\n") {
		if !strings.Contains(line, "entitlementsSvc") {
			continue
		}
		refs++
		ok := false
		for _, re := range allowed {
			if re.MatchString(line) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("cmd/rivolt/main.go:%d hands the plan service to something not on the list: %s", i+1, strings.TrimSpace(line))
		}
	}
	if refs < 5 {
		t.Fatalf("found only %d references to entitlementsSvc: the check no longer sees main's wiring", refs)
	}
	// And no Rivian, rate-limit or lease constructor call mentions the plan on the same statement.
	for i, line := range strings.Split(string(src), "\n") {
		if allowed[0].MatchString(line) {
			continue // the construction itself reads an env flag through rivian.ParseBoolEnv; it hands the service to nothing
		}
		if strings.Contains(line, "entitlementsSvc") && regexp.MustCompile(`\b(rivian|ratelimit|leases|fence)\.[A-Za-z]+\(`).MatchString(line) {
			t.Errorf("cmd/rivolt/main.go:%d passes the plan service into a Rivian/limiter/lease call: %s", i+1, strings.TrimSpace(line))
		}
	}
}
