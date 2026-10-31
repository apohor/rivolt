package entitlements

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Pro never means more Rivian calls. The Rivian client, the shared budget and its rate-limit classes, the poller (the state monitor), the
// lease and fence machinery that decides which pod polls, and the recorders' stores (drives, charges, samples) must not know a plan
// exists: they import neither the entitlements package nor the window/launch packages derived from it (histfloor is the user's own deletion floor, not a plan), and never call a Pro check. A
// free account's vehicle is recorded, polled and alerted exactly like a Pro one; only what the API shows differs.
var planBlind = []string{
	"rivian",    // the client, the monitor (poller), the recorder, the shared budget classes
	"ratelimit", // the budget's buckets
	"leases",    // which pod holds which vehicle
	"fence",     // single-owner writes
	"drives",
	"charges",
	"samples",
	"receipts", // the receipts sync calls Rivian
	"retention",
	"archive",
}

var planRef = regexp.MustCompile(`internal/(entitlements|launch|histwin)"|\bentitlements\.|\.Pro\(|\bisPro\b|\bpro\(`)

func TestRivianSideNeverConsultsThePlan(t *testing.T) {
	scanned := 0
	for _, pkg := range planBlind {
		dir := filepath.Join("..", pkg)
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s (%v): update the list", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			scanned++
			if m := planRef.FindString(string(b)); m != "" {
				t.Errorf("%s refers to the plan (%q): the Rivian budget, poller and recorders must be tier-blind", f, m)
			}
		}
	}
	if scanned < 30 {
		t.Fatalf("only %d files scanned: the walk is not seeing the packages", scanned)
	}
}

// The detector itself catches what it should.
func TestPlanReferenceDetector(t *testing.T) {
	for src, want := range map[string]bool{
		`import "github.com/apohor/rivolt/internal/entitlements"`: true,
		`import "github.com/apohor/rivolt/internal/histwin"`:      true,
		`if e.Pro(ctx, uid) {`:                                    true,
		`if !isPro(uid) {`:                                        true,
		`x := entitlements.State{}`:                               true,
		`// the process budget`:                                   false,
		`proxy := newProxy(); improve(); approve()`:               false,
		`import "github.com/apohor/rivolt/internal/ratelimit"`:    false,
	} {
		if got := planRef.MatchString(src); got != want {
			t.Errorf("%q: detected %v, want %v", src, got, want)
		}
	}
}
