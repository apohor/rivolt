package arch_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The module boundary lint (zero-downtime design §2.6, S19 9e). Every package under internal/ and cmd/ belongs to one module; every
// non-test import between packages of different modules must be an edge the allowed table permits, or be listed in knownCrossings.
//
// knownCrossings only shrinks: an entry no import needs any more fails the test, so it is deleted in the PR that removes the last use. A new
// crossing is a design change: it goes through review as an edit of allowed, not as a new knownCrossings entry.

const module = "github.com/apohor/rivolt/"

// modules maps each package (its path relative to the repository root) to its module. A package that is not here fails the lint: a new
// package is assigned in the PR that adds it. A path's subpackages belong to it unless they are listed themselves.
var modules = map[string]string{
	// platform: shared, small and stable. A change here counts for every role (S19 9g).
	"internal/db": "platform", "internal/config": "platform", "internal/logging": "platform", "internal/metrics": "platform",
	"internal/tracing": "platform", "internal/crypto": "platform", "internal/flags": "platform", "internal/bg": "platform",
	"internal/singleton": "platform", "internal/clientip": "platform", "internal/histwin": "platform", "internal/histfloor": "platform",
	"internal/testpg": "platform", "internal/rolltest": "platform", "internal/integration": "platform", "internal/ci": "platform",
	"internal/arch": "platform",
	// Leaves with no in-repo imports that several modules use: mail transport, the app link, the LLM adapters, the electricity schedule.
	"internal/email": "platform", "internal/iosapp": "platform", "internal/ai": "platform", "internal/offpeak": "platform",

	// ingest: the Rivian wire and connector.
	"internal/rivian": "ingest", "internal/leases": "ingest", "internal/fence": "ingest", "internal/forward": "ingest",
	"internal/vehiclelease": "ingest", "internal/vehiclediscovery": "ingest", "internal/secrets": "ingest", "internal/ratelimit": "ingest",
	"internal/drain": "ingest", "internal/rivianexpiry": "ingest", "internal/chargewatch": "ingest", "internal/fleetbucket": "ingest",

	// recorder: the sessions and what is derived from them as they close.
	"internal/drives": "recorder", "internal/charges": "recorder", "internal/samples": "recorder", "internal/milestones": "recorder",
	"internal/leftopen": "recorder", "internal/packhealth": "recorder", "internal/tirelow": "recorder", "internal/otadetails": "recorder",
	"internal/receipts": "recorder", "internal/ledger": "recorder", // the money statement: a read model of the recorded charges and drives

	// planner: trips, routing and the map services.
	"internal/trips": "planner", "internal/planleg": "planner", "internal/tripadvice": "planner", "internal/tripmultiday": "planner",
	"internal/tripprofile": "planner", "internal/tripweather": "planner", "internal/syntheticplan": "planner", "internal/chargers": "planner",
	"internal/dcfcrates": "planner", "internal/maps": "planner", "internal/elevation": "planner", "internal/geocoding": "planner",
	"internal/weather": "planner", "internal/zonelookup": "planner", "internal/radar": "planner",

	// billing.
	"internal/stripebill": "billing", "internal/applebill": "billing", "internal/entitlements": "billing", "internal/billingdates": "billing",
	"internal/billingmail": "billing", "internal/statement": "billing", "internal/aibudget": "billing",

	// api: HTTP, identity and the user's own settings.
	"internal/api": "api", "internal/web": "api", "internal/webcarry": "api", "internal/auth": "api", "internal/appauth": "api",
	"internal/kratos": "api", "internal/hydra": "api", "internal/idp": "api", "internal/oidc": "api", "internal/passkeys": "api",
	"internal/appleid": "api", "internal/identity": "api", "internal/sessions": "api", "internal/cfaccess": "api", "internal/adminonly": "api",
	"internal/adminaudit": "api", "internal/adminhealth": "api", "internal/signupgate": "api", "internal/signuprequests": "api",
	"internal/settings": "api", "internal/appsettings": "api", "internal/privacy": "api", "internal/analytics": "api",
	"internal/changelog": "api", "internal/launch": "api", "internal/launchnotice": "api", "internal/preflight": "api",

	// jobs: background work over the stored data, and what it sends.
	"internal/archive": "jobs", "internal/retention": "jobs", "internal/repair": "jobs", "internal/recap": "jobs", "internal/weekly": "jobs",
	"internal/insights": "jobs", "internal/push": "jobs", "internal/onboardmail": "jobs",
	"internal/plugreminder": "jobs", "internal/overnight": "jobs", "internal/r2arrival": "jobs",
	"internal/engage": "jobs", "internal/electrafi": "jobs", "internal/demo": "jobs", "internal/backfill": "jobs",

	// The composition roots: they wire everything.
	"cmd/rivolt": "root", "cmd/replay": "root", "cmd/sampletruck": "root",
}

