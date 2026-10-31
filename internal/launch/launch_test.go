package launch

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestIDsAreUniqueAndDescribed(t *testing.T) {
	seen := map[string]bool{}
	for _, it := range Items {
		if it.ID == "" || it.What == "" {
			t.Errorf("item %+v needs an ID and a What", it)
		}
		if seen[it.ID] {
			t.Errorf("duplicate item %s", it.ID)
		}
		seen[it.ID] = true
	}
}

// A cleared item names the PR that cleared it and a proof that exists; a pending item claims neither.
func TestClearedItemsHaveTheirProof(t *testing.T) {
	root := repoRoot(t)
	var testFuncs = map[string]bool{}
	re := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "dist") {
			return fs.SkipDir
		}
		if strings.HasSuffix(p, "_test.go") {
			if b, err := os.ReadFile(p); err == nil {
				for _, m := range re.FindAllStringSubmatch(string(b), -1) {
					testFuncs[m[1]] = true
				}
			}
		}
		return nil
	})
	for _, it := range Items {
		if !it.Cleared {
			if it.ClearedBy != "" || it.Proof != "" {
				t.Errorf("%s is pending but names %q / %q: set Cleared in the PR that has the proof", it.ID, it.ClearedBy, it.Proof)
			}
			continue
		}
		if !regexp.MustCompile(`^#\d+(,#\d+)*$`).MatchString(it.ClearedBy) {
			t.Errorf("%s is cleared but ClearedBy is %q, want a PR like #1374 (several comma-separated)", it.ID, it.ClearedBy)
		}
		if it.Proof == "" {
			t.Errorf("%s is cleared without a proof", it.ID)
		}
		for _, proof := range strings.Split(it.Proof, ",") {
			if strings.HasPrefix(proof, "Test") {
				if !testFuncs[proof] {
					t.Errorf("%s: proof %s is not a test function in the repo", it.ID, proof)
				}
			} else if _, err := os.Stat(filepath.Join(root, proof)); err != nil {
				t.Errorf("%s: proof file %s does not exist", it.ID, proof)
			}
		}
	}
}

func TestPendingIsTheUnclearedItems(t *testing.T) {
	n := 0
	for _, it := range Items {
		if !it.Cleared {
			n++
		}
	}
	if got := len(Pending()); got != n {
		t.Errorf("Pending() = %d items, want %d", got, n)
	}
	if Cleared("no-such-item") {
		t.Error("an unknown item reads as cleared")
	}
}

// Billing off never blocks; billing on is blocked by exactly the pending items.
func TestGate(t *testing.T) {
	if g := Gate(false); len(g) != 0 {
		t.Errorf("billing off is gated by %v", g)
	}
	if g := Gate(true); strings.Join(g, ",") != strings.Join(Pending(), ",") {
		t.Errorf("billing on is gated by %v, want the pending items %v", g, Pending())
	}
}
