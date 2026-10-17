package rivian

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Every write of a vehicle's cached state goes through putCacheLocked,
// which stamps the wake (wokeAt) when a sleeping car's next state is
// awake. A direct m.cache[id] = st skips that, and the sleep gate then
// misattributes the wake (#217). Deleting an entry (forgetVehicle) is
// not a write of state and stays allowed.
func TestCacheWritesGoThroughPutCacheLocked(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range as.Lhs {
					ix, ok := lhs.(*ast.IndexExpr)
					if !ok {
						continue
					}
					sel, ok := ix.X.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "cache" {
						continue
					}
					checked++
					if fn.Name.Name != "putCacheLocked" {
						t.Errorf("%s: %s writes .cache[...] directly; use putCacheLocked", fset.Position(as.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("found no cache write at all; the guard is looking in the wrong place")
	}
}