// allowed is the "May import" column: the modules a module's packages may import, besides their own module.
var allowed = map[string][]string{
	"platform": {},
	"ingest":   {"platform", "recorder"},
	"recorder": {"platform"},
	"planner":  {"platform"},
	"billing":  {"platform"},
	"api":      {"platform", "recorder", "planner", "billing"},
	"jobs":     {"platform", "recorder", "planner", "billing"},
	"root":     {"platform", "ingest", "recorder", "planner", "billing", "api", "jobs"},
}

// allowedPackages are the single packages of another module a module may import: the forward client, the fence, the entitlements check.
// rivian/model (State, Vehicle) joins this list for recorder, planner and api once it is split out of rivian.
var allowedPackages = map[string][]string{
	"recorder": {"internal/fence"},
	"planner":  {"internal/forward", "internal/entitlements"},
	"api":      {"internal/forward"},
	"jobs":     {"internal/forward"},
}

// knownCrossings are today's imports outside the table, "<from package> -> <to package>". It only shrinks.
var knownCrossings = map[string]bool{
	// The rivian model types (State, Vehicle, sessions): most go once rivian/model is split out.
	"internal/api -> internal/rivian":                 true,
	"internal/demo -> internal/rivian":                true,
	"internal/ledger -> internal/rivian":              true,
	"internal/leftopen -> internal/rivian":            true,
	"internal/otadetails -> internal/rivian":          true,
	"internal/planleg -> internal/rivian":             true,
	"internal/receipts -> internal/rivian":            true,
	"internal/repair/chargeenergy -> internal/rivian": true,
	"internal/repair/routegaps -> internal/rivian":    true,
	"internal/repair/routepoints -> internal/rivian":  true,
	"internal/repair/stretched -> internal/rivian":    true,
	"internal/syntheticplan -> internal/rivian":       true,
	"internal/tirelow -> internal/rivian":             true,
	"internal/tripadvice -> internal/rivian":          true,
	"internal/tripmultiday -> internal/rivian":        true,
	// The rest, by importer.
	"internal/api -> internal/archive":               true,
	"internal/api -> internal/demo":                  true,
	"internal/api -> internal/electrafi":             true,
	"internal/api -> internal/engage":                true,
	"internal/api -> internal/insights":              true,
	"internal/api -> internal/onboardmail":           true,
	"internal/api -> internal/overnight":             true,
	"internal/api -> internal/push":                  true,
	"internal/api -> internal/r2arrival":             true,
	"internal/api -> internal/recap":                 true,
	"internal/api -> internal/rivianexpiry":          true,
	"internal/api -> internal/secrets":               true,
	"internal/api -> internal/vehiclelease":          true,
	"internal/api -> internal/weekly":                true,
	"internal/applebill -> internal/auth":            true,
	"internal/appleid -> internal/secrets":           true,
	"internal/billingmail -> internal/launchnotice":  true,
	"internal/db -> internal/fence":                  true,
	"internal/demo -> internal/secrets":              true,
	"internal/demo -> internal/settings":             true,
	"internal/engage -> internal/fence":              true,
	"internal/forward -> internal/settings":          true,
	"internal/ledger -> internal/dcfcrates":          true,
	"internal/metrics -> internal/applebill":         true,
	"internal/metrics -> internal/launchnotice":      true,
	"internal/metrics -> internal/stripebill":        true,
	"internal/milestones -> internal/engage":         true,
	"internal/receipts -> internal/dcfcrates":        true,
	"internal/repair/stretched -> internal/settings": true,
	"internal/rivian -> internal/dcfcrates":          true,
	"internal/rivian -> internal/settings":           true,
	"internal/rivianexpiry -> internal/engage":       true,
	"internal/rivianexpiry -> internal/statement":    true,
	"internal/samples -> internal/archive":           true,
	"internal/samples -> internal/archive/s3":        true,
	"internal/samples -> internal/auth":              true,
	"internal/statement -> internal/ledger":          true,
	"internal/syntheticplan -> internal/settings":    true,
}

