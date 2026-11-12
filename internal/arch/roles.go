package arch

import (
	"path"
	"strings"
)

// Roles are the process roles a merge can change (RIVOLT_ROLE without all; S19 9g).
var Roles = []string{"ingest", "api", "jobs"}

// roleModules are the modules whose packages each role runs, the roots of its dependency set. A package is in a role's set when a root
// package imports it, directly or through others (non-test imports, every build tag).
var roleModules = map[string][]string{
	"ingest": {"ingest", "recorder"},        // the Rivian sessions, the recorder and its close hooks
	"api":    {"api", "planner", "billing"}, // the public routes
	"jobs":   {"jobs", "billing"},           // the sweeps, retriers and sends (statement, Stripe retrier)
}

// roleExtraRoots are packages of another module that a role's starters run (cmd/rivolt/starters.go): the module says whose code it is,
// the starter says where it runs.
var roleExtraRoots = map[string][]string{
	"jobs": {"internal/chargewatch", "internal/drain", "internal/rivianexpiry", "internal/packhealth", "internal/milestones",
		"internal/samples", "internal/entitlements"},
}

// RoleMap answers which roles a changed path affects. Build it with NewRoleMap from the repository's import graph.
type RoleMap struct {
	pkgs   map[string]bool            // every package under internal/ and cmd/
	binary map[string]bool            // packages linked into cmd/rivolt
	sets   map[string]map[string]bool // role -> packages in its dependency set
}

// NewRoleMap reads the import graph under root (the repository) and builds each role's dependency set.
func NewRoleMap(root string) (*RoleMap, error) {
	graph, err := imports(root)
	if err != nil {
		return nil, err
	}
	return roleMapOf(graph), nil
}

func roleMapOf(graph map[string]map[string]bool) *RoleMap {
	m := &RoleMap{pkgs: map[string]bool{}, binary: closure(graph, []string{"cmd/rivolt"}), sets: map[string]map[string]bool{}}
	for pkg := range graph {
		m.pkgs[pkg] = true
	}
	for _, r := range Roles {
		var roots []string
		for pkg := range graph {
			mod, ok := moduleOf(pkg)
			if !ok {
				continue
			}
			for _, want := range roleModules[r] {
				if mod == want {
					roots = append(roots, pkg)
				}
			}
		}
		for _, p := range roleExtraRoots[r] {
			for pkg := range graph {
				if pkg == p || strings.HasPrefix(pkg, p+"/") {
					roots = append(roots, pkg)
				}
			}
		}
		m.sets[r] = closure(graph, roots)
	}
	return m
}

func closure(graph map[string]map[string]bool, roots []string) map[string]bool {
	seen := map[string]bool{}
	stack := append([]string(nil), roots...)
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		for to := range graph[p] {
			stack = append(stack, to)
		}
	}
	return seen
}

// For returns the roles the changed paths (relative to the repository root, slash-separated) affect, in Roles order:
//   - a Go package's directory (any file in it, embedded assets and migrations included): nothing when the server binary does not link it
//     (test helpers, other commands); every role for platform and cmd/rivolt; otherwise each role whose dependency set has it, and every
//     role when none does (linked, but no role claims it);
//   - go.mod, go.sum, the Dockerfile and the chart (deploy/helm, every role's Deployment and values): every role;
//   - the web app (web/, served on the public routes): api;
//   - the iPhone app, docs, CI, scripts, tools and top-level text: none (they do not ship in the server);
//   - anything else: every role, so an unclassified path is never under-counted.
func (m *RoleMap) For(paths []string) []string {
	hit := map[string]bool{}
	all := func() {
		for _, r := range Roles {
			hit[r] = true
		}
	}
	for _, p := range paths {
		switch {
		case p == "go.mod" || p == "go.sum" || p == "Dockerfile" || strings.HasPrefix(p, "deploy/helm/"):
			all()
		case strings.HasPrefix(p, "web/e2e/"):
		case strings.HasPrefix(p, "web/"):
			hit["api"] = true
		case strings.HasPrefix(p, "internal/") || strings.HasPrefix(p, "cmd/"):
			pkg, ok := m.packageOf(p)
			if !ok || !m.binary[pkg] {
				continue // not a package the server links (a new directory with no Go files yet, a test helper)
			}
			if strings.HasSuffix(p, "_test.go") || strings.Contains(p, "/testdata/") {
				continue
			}
			mod, _ := moduleOf(pkg)
			if mod == "platform" || mod == "root" {
				all()
				continue
			}
			found := false
			for _, r := range Roles {
				if m.sets[r][pkg] {
					hit[r], found = true, true
				}
			}
			if !found {
				all()
			}
		case strings.HasPrefix(p, "ios/") || strings.HasPrefix(p, "docs/") || strings.HasPrefix(p, ".github/") ||
			strings.HasPrefix(p, "scripts/") || strings.HasPrefix(p, "tools/") || !strings.Contains(p, "/") && path.Ext(p) == ".md" ||
			p == "LICENSE" || p == "Makefile" || p == "renovate.json" || p == "docker-compose.yml" || p == ".gitignore":
		default:
			all()
		}
	}
	var out []string
	for _, r := range Roles {
		if hit[r] {
			out = append(out, r)
		}
	}
	return out
}

// packageOf returns the package a file belongs to: its directory, or the nearest parent that is a package (an embedded asset in a
// subdirectory, internal/db/migrations/*.sql, belongs to internal/db).
func (m *RoleMap) packageOf(file string) (string, bool) {
	for d := path.Dir(file); d != "." && d != "/"; d = path.Dir(d) {
		if m.pkgs[d] {
			return d, true
		}
	}
	return "", false
}

// IngestAffecting counts how many of the merges' role lists include ingest.
func IngestAffecting(merges [][]string) int {
	n := 0
	for _, roles := range merges {
		for _, r := range roles {
			if r == "ingest" {
				n++
				break
			}
		}
	}
	return n
}
