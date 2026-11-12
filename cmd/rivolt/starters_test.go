package main

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/api"
)

// startedBy lists the starters role p runs, sorted.
func startedBy(p rolePlan) []string {
	var out []string
	for name, st := range starters {
		if p.starts(st) {
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
		roleAPI:    {"db_app_grants", "db_readonly_grants", "inactive_purge", "archive_deletions", "lease_coordinator", "lease_reconcile_trigger", "forward_owner", "vehicle_discovery", "push_live", "ota_sync", "receipt_sync", "left_open", "tire_low", "partition_janitor", "retention", "backfill", "chargewatch", "rivian_expiry", "r2_arrival", "outbox_sender", "outbox_purge"},
		roleIngest: {"chargers_warm", "chargers_reload", "partition_janitor", "retention", "inactive_purge", "stripe_cancel_retrier", "rivian_expiry", "r2_arrival", "outbox_sender", "outbox_purge"},
		roleJobs:   {"lease_coordinator", "lease_reconcile_trigger", "forward_owner", "vehicle_discovery", "ota_sync", "receipt_sync", "chargers_warm", "chargers_reload"},
	}
	always := []string{"http_server", "http_shutdown", "metrics_server", "second_signal", "drain_watch", "drain_watch_leaseless", "token_release", "group_map_follow"}
	for _, r := range processRoles {
		p := planFor(r)
		p.Demo = true // the demo starters count too: all starts every one
		got := map[string]bool{}
		for _, name := range startedBy(p) {
			got[name] = true
		}
		for name, st := range starters {
			k := st.kind
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

// What each install starts on top of its role (S19 9d-2). The admin deployment runs role all and no background work of its own: it starts
// exactly what it started before the registry, all of it claim-safe or per-pod. A demo-only install sends nothing; only a demo install
// seeds the demo account.
func TestStartersPerInstall(t *testing.T) {
	admin := planFor(roleAll)
	admin.AdminOnly, admin.Demo = true, true
	adminStarts := []string{"account_sync", "apple_revoke_retrier", "chargers_reload", "chargers_warm", "chargewatch", "demo_plans", "demo_pricing",
		"demo_weather", "entitlement_stats", "flags_poll", "http_server", "http_shutdown", "idp_delete_retrier", "metrics_server",
		"push_notifications_sweep", "second_signal", "settings_sync", "stripe_cancel_retrier", "token_release"}
	if got := strings.Join(startedBy(admin), ","); got != strings.Join(adminStarts, ",") {
		t.Errorf("the admin deployment starts %s\nwant %s", got, strings.Join(adminStarts, ","))
	}
	for _, name := range []string{"lease_coordinator", "forward_owner", "retention", "partition_janitor", "inactive_purge", "archive_deletions",
		"backfill", "vehicle_discovery", "db_app_grants", "reauth_nudge", "rivian_expiry", "r2_arrival", "tire_low", "left_open", "milestones", "plug_reminder", "push_live", "outbox_sender", "outbox_purge"} {
		if admin.starts(starters[name]) {
			t.Errorf("the admin deployment starts %s", name)
		}
	}

	demoOnly := planFor(roleAll)
	demoOnly.DemoOnly, demoOnly.Demo = true, true
	for _, name := range []string{"push_notifications_sweep", "milestones", "plug_reminder", "left_open", "tire_low"} {
		if demoOnly.starts(starters[name]) {
			t.Errorf("a demo-only install starts %s", name)
		}
	}
	for _, name := range []string{"demo_pricing", "demo_weather", "demo_plans", "retention", "http_server"} {
		if !demoOnly.starts(starters[name]) {
			t.Errorf("a demo-only install does not start %s", name)
		}
	}
	for _, r := range processRoles {
		for _, name := range []string{"demo_pricing", "demo_weather", "demo_plans"} {
			if planFor(r).starts(starters[name]) {
				t.Errorf("%s without the demo starts %s", r, name)
			}
		}
	}
}

// Every starter says how it stays single when every pod of its role runs it, and names the lock or claim, or why every pod may.
func TestStartersSayHowTheyStaySingle(t *testing.T) {
	for name, st := range starters {
		if st.single == singleUnset || strings.TrimSpace(st.by) == "" {
			t.Errorf("starter %s: set single (owned, lock or every pod) and by (the lock or claim, or why every pod may run it)", name)
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
	if planFor(roleAll).start("demo_pricing", func() { ran <- "demo" }) {
		t.Error("an install without the demo started demo_pricing")
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
// which is where its role is decided. Calls that start work of their own (bg.Go, and X.Start/Run/Loop/Watch/Sync(ctx, ...)) are flagged
// outside a start too, unless a `//rivolt:transient <reason>` comment on the line above or the same line says why they are not a starter.
// A site's own install gate must agree with its entry: under !adminOnlyMode it is not onAdmin, under !demo.OnlyEnabled() it is offDemoOnly,
// and under demo.Enabled() it is demo.
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
		file, err := parser.ParseFile(fset, f, src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		transient := map[int]bool{} // lines a //rivolt:transient comment covers: its own and the next
		for _, cg := range file.Comments {
			for _, c := range cg.List {
				reason, ok := strings.CutPrefix(c.Text, "//rivolt:transient")
				if !ok {
					continue
				}
				if strings.TrimSpace(reason) == "" {
					t.Errorf("%s: //rivolt:transient needs a reason", fset.Position(c.Pos()))
				}
				line := fset.Position(c.Pos()).Line
				transient[line], transient[line+1] = true, true
			}
		}
		excused := func(n ast.Node) bool { return transient[fset.Position(n.Pos()).Line] }
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			inStartFunc := ok && f == "starters.go" && fn.Name.Name == "start" && fn.Recv != nil
			var walk func(n ast.Node, inStart bool, gates []string)
			walk = func(n ast.Node, inStart bool, gates []string) {
				ast.Inspect(n, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.IfStmt:
						if n.Init != nil {
							walk(n.Init, inStart, gates)
						}
						walk(n.Cond, inStart, gates)
						walk(n.Body, inStart, append(gates[:len(gates):len(gates)], exprString(fset, n.Cond)))
						if n.Else != nil {
							walk(n.Else, inStart, append(gates[:len(gates):len(gates)], "else of "+exprString(fset, n.Cond)))
						}
						return false
					case *ast.GoStmt:
						if !inStart && !excused(n) {
							t.Errorf("%s: a go statement outside rolePlan.start: wrap it in <plan>.start(\"name\", ...) and register the name in starters", fset.Position(n.Pos()))
						}
					case *ast.CallExpr:
						sel, ok := n.Fun.(*ast.SelectorExpr)
						if !ok {
							return true
						}
						if sel.Sel.Name == "start" && len(n.Args) == 2 {
							body, isFunc := n.Args[1].(*ast.FuncLit)
							if !isFunc {
								return true
							}
							lit, ok := n.Args[0].(*ast.BasicLit)
							if !ok || lit.Kind != token.STRING {
								t.Errorf("%s: start's name must be a string literal", fset.Position(n.Pos()))
								return true
							}
							name, _ := strconv.Unquote(lit.Value)
							st, ok := starters[name]
							if !ok {
								t.Errorf("%s: starter %q is not registered in starters", fset.Position(n.Pos()), name)
							} else {
								checkGates(t, fset.Position(n.Pos()).String(), name, st, gates)
							}
							sites[name] = append(sites[name], fset.Position(n.Pos()).String())
							walk(n.Fun, inStart, gates)
							walk(body, true, gates)
							return false
						}
						if !inStart && startsWork(n, sel) && !excused(n) {
							t.Errorf("%s: %s starts work outside rolePlan.start: register it, or say why not with //rivolt:transient <reason>", fset.Position(n.Pos()), exprString(fset, n.Fun))
						}
					}
					return true
				})
			}
			walk(d, inStartFunc, nil)
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

// startsWork reports whether a call outside a start looks like one that starts work of its own: bg.Go, or a Start/Run/Loop/Watch/Sync
// method (or one named with that prefix, RunLive) given ctx first.
func startsWork(call *ast.CallExpr, sel *ast.SelectorExpr) bool {
	if x, ok := sel.X.(*ast.Ident); ok && x.Name == "bg" && sel.Sel.Name == "Go" {
		return true
	}
	if len(call.Args) == 0 {
		return false
	}
	if id, ok := call.Args[0].(*ast.Ident); !ok || id.Name != "ctx" {
		return false
	}
	for _, p := range []string{"Start", "Run", "Loop", "Watch", "Sync"} {
		if rest, ok := strings.CutPrefix(sel.Sel.Name, p); ok && (rest == "" || unicode.IsUpper(rune(rest[0]))) {
			return true
		}
	}
	return false
}

// checkGates fails when a start site's enclosing conditions gate it on the install in a way its starters entry does not record.
func checkGates(t *testing.T, pos, name string, st starter, gates []string) {
	t.Helper()
	for _, g := range gates {
		if strings.HasPrefix(g, "else of ") {
			continue
		}
		if strings.Contains(g, "!adminOnlyMode") && st.onAdmin {
			t.Errorf("%s: %s runs under !adminOnlyMode but its entry says onAdmin", pos, name)
		}
		if strings.Contains(g, "!demo.OnlyEnabled()") && !st.offDemoOnly {
			t.Errorf("%s: %s runs under !demo.OnlyEnabled() but its entry is not offDemoOnly", pos, name)
		}
		if strings.Contains(strings.ReplaceAll(g, "!demo.Enabled()", ""), "demo.Enabled()") && !st.demo {
			t.Errorf("%s: %s runs under demo.Enabled() but its entry is not demo", pos, name)
		}
	}
}

func exprString(fset *token.FileSet, e ast.Node) string {
	var b strings.Builder
	_ = printer.Fprint(&b, fset, e)
	return b.String()
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
