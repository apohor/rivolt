// Package ci holds tests of the release pipeline itself: what a release tag is and what the tag run may do with it.
package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func script(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command("bash", append([]string{filepath.Join("..", "..", ".github", "ci", name)}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// A release candidate is vX.Y.Z-rc.N with N from 1; a release is vX.Y.Z. Everything else is refused before the tag run can retag or
// bump anything.
func TestTagKind(t *testing.T) {
	for tag, want := range map[string]string{
		"v0.40.21": "final", "v1.0.0": "final", "v10.20.30": "final",
		"v0.40.21-rc.1": "rc", "v0.40.21-rc.12": "rc",
	} {
		if got, err := script(t, "tag-kind.sh", tag); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{
		"", "0.40.21", "v0.40", "v0.40.21-rc", "v0.40.21-rc.0", "v0.40.21-rc.01", "v0.40.21-rc.1.2", "v0.40.21-beta.1", "v0.40.21-RC.1",
		"v01.2.3", "v1.2.3+meta", "v1.2.3-rc.1+meta", "v1.2.3-", "ios-v0.2.31", "vv1.2.3",
	} {
		if got, err := script(t, "tag-kind.sh", tag); err == nil {
			t.Errorf("%q was accepted as %q", tag, got)
		}
	}
}

// What the retag job attaches: an rc gets only its own tag, so it can never move X.Y or latest, which is where prod's image comes from.
func TestRetagTags(t *testing.T) {
	for tag, want := range map[string]string{
		"v0.40.21":      "0.40.21 0.40 latest",
		"v1.2.3":        "1.2.3 1.2 latest",
		"v0.40.21-rc.1": "0.40.21-rc.1",
		"v1.2.3-rc.12":  "1.2.3-rc.12",
	} {
		got, err := script(t, "retag-tags.sh", tag)
		if err != nil || strings.Join(strings.Fields(got), " ") != want {
			t.Errorf("%s retags %q (%v), want %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{"v1.2.3-beta.1", "v1.2.3-rc", "garbage"} {
		if got, err := script(t, "retag-tags.sh", tag); err == nil {
			t.Errorf("%q retagged %q", tag, got)
		}
	}
}

// job returns the text of one top-level job of build.yml.
func job(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "build.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + `:\n`).FindIndex(b)
	if m == nil {
		t.Fatalf("build.yml has no job %q", name)
	}
	rest := b[m[1]:]
	if n := regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\n`).FindIndex(rest); n != nil {
		rest = rest[:n[0]]
	}
	return string(rest)
}

// The guard that an rc can never reach prod values. bump-prod rewrites rivolt-infra's prod app.yaml and values.yaml; it runs only for a
// final tag (tag-validate's verdict), and refuses a prerelease version itself even if that condition is ever loosened. tag-validate
// refuses any tag that is neither shape, and retag takes its tag set from retag-tags.sh.
func TestAReleaseCandidateNeverReachesProd(t *testing.T) {
	bump := job(t, "bump-prod")
	for _, want := range []string{
		"needs.tag-validate.outputs.kind == 'final'", // the job condition
		"needs: [tag-validate, retag]",               // so that condition can read tag-validate's output
		`*-*)`,                                       // the step's own refusal of any prerelease version
	} {
		if !strings.Contains(bump, want) {
			t.Errorf("bump-prod lost %q: a release candidate could reach prod's values", want)
		}
	}
	if v := job(t, "tag-validate"); !strings.Contains(v, ".github/ci/tag-kind.sh") || !strings.Contains(v, "kind=") {
		t.Error("tag-validate no longer classifies the tag with tag-kind.sh")
	}
	if r := job(t, "retag"); !strings.Contains(r, ".github/ci/retag-tags.sh") || strings.Contains(r, `"${IMAGE}:latest"`) {
		t.Error("retag must take its tag set from retag-tags.sh and name no floating tag itself")
	}
}
