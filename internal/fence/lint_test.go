package fence_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The unguarded-write lint (single-owner design 4.3, test 9): every function in samples, drives,
// charges and the recorder files that writes owner-side tables must carry the fence (the
// rivolt_lease_ok guard or a fence token from the context), or be listed here with a reason.
//
// notYetGuarded is the work list of the enforcement PRs. It only shrinks: each PR that guards a
// write removes its entry, and the test fails for an entry that is guarded already, so the list
// cannot go stale. The last enforcement PR empties it and flips enforcementComplete.
// notOwnerSide is permanent: writes that are not made by the group's owner (user actions,
// imports, repairs, maintenance jobs), which no lease guards.

const enforcementComplete = false

var notYetGuarded = map[string]string{
	"samples.Store.InsertBatch":              "guarded by the sample-insert PR (#1291); remove this entry when it merges",
	"drives.Store.Upsert":                    "drives PR",
	"charges.Store.Upsert":                   "charges PR",
	"charges.Store.AbandonOpenLive":          "charges PR (live charge session)",
	"charges.Store.RefreshOpenLive":          "charges PR (live charge session)",
	"charges.Store.CloseStaleOpenLive":       "charges PR (live charge session)",
	"charges.Store.CloseStaleOpenLiveBefore": "charges PR (live charge session)",
	"charges.Store.ApplyNetworkRate":         "classify in the charges PR: owner close hook or user action",
	"charges.Store.ConfirmNetwork":           "classify in the charges PR: owner close hook or user action",
	"charges.Store.MarkFree":                 "classify in the charges PR: owner close hook or user action",
	"charges.Store.UpdatePricing":            "classify in the charges PR: owner close hook or user action",
}

var notOwnerSide = map[string]string{
	"samples.Store.InsertBulk":          "replay catch-up and imports: no Rivian session, no lease",
	"samples.Store.Reset":               "user action (reset data)",
	"samples.Thinner.thin":              "singleton maintenance job",
	"samples.Thinner.dropExpiredFull":   "singleton maintenance job",
	"samples.Thinner.rethinPair":        "singleton maintenance job",
	"drives.Store.Reset":                "user action (reset data)",
	"drives.Store.SetTrailerOverride":   "user action",
	"charges.Store.Reset":               "user action (reset data)",
	"charges.Store.DeleteByExternalID":  "user action / import",
	"charges.Store.RestampPlace":        "user action",
	"charges.Store.UndoRestamp":         "user action",
	"charges.Store.SetLocationOverride": "user action",
	"charges.Store.SetPlaceOverride":    "user action",
	"charges.Store.SetNetworkPrice":     "user action",
}

var writeSQL = regexp.MustCompile(`(?is)\b(INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+[a-z_]+`)

// guarded reports whether a function body carries the fence.
func guarded(src string) bool {
	return strings.Contains(src, "rivolt_lease_ok") || strings.Contains(src, "fence.From(") || strings.Contains(src, "guardedInsert")
}

// writers returns "pkg.Recv.Func" for every function in the scanned files whose string literals
// contain write SQL, and whether it is guarded.
func writers(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	dirs := []string{"samples", "drives", "charges", "rivian"}
	for _, d := range dirs {
		files, err := filepath.Glob(filepath.Join("..", d, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			base := filepath.Base(f)
			if strings.HasSuffix(base, "_test.go") {
				continue
			}
			if d == "rivian" && !strings.Contains(base, "recorder") && !strings.Contains(base, "charging_schedule") {
				continue
			}
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			src := mustRead(t, f)
			for _, decl := range af.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				writes := false
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING && writeSQL.MatchString(bl.Value) {
						writes = true
					}
					return true
				})
				if !writes {
					continue
				}
				name := fd.Name.Name
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					typ := fd.Recv.List[0].Type
					if s, ok := typ.(*ast.StarExpr); ok {
						typ = s.X
					}
					if id, ok := typ.(*ast.Ident); ok {
						name = id.Name + "." + name
					}
				}
				body := src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset]
				out[d+"."+name] = guarded(body)
			}
		}
	}
	return out
}

func TestEveryOwnerWriteIsGuardedOrListed(t *testing.T) {
	found := writers(t)
	var bad []string
	for name, isGuarded := range found {
		_, pending := notYetGuarded[name]
		_, exempt := notOwnerSide[name]
		switch {
		case isGuarded && pending:
			bad = append(bad, fmt.Sprintf("%s is guarded now: remove it from notYetGuarded", name))
		case isGuarded && exempt:
			bad = append(bad, fmt.Sprintf("%s is guarded: it does not belong in notOwnerSide", name))
		case !isGuarded && !pending && !exempt:
			bad = append(bad, fmt.Sprintf("%s writes without the fence: guard it (rivolt_lease_ok in the statement, token from fence.From), or list it with a reason", name))
		}
	}
	for _, m := range []map[string]string{notYetGuarded, notOwnerSide} {
		for name := range m {
			if _, ok := found[name]; !ok {
				bad = append(bad, fmt.Sprintf("%s is listed but no longer writes (renamed or removed): drop the entry", name))
			}
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}

func TestNotYetGuardedIsEmptyOnceEnforcementIsComplete(t *testing.T) {
	if enforcementComplete && len(notYetGuarded) > 0 {
		t.Errorf("enforcementComplete is set but %d writes are not guarded yet", len(notYetGuarded))
	}
}

func mustRead(t *testing.T, f string) string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