// moduleOf returns the module of a package path relative to the repository root, by its longest listed prefix.
func moduleOf(pkg string) (string, bool) {
	for p := pkg; p != "." && p != ""; p = filepath.Dir(p) {
		if m, ok := modules[p]; ok {
			return m, true
		}
	}
	return "", false
}

func permitted(fromMod, toMod, to string) bool {
	if fromMod == toMod {
		return true
	}
	for _, m := range allowed[fromMod] {
		if m == toMod {
			return true
		}
	}
	for _, p := range allowedPackages[fromMod] {
		if p == to {
			return true
		}
	}
	return false
}

// imports reads every non-test .go file under internal/ and cmd/ (all build tags: a file behind a tag still ships in some build) and returns
// each package's in-repo imports.
func imports(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if n := e.Name(); n == "testdata" || n == "dist" || n == "node_modules" || strings.HasPrefix(n, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			rel = filepath.ToSlash(rel)
			if out[rel] == nil {
				out[rel] = map[string]bool{}
			}
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if to, ok := strings.CutPrefix(path, module); ok && to != rel {
					out[rel][to] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// crossings lists the imports outside the table, and the packages no module claims.
func crossings(graph map[string]map[string]bool) (cross []string, unassigned []string) {
	seen := map[string]bool{}
	for from, tos := range graph {
		fromMod, ok := moduleOf(from)
		if !ok {
			if !seen[from] {
				unassigned, seen[from] = append(unassigned, from), true
			}
			continue
		}
		for to := range tos {
			toMod, ok := moduleOf(to)
			if !ok {
				if !seen[to] {
					unassigned, seen[to] = append(unassigned, to), true
				}
				continue
			}
			if !permitted(fromMod, toMod, to) {
				cross = append(cross, from+" -> "+to)
			}
		}
	}
	sort.Strings(cross)
	sort.Strings(unassigned)
	return cross, unassigned
}

func TestModuleBoundaries(t *testing.T) {
	graph := imports(t, filepath.Join("..", ".."))
	cross, unassigned := crossings(graph)
	if os.Getenv("ARCH_PRINT_CROSSINGS") == "1" {
		for _, c := range cross {
			fmt.Printf("\t%q: true,\n", c)
		}
	}
	for _, p := range unassigned {
		t.Errorf("package %s has no module: assign it in modules (internal/arch/boundaries_test.go)", p)
	}
	got := map[string]bool{}
	for _, c := range cross {
		got[c] = true
		if !knownCrossings[c] {
			t.Errorf("new import across a module boundary: %s. Use the module's published surface, or change allowed with review", c)
		}
	}
	var stale []string
	for c := range knownCrossings {
		if !got[c] {
			stale = append(stale, c)
		}
	}
	sort.Strings(stale)
	for _, c := range stale {
		t.Errorf("knownCrossings entry %s is no longer needed: delete it", c)
	}
}

// Every listed path is a package that exists, so a deleted package leaves no entry behind.
func TestModulesListExistingPackages(t *testing.T) {
	for p := range modules {
		if _, err := os.Stat(filepath.Join("..", "..", p)); err != nil {
			t.Errorf("modules lists %s, which does not exist: delete it", p)
		}
	}
	for m := range allowed {
		for _, to := range allowed[m] {
			if _, ok := allowed[to]; !ok {
				t.Errorf("allowed[%s] names an unknown module %s", m, to)
			}
		}
	}
	for m, ps := range allowedPackages {
		for _, p := range ps {
			if _, ok := modules[p]; !ok {
				t.Errorf("allowedPackages[%s] names %s, which is not in modules", m, p)
			}
		}
	}
}

// The lint's own logic, on a made-up graph: an allowed edge passes, a new edge and an unassigned package are found.
func TestCrossingsFindsNewEdgesAndUnassigned(t *testing.T) {
	cross, unassigned := crossings(map[string]map[string]bool{
		"internal/api":       {"internal/drives": true, "internal/forward": true, "internal/db": true},
		"internal/drives":    {"internal/api": true, "internal/fence": true},
		"internal/repair/r2": {"internal/leases": true},
		"internal/newthing":  {"internal/db": true},
	})
	if want := "internal/drives -> internal/api,internal/repair/r2 -> internal/leases"; strings.Join(cross, ",") != want {
		t.Errorf("crossings %v, want %s", cross, want)
	}
	if strings.Join(unassigned, ",") != "internal/newthing" {
		t.Errorf("unassigned %v", unassigned)
	}
}
