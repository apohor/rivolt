package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/api"
)

// startedBy lists the starters role p runs, sorted.
func startedBy(p rolePlan) []string {
	var out []string
	for name, k := range starters {
		if p.runs(k) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// What each role starts (S19 9b, 9d): api starts no lease coordinator, no Rivian work and no singleton (discovery, janitor, grant tickers,
// archive, retention and thinning); ingest serves no public routes and runs no
// jobs; jobs holds no leases and serves no public routes; all starts everything. The monitors and the recorder are not goroutines started
// here: on api and jobs the fence reads every group Lost (lead mode is required for them, role.go), so no monitor streams or writes there.
func TestStartersPerRole(t *testing.T) {
	never := map[processRole][]string{
		roleAPI:    {"db_app_grants", "db_readonly_grants", "inactive_purge", "archive_deletions", "lease_coordinator", "lease_reconcile_trigger", "forward_owner", "vehicle_discovery", "push_live", "ota_sync", "receipt_sync", "left_open", "tire_low", "partition_janitor", "retention", "backfill", "chargewatch"},
		roleIngest: {"chargers_warm", "chargers_reload", "partition_janitor", "retention", "inactive_purge", "stripe_cancel_retrier"},
		roleJobs:   {"lease_coordinator", "lease_reconcile_trigger", "forward_owner", "vehicle_discovery", "ota_sync", "receipt_sync", "chargers_warm", "chargers_reload"},
	}
	always := []string{"http_server", "http_shutdown", "metrics_server", "second_signal", "drain_watch", "drain_watch_leaseless", "token_release", "group_map_follow"}
	for _, r := range processRoles {
		p := planFor(r)
		got := map[string]bool{}
		for _, name := range startedBy(p) {
			got[name] = true
		}
		for name, k := range starters {
			want := k == startAlways || (k == startRoutes && p.PublicRoutes) || (k == startLeases && p.Leases) || (k == startJobs && p.Jobs)
			if got[name] != want {
				t.Errorf("%s: starter %s runs=%v, want %v", r, name, got[name], want)
			}
		}
		for _, name := range never[r] {
			if got[name] {
				t.Errorf("%s starts %s", r, name)
			}
		}
		for _, name := range always {
			if !got[name] {
				t.Errorf("%s does not start %s", r, name)
			}
		}
		if r == roleAll && len(got) != len(starters) {
			t.Errorf("all starts %d of %d starters", len(got), len(starters))
		}
	}
	// The plan fields the kinds read agree with the role's other fields: a role that runs Rivian work holds leases and owns forwards.
	for _, r := range processRoles {
		if p := planFor(r); p.Leases != p.ForwardOwner {
			t.Errorf("%s: Leases %v, ForwardOwner %v", r, p.Leases, p.ForwardOwner)
		}
	}
}

// start runs a registered starter only on a role that runs its kind, and panics on a name nobody registered.
func TestStartGatesAndPanics(t *testing.T) {
	ran := make(chan string, 2)
	if !planFor(roleJobs).start("retention", func() { ran <- "retention" }) {
		t.Error("jobs did not start retention")
	}
	if planFor(roleAPI).start("retention", func() { ran <- "api retention" }) {
		t.Error("api started retention")
	}
	if !(rolePlan{}).start("http_server", func() { ran <- "http_server" }) {
		t.Error("the zero plan did not start a startAlways starter")
	}
	got := []string{<-ran, <-ran}
	sort.Strings(got)
	if strings.Join(got, ",") != "http_server,retention" {
		t.Errorf("ran %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("an unregistered starter did not panic")
		}
	}()
	planFor(roleAll).start("not_registered", func() {})
}

// The starter lint: no non-test file in this package has a `go` statement outside rolePlan.start, every start call names a registered starter
// with a string literal, and every registered starter is started at exactly one site. A new goroutine therefore needs a starters entry,
// which is where its role is decided.
func TestStarterLint(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	sites := map[string][]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			inStart := ok && f == "starters.go" && fn.Name.Name == "start" && fn.Recv != nil
			ast.Inspect(d, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.GoStmt:
					if !inStart {
						t.Errorf("%s: a go statement outside rolePlan.start: wrap it in <plan>.start(\"name\", ...) and register the name in starters", fset.Position(n.Pos()))
					}
				case *ast.CallExpr:
					sel, ok := n.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "start" || len(n.Args) != 2 {
						return true
					}
					if _, isFunc := n.Args[1].(*ast.FuncLit); !isFunc {
						return true
					}
					lit, ok := n.Args[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s: start's name must be a string literal", fset.Position(n.Pos()))
						return true
					}
					name, _ := strconv.Unquote(lit.Value)
					if _, ok := starters[name]; !ok {
						t.Errorf("%s: starter %q is not registered in starters", fset.Position(n.Pos()), name)
					}
					sites[name] = append(sites[name], fset.Position(n.Pos()).String())
				}
				return true
			})
		}
	}
	for name := range starters {
		switch len(sites[name]) {
		case 1:
		case 0:
			t.Errorf("starter %q is registered but nothing starts it: remove it", name)
		default:
			t.Errorf("starter %q is started at %d sites (%v): give each its own name", name, len(sites[name]), sites[name])
		}
	}
}

// The route groups per role, through the real router main builds (ProbesOnly is !PublicRoutes, main.go): a role without public routes has
// exactly the three probes, and a role with them has the full router.
func TestRoutesPerRole(t *testing.T) {
	for _, r := range processRoles {
		p := planFor(r)
		var routes []string
		if err := chi.Walk(api.New(api.Deps{ProbesOnly: !p.PublicRoutes, Version: "test"}).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			routes = append(routes, method+" "+route)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		sort.Strings(routes)
		probes := strings.Join(routes, ",") == "GET /api/health,GET /healthz,GET /readyz"
		if p.PublicRoutes == probes {
			t.Errorf("%s (PublicRoutes %v): %d routes", r, p.PublicRoutes, len(routes))
		}
		if p.PublicRoutes {
			found := false
			for _, rt := range routes {
				found = found || strings.HasPrefix(rt, "GET /api/state")
			}
			if !found {
				t.Errorf("%s: the full router has no GET /api/state", r)
			}
		}
	}
}
