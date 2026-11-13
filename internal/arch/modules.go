package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

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
	"internal/receipts": "recorder", "internal/imports": "recorder", "internal/ledger": "recorder", "internal/firstscreen": "recorder", // the money statement: a read model of the recorded charges and drives

	// planner: trips, routing and the map services.
	"internal/trips": "planner", "internal/planleg": "planner", "internal/tripadvice": "planner", "internal/tripmultiday": "planner",
	"internal/tripprofile": "planner", "internal/tripweather": "planner", "internal/syntheticplan": "planner", "internal/chargers": "planner",
	"internal/dcfcrates": "planner", "internal/maps": "planner", "internal/elevation": "planner", "internal/geocoding": "planner",
	"internal/weather": "planner", "internal/zonelookup": "planner", "internal/radar": "planner",

	// billing.
	"internal/stripebill": "billing", "internal/applebill": "billing", "internal/entitlements": "billing", "internal/billingdates": "billing",
	"internal/billingmail": "billing", "internal/statement": "billing", "internal/aibudget": "billing", "internal/aitaste": "billing",

	// api: HTTP, identity and the user's own settings.
	"internal/api": "api", "internal/web": "api", "internal/webcarry": "api", "internal/auth": "api", "internal/appauth": "api",
	"internal/kratos": "api", "internal/hydra": "api", "internal/idp": "api", "internal/oidc": "api", "internal/passkeys": "api",
	"internal/appleid": "api", "internal/identity": "api", "internal/sessions": "api", "internal/cfaccess": "api", "internal/adminonly": "api",
	"internal/adminaudit": "api", "internal/adminhealth": "api", "internal/signupgate": "api", "internal/signuprequests": "api",
	"internal/settings": "api", "internal/appsettings": "api", "internal/privacy": "api", "internal/analytics": "api",
	"internal/histimport": "api", "internal/changelog": "api", "internal/launch": "api", "internal/launchnotice": "api", "internal/preflight": "api",

	// jobs: background work over the stored data, and what it sends.
	"internal/archive": "jobs", "internal/retention": "jobs", "internal/repair": "jobs", "internal/recap": "jobs", "internal/weekly": "jobs",
	"internal/insights": "jobs", "internal/push": "jobs", "internal/onboardmail": "jobs",
	"internal/plugreminder": "jobs", "internal/overnight": "jobs", "internal/r2arrival": "jobs",
	"internal/engage": "jobs", "internal/electrafi": "jobs", "internal/demo": "jobs", "internal/backfill": "jobs", "internal/outbox": "jobs",

	// The composition roots: they wire everything.
	"cmd/rivolt": "root", "cmd/replay": "root", "cmd/sampletruck": "root",
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

// imports reads every non-test .go file under internal/ and cmd/ (all build tags: a file behind a tag still ships in some build) and returns
// each package's in-repo imports.
func imports(root string) (map[string]map[string]bool, error) {
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
			return nil, err
		}
	}
	return out, nil
}
