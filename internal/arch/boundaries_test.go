package arch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The module boundary lint (zero-downtime design §2.6, S19 9e). Every package under internal/ and cmd/ belongs to one module; every
// non-test import between packages of different modules must be an edge the allowed table permits, or be listed in knownCrossings.
//
// knownCrossings only shrinks: an entry no import needs any more fails the test, so it is deleted in the PR that removes the last use. A new
// crossing is a design change: it goes through review as an edit of allowed, not as a new knownCrossings entry.

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
	graph, err := imports(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cross, unassigned := crossings(graph)
	if os.Getenv("ARCH_PRINT_CROSSINGS") == "1" {
		for _, c := range cross {
			fmt.Printf("\t%q: true,\n", c)
		}
	}
	for _, p := range unassigned {
		t.Errorf("package %s has no module: assign it in modules (internal/arch/modules.go)", p)
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
