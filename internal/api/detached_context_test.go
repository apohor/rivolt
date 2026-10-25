package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Row-level security, stage 2: on the request pool a context with no user reads no rows, so work detached from a request
// (context.Background(), context.TODO()) that touches a request-side store silently reads nothing: a count of 0, an empty settings
// read, an UPDATE that matches no row. Every detached context in this package is therefore either given the user
// (auth.WithUser(context.Background(), uid)) or listed here with why it needs none. A new one fails this test until someone says which.
var detachedContextAllowed = map[string]struct {
	n   int
	why string
}{
	"admin_audit.go":     {1, "the audit row is an admin table written on the system pool"},
	"events.go":          {1, "the retention purge: a system-pool job across users"},
	"rivian.go":          {1, "notifyAdmin: sends mail, touches no store"},
	"signup_requests.go": {1, "notifyAdmin: sends mail, touches no store"},
	"statement_email.go": {1, "the statement sweep: a system-pool job across users"},
	"vehicles.go":        {1, "the monitor's async vehicle refresh: the monitor and its hooks are system-side"},
	"weekly_recap.go":    {1, "the weekly recap sweep: a system-pool job across users"},
}

var detachedCtx = regexp.MustCompile(`context\.(Background|TODO)\(\)`)

func TestDetachedContextsCarryTheUserOrSayWhyNot(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 50 {
		t.Fatalf("glob: %d files, %v", len(files), err)
	}
	got := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "//") || !detachedCtx.MatchString(line) {
				continue
			}
			if strings.Contains(line, "auth.WithUser(") {
				continue // the user rides in the context
			}
			got[f]++
		}
	}
	var files2 []string
	for f := range got {
		files2 = append(files2, f)
	}
	sort.Strings(files2)
	for _, f := range files2 {
		want, ok := detachedContextAllowed[f]
		if !ok {
			t.Errorf("%s has %d detached context(s) with no user: carry it (auth.WithUser(context.Background(), uid)) or list the file in detachedContextAllowed with why", f, got[f])
		} else if got[f] != want.n {
			t.Errorf("%s has %d detached contexts, the list says %d (%s): classify the new one", f, got[f], want.n, want.why)
		}
	}
	for f, want := range detachedContextAllowed {
		if got[f] == 0 {
			t.Errorf("%s (%s) no longer has a detached context: delete it from detachedContextAllowed", f, want.why)
		}
	}
}
