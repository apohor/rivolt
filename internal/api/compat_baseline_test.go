package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/offpeak"
	"github.com/apohor/rivolt/internal/settings"
)

// Old-client compatibility (zero-impact deploys, G5). A deploy replaces the server while builds from before it are still in people's hands: a
// TestFlight build that ships once and updates when its owner does, a browser tab that loaded the previous web release. The contract tests above
// check the CURRENT clients, and a removal can edit the fixture in the same PR as the server, so the shipped builds' view of the API would go
// unchecked.
//
// So the baseline is frozen. ios/CompatBaseline/<tag>/ holds what an old build knew: its response fixtures (responses/), the bodies
// it sends (requests/, when the tag has request fixtures), and the routes it calls (routes.txt). ios/compat-baseline.txt lists the active tags
// ("<tag> ios" or "<tag> web"). The tests here run them against the CURRENT server:
//
//   - every baseline response decodes strictly (DisallowUnknownFields) into the server's current type, so a field the old build reads that the
//     server no longer sends, or sends with another type, fails the build of the PR that did it;
//   - every baseline request body decodes strictly into the server's current request type, so a field an old build still sends is not a 400
//     (a renamed request field must keep accepting the old name);
//   - every route the old build calls is still mounted with the same method.
//
// Limits, on purpose: only what the fixtures contain is protected (a field no fixture carries is not); an ADDED field passes; a changed meaning
// under the same type is not seen; for the web only route literals are checked, since old JS against changed response shapes is the API rule's
// job and a body-level web check needs recorded fixtures we do not have. The baseline moves only when min_ios_build does: a build older than the
// baseline is not promised anything. Refresh it with ios/freeze-compat.sh <tag> <ios|web>; deleting a baseline file is a visible act in review.

const (
	compatManifest = "../../ios/compat-baseline.txt"
	compatDir      = "../../ios/CompatBaseline"
)

// compatRetiredFixtures are baseline responses whose endpoint was retired on purpose (the build that read it was dropped from the baseline's
// promise or the screen is gone), each with why. A baseline fixture that is in neither iosFixtureTargets, fixturesCheckedElsewhere nor here fails.
var compatRetiredFixtures = map[string]string{}

// compatRetiredRoutes ("METHOD path") are routes an old build calls that were retired on purpose, each with why.
var compatRetiredRoutes = map[string]string{}

// requestFixtureTargets maps a baseline request fixture to the server request type it must decode into. Request fixtures are the bodies the
// clients send (the iPhone app's encoders, recorded by its tests). An old-build request fixture with no entry here fails, so none is skipped.
func requestFixtureTargets() map[string]any {
	return map[string]any{
		"app_token_body.json":           &appTokenRequest{},
		"charging_rates_patch.json":     &chargingRatePatch{},
		"corridor_layers_plain.json":    &corridorLayersRequest{},
		"corridor_layers_settings.json": &corridorLayersRequest{},
		"offpeak_put.json":              &offpeak.Schedule{},
		"planner_prefs_full.json":       &settings.PlannerPrefs{},
		"planner_prefs_old.json":        &settings.PlannerPrefs{}, // an older client: only drive_mode and has_adapter; the rest must stay accepted as absent
		"saved_trip_body.json":          &savedTripBody{},
		"trip_multiday_request.json":    &tripPlanMultidayRequest{},
		"trip_plan_request.json":        &tripPlanRequest{},
	}
}

const iosRequestFixtureDir = "../../ios/RivoltTests/RequestFixtures"

