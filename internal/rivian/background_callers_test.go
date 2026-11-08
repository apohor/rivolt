package rivian

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Single-owner M3-6 guard: only the pod that holds a user's group lease may call Rivian for that user (design section 1). This test lists every
// place outside the rivian package and the HTTP handlers that calls a method of *LiveClient (the real Rivian client; the method names come
// from reflection, so a new client method is watched at once) or StateMonitor.RefreshVehicleInfo, and fails for any that is not named below
// with the reason it is allowed. A new background caller must therefore be added here, with its ownership gate, in review.
//
// It is a name match on files that import internal/rivian, not a type check, so a method of another type with the same name can show up; those
// are listed too, with the reason (their own Source interface, a store). That is the price of needing no extra tooling, and a false
// positive costs one allowlist line.

// localMethods never leave the process: they build the client, hold its session state or read it. Their names are checked to exist.
func localMethod(name string) bool {
	switch {
	case strings.HasPrefix(name, "With"), strings.HasPrefix(name, "Set"):
		return true
	}
	switch name {
	case "Restore", "Authenticated", "Email", "NeedsReauth", "Session":
		return true
	}
	return false
}

// allowed: "file function .Method" -> why this caller may reach Rivian (an ownership gate, or not a Rivian call).
var allowedBackgroundCallers = map[string]string{
	"internal/leftopen/leftopen.go Tick .Vehicles":                 "its own Source interface (Postgres), not the Rivian client",
	"internal/tirelow/tirelow.go Tick .Vehicles":                   "its own Source interface (Postgres), not the Rivian client",
	"internal/otadetails/otadetails.go SyncUser .Vehicles":         "the store's vehicle list (Postgres); the Rivian calls below it are fenced by fence.Held per vehicle",
	"internal/otadetails/otadetails.go SyncUser .OTAUpdateDetails": "only for vehicles whose group this pod holds (fence.Held before the claim and before the account is touched; otadetails fence tests)",
	"internal/receipts/syncer.go run .CompletedSessionSummaries":   "only on the pod that owns the user's group (FenceUser before anything; receipts syncer_fence tests)",
	"internal/receipts/syncer.go run .SessionCharger":              "same gate as CompletedSessionSummaries",
	"internal/planleg/planleg.go PlanTrip .PlanTrip":               "request path (the trip planner leg cache), forwarded to the owner by M3-4; not a background job",
	"internal/tripmultiday/plan.go Plan .PlanTrip":                 "request path (multi-day planner), forwarded to the owner by M3-4; not a background job",
	"internal/api/vehicles.go handleVehicles .RefreshVehicleInfo":  "the old /api/vehicles path's cold-start refresh: request-driven, and for a linked account behind the 5-minute fallback claim (#1685)",
}

func TestBackgroundRivianCallersAreOwnerGated(t *testing.T) {
	names := map[string]bool{}
	typ := reflect.TypeOf((*LiveClient)(nil))
	for i := 0; i < typ.NumMethod(); i++ {
		names[typ.Method(i).Name] = true
	}
	if len(names) < 30 {
		t.Fatalf("only %d LiveClient methods found; the reflection is not seeing the client", len(names))
	}
	if _, ok := reflect.TypeOf((*StateMonitor)(nil)).MethodByName("RefreshVehicleInfo"); !ok {
		t.Fatal("StateMonitor.RefreshVehicleInfo is gone; update the guard")
	}
	for _, n := range []string{"Restore", "Authenticated"} {
		if !names[n] {
			t.Fatalf("local method %q is not a LiveClient method any more; update localMethod", n)
		}
	}
	root := filepath.Join("..", "..")
	used := map[string]bool{}
	var found []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			switch rel {
			case ".git", "web", "ios", "node_modules", "internal/rivian", "internal/web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return nil
		}
		imports := false
		for _, im := range f.Imports {
			if strings.HasSuffix(im.Path.Value, `internal/rivian"`) {
				imports = true
			}
		}
		if !imports {
			return nil
		}
		// The HTTP handlers are request paths (the forwarder covers them, M3-2 to M3-5); only the refresh is watched there.
		inAPI := strings.HasPrefix(rel, "internal/api/")
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				s, ok := c.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				m := s.Sel.Name
				watched := (names[m] && !inAPI && !localMethod(m)) || m == "RefreshVehicleInfo"
				if watched {
					found = append(found, fmt.Sprintf("%s %s .%s", rel, fd.Name.Name, m))
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	for _, f := range found {
		if _, ok := allowedBackgroundCallers[f]; !ok {
			t.Errorf("%s calls Rivian (or a method of that name) and is not in allowedBackgroundCallers: add it with the reason it is owner-gated, or gate it", f)
			continue
		}
		used[f] = true
	}
	for k := range allowedBackgroundCallers {
		if !used[k] {
			t.Errorf("allowedBackgroundCallers has %q but no such call was found: remove the stale entry", k)
		}
	}
}
