package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// An unknown first argument starts the server (main's switch has a default that does), so a subcommand that is not in that switch would
// start a second server instead of running. This pins launch-preflight to the dispatch and to the usage text, from the source.
func TestLaunchPreflightIsDispatched(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{} // case label -> the function it calls
	ast.Inspect(f, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, e := range cc.List {
			lit, ok := e.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			name := strings.Trim(lit.Value, `"`)
			ast.Inspect(&ast.BlockStmt{List: cc.Body}, func(m ast.Node) bool {
				if call, ok := m.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && strings.HasPrefix(id.Name, "run") {
						cases[name] = id.Name
					}
				}
				return true
			})
		}
		return true
	})
	if got := cases["launch-preflight"]; got != "runLaunchPreflight" {
		t.Errorf("main's switch sends launch-preflight to %q, want runLaunchPreflight (an unknown subcommand starts the server)", got)
	}
	if cases["entitlements-founding"] == "" || cases["launch-notice"] == "" {
		t.Error("the dispatch scan found no entitlements-founding or launch-notice: the scan is broken")
	}
	if !strings.Contains(string(src), "rivolt launch-preflight") {
		t.Error("the usage text does not list launch-preflight")
	}
}

// The loaded-alert-rules check fails closed: skipped only when told to (offline, --no-alert-check); with no address it is a problem, not a pass.
func TestAlertRulesSourceFailsClosed(t *testing.T) {
	if alertRulesSource("http://prom:9090", "", true) != nil {
		t.Error("skip=true must skip the check")
	}
	f := alertRulesSource("", "", false)
	if f == nil {
		t.Fatal("no address and not skipped must not skip the check")
	}
	if _, err := f(context.Background()); err == nil || !strings.Contains(err.Error(), "--prometheus-url") {
		t.Errorf("no address: want an error that says how to fix it, got %v", err)
	}
	if alertRulesSource("http://flag:9090", "http://env:9090", false) == nil || alertRulesSource("", "http://env:9090", false) == nil {
		t.Error("a flag or environment address must give a reader")
	}
}