// The request bodies the iPhone app sends today (ios/RivoltTests/RequestFixtures, recorded from the app's own encoders by its tests) decode
// strictly into the server's current request types, and every file has a type: none can skip the check. They join the old-client baseline at
// the next freeze of an iOS tag (ios/freeze-compat.sh), where TestOldIOSRequestBodiesAreStillAccepted keeps them honest against later servers.
func TestIOSRequestFixturesMatchServerTypes(t *testing.T) {
	targets := requestFixtureTargets()
	have := map[string]bool{}
	for _, name := range readBaselineDir(t, iosRequestFixtureDir) {
		have[name] = true
		target, ok := targets[name]
		if !ok {
			t.Errorf("%s has no entry in requestFixtureTargets: add the server request type it is sent to", name)
			continue
		}
		b, err := os.ReadFile(filepath.Join(iosRequestFixtureDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := decodeStrict(b, target); err != nil {
			t.Errorf("%s no longer decodes into the server's request type (the app would get a 400): %v", name, err)
		}
	}
	for name := range targets {
		if !have[name] {
			t.Errorf("requestFixtureTargets names %s, which is not in %s: delete the entry", name, iosRequestFixtureDir)
		}
	}
	if len(have) == 0 {
		t.Error("no request fixtures found: the directory moved?")
	}
}

type compatEntry struct{ tag, kind string }

func compatBaselines(t *testing.T) []compatEntry {
	t.Helper()
	b, err := os.ReadFile(compatManifest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []compatEntry
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || (f[1] != "ios" && f[1] != "web") {
			t.Fatalf("%s: bad line %q, want \"<tag> ios|web\"", compatManifest, line)
		}
		out = append(out, compatEntry{tag: f[0], kind: f[1]})
	}
	return out
}

func readBaselineDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func decodeStrict(b []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

// What an old iPhone build read still decodes into what the server sends now.
func TestOldIOSResponsesStillDecodeIntoTheServerTypes(t *testing.T) {
	targets := iosFixtureTargets()
	for _, e := range compatBaselines(t) {
		if e.kind != "ios" {
			continue
		}
		dir := filepath.Join(compatDir, e.tag, "responses")
		names := readBaselineDir(t, dir)
		if len(names) == 0 {
			t.Errorf("baseline %s has no responses: refreeze it (ios/freeze-compat.sh %s ios)", e.tag, e.tag)
		}
		for _, name := range names {
			t.Run(e.tag+"/"+name, func(t *testing.T) {
				if _, ok := fixturesCheckedElsewhere[name]; ok {
					return
				}
				if why, ok := compatRetiredFixtures[name]; ok {
					if why == "" {
						t.Errorf("compatRetiredFixtures[%q] has no reason", name)
					}
					return
				}
				target, ok := targets[name]
				if !ok {
					t.Fatalf("%s read %s, which the server has no type for any more: if the endpoint was retired on purpose, add it to compatRetiredFixtures with why", e.tag, name)
				}
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := decodeStrict(b, target); err != nil {
					t.Fatalf("build %s reads %s and the server no longer sends it that way: %v\n(a field it reads was removed, renamed or retyped; keep sending it, or retire the baseline with min_ios_build)", e.tag, name, err)
				}
			})
		}
	}
}

// What an old build sends is still accepted.
func TestOldIOSRequestBodiesAreStillAccepted(t *testing.T) {
	targets := requestFixtureTargets()
	for _, e := range compatBaselines(t) {
		if e.kind != "ios" {
			continue
		}
		dir := filepath.Join(compatDir, e.tag, "requests")
		for _, name := range readBaselineDir(t, dir) {
			t.Run(e.tag+"/"+name, func(t *testing.T) {
				target, ok := targets[name]
				if !ok {
					t.Fatalf("%s sends %s, which has no entry in requestFixtureTargets: add its server request type", e.tag, name)
				}
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := decodeStrict(b, target); err != nil {
					t.Fatalf("build %s sends %s and the server would refuse it (400): %v\n(a request field was removed or renamed: keep accepting the old name)", e.tag, name, err)
				}
			})
		}
	}
}

// The routes an old build calls are still mounted, with the same method.
func TestOldClientRoutesAreStillMounted(t *testing.T) {
	routes := routesOf(t, contractRouter(t))
	for _, e := range compatBaselines(t) {
		b, err := os.ReadFile(filepath.Join(compatDir, e.tag, "routes.txt"))
		if err != nil {
			t.Fatalf("baseline %s has no routes.txt: refreeze it (ios/freeze-compat.sh %s %s): %v", e.tag, e.tag, e.kind, err)
		}
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			n++
			method, path, ok := strings.Cut(line, " ")
			if !ok {
				t.Fatalf("%s routes.txt: bad line %q", e.tag, line)
			}
			if why, retired := compatRetiredRoutes[line]; retired {
				if why == "" {
					t.Errorf("compatRetiredRoutes[%q] has no reason", line)
				}
				continue
			}
			side := "web"
			if e.kind == "ios" {
				side = "swift"
			}
			if _, ignored := ignoredClientCalls[side+" "+line]; ignored {
				continue
			}
			cs := splitPath(path)
			found := false
			for _, r := range routes {
				if r.method == method && matchRoute(cs, r.segs) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("build %s calls %s and the server has no such route any more (retired on purpose? add it to compatRetiredRoutes with why)", e.tag, line)
			}
		}
		if n < 25 {
			t.Errorf("baseline %s lists only %d routes: refreeze it", e.tag, n)
		}
	}
}

// TestFreezeCompatBaseline writes (or refreshes) a baseline from a git tag. Run it through ios/freeze-compat.sh; it is skipped otherwise.
func TestFreezeCompatBaseline(t *testing.T) {
	tag, kind := os.Getenv("RIVOLT_FREEZE_COMPAT_TAG"), os.Getenv("RIVOLT_FREEZE_COMPAT_KIND")
	if tag == "" {
		t.Skip("set by ios/freeze-compat.sh")
	}
	if kind != "ios" && kind != "web" {
		t.Fatalf("kind %q, want ios or web", kind)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { return exec.Command("git", "-C", repo, "cat-file", "-e", tag+":"+p).Run() == nil }
	src := "ios/Rivolt"
	if kind == "web" {
		src = "web/src"
	}
	paths := []string{src}
	if kind == "ios" {
		paths = append(paths, "ios/RivoltTests/Fixtures", "ios/RivoltTests/RequestFixtures")
	}
	tmp := t.TempDir()
	for _, p := range paths {
		if !exists(p) {
			if p == src {
				t.Fatalf("%s has no %s", tag, p)
			}
			continue
		}
		cmd := exec.Command("sh", "-c", fmt.Sprintf("git -C %q archive %q -- %q | tar -x -C %q", repo, tag, p, tmp))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("archive %s: %v\n%s", p, err, out)
		}
	}
	dest := filepath.Join(compatDir, tag)
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	copyJSON := func(from, to string) {
		entries, err := os.ReadDir(from)
		if err != nil {
			return
		}
		if err := os.MkdirAll(to, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(from, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(to, e.Name()), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if kind == "ios" {
		copyJSON(filepath.Join(tmp, "ios/RivoltTests/Fixtures"), filepath.Join(dest, "responses"))
		copyJSON(filepath.Join(tmp, "ios/RivoltTests/RequestFixtures"), filepath.Join(dest, "requests"))
	}
	seen := map[string]bool{}
	var lines []string
	for _, c := range clientCalls(t, filepath.Join(tmp, src), kind == "ios") {
		l := c.method + " " + c.path
		if !seen[l] {
			seen[l] = true
			lines = append(lines, l)
		}
	}
	sort.Strings(lines)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "routes.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The manifest keeps one line per tag.
	var keep []string
	if b, err := os.ReadFile(compatManifest); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) == 2 && f[0] == tag {
				continue
			}
			if strings.TrimSpace(l) != "" {
				keep = append(keep, l)
			}
		}
	}
	keep = append(keep, tag+" "+kind)
	if err := os.WriteFile(compatManifest, []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("froze %s (%s): %d routes", tag, kind, len(lines))
}
