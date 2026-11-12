package arch

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// S19 9g acceptance, on the repository's own import graph: a change to an api-only package reports api, a platform change reports every
// role, and paths that do not ship in the server report none.
func TestRolesForRealPaths(t *testing.T) {
	m, err := NewRoleMap(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		paths []string
		want  string
	}{
		{[]string{"internal/passkeys/passkeys.go"}, "api"},
		{[]string{"internal/passkeys/passkeys.go", "internal/kratos/client.go", "web/src/main.tsx"}, "api,jobs"}, // kratos is in the IdP retrier's closure
		{[]string{"internal/tirelow/tirelow.go"}, "ingest"},
		{[]string{"internal/retention/retention.go"}, "jobs"},
		{[]string{"internal/db/users.go"}, "ingest,api,jobs"},
		{[]string{"internal/db/migrations/0001_init.sql"}, "ingest,api,jobs"}, // an embedded asset belongs to its package
		{[]string{"cmd/rivolt/main.go"}, "ingest,api,jobs"},
		{[]string{"go.sum"}, "ingest,api,jobs"},
		{[]string{"deploy/helm/rivolt/values.yaml"}, "ingest,api,jobs"},
		{[]string{"something/new.txt"}, "ingest,api,jobs"}, // unclassified counts for every role
		{[]string{"internal/passkeys/passkeys_test.go", "internal/testpg/testpg.go", "docs/ARCHITECTURE.md", "ios/Rivolt/App.swift",
			".github/workflows/build.yml", "web/e2e/smoke.spec.ts", "README.md"}, ""},
	} {
		if got := strings.Join(m.For(c.paths), ","); got != c.want {
			t.Errorf("%v: roles %q, want %q", c.paths, got, c.want)
		}
	}
}

// The monitor's close hooks run in the Rivian session loop, so a change to their packages rolls ingest; the IdP-delete and Apple-revoke
// retriers' packages roll jobs.
func TestCloseHookAndRetrierPackages(t *testing.T) {
	m, err := NewRoleMap(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for role, pkgs := range map[string][]string{
		"ingest": {"push", "recap", "weather", "engage", "trips"},
		"jobs":   {"idp", "oidc"},
	} {
		for _, p := range pkgs {
			roles := m.For([]string{"internal/" + p + "/x.go"})
			if !slices.Contains(roles, role) {
				t.Errorf("internal/%s: roles %v, want %s among them", p, roles, role)
			}
		}
	}
}

// The map's own logic, on a made-up graph: dependency sets follow imports transitively, an unlinked package counts for no role, and a
// linked package no role reaches counts for every role.
func TestRoleMapOnAGraph(t *testing.T) {
	m := roleMapOf(map[string]map[string]bool{
		"cmd/rivolt":         {"internal/api": true, "internal/rivian": true, "internal/retention": true, "internal/maps": true, "internal/db": true},
		"internal/api":       {"internal/trips": true, "internal/db": true},
		"internal/trips":     {},
		"internal/maps":      {},
		"internal/rivian":    {"internal/drives": true},
		"internal/drives":    {"internal/db": true},
		"internal/db":        {},
		"internal/retention": {"internal/drives": true},
		"internal/testpg":    {"internal/db": true},
	})
	for path, want := range map[string]string{
		"internal/trips/a.go":     "ingest,api",      // api imports it; the close hooks use it
		"internal/drives/a.go":    "ingest,jobs",     // recorder, and retention imports it
		"internal/db/a.go":        "ingest,api,jobs", // platform
		"internal/testpg/a.go":    "",                // not linked
		"internal/maps/a.go":      "api",             // planner is an api module
		"internal/notapkg/x.json": "",                // no package there
	} {
		if got := strings.Join(m.For([]string{path}), ","); got != want {
			t.Errorf("%s: roles %q, want %q", path, got, want)
		}
	}
	if n := IngestAffecting([][]string{{"api"}, {"ingest", "api", "jobs"}, {}, {"jobs", "ingest"}}); n != 2 {
		t.Errorf("IngestAffecting = %d, want 2", n)
	}
}
