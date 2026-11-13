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
func job(t *testing.T, name string) string { return jobOf(t, "build.yml", name) }

func jobOf(t *testing.T, file, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", file))
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

// The other things a v*.*.* tag starts. A release candidate's GitHub Release is a prerelease that is never "Latest", its notes are
// "since the last release" (not since the last rc), nothing is announced for it, and a manual publish run on an rc ref cannot move `latest`.
func TestOtherTagWorkflowsKnowReleaseCandidates(t *testing.T) {
	notes := jobOf(t, "release-notes.yml", "publish")
	for _, want := range []string{
		".github/ci/tag-kind.sh",      // the same classification, and an invalid tag makes no release
		"--prerelease --latest=false", // an rc is a prerelease and never Latest
		"--match 'v[0-9]*'",           // previous release: v-tags only (not ios-v...)
		"--exclude='*-*'",             // ... and never a prerelease
		`"${flags[@]}"`,               // the flags reach gh release create
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("release-notes.yml lost %q", want)
		}
	}
	if ch := jobOf(t, "release-channels.yml", "discussion"); !strings.Contains(ch, "!github.event.release.prerelease") {
		t.Error("release-channels.yml would announce a prerelease")
	}
	if pub := job(t, "publish"); !strings.Contains(pub, "!contains(github.ref_name, '-')") {
		t.Error("publish can move `latest` on a prerelease tag ref")
	}
}

// preview-tag.sh against a local bare "rivolt-infra": a pin and an unpin change both preview values files in one commit that lands on main,
// a repeat is a no-op, an unpin of an unpinned preview does nothing, and bad input is refused before anything is committed.
func TestPreviewTag(t *testing.T) {
	sh := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v in %s: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin.git")
	seed := filepath.Join(tmp, "seed")
	sh(tmp, "git", "init", "-q", "--bare", "-b", "main", origin)
	sh(tmp, "git", "clone", "-q", origin, seed)
	sh(seed, "git", "config", "user.name", "t")
	sh(seed, "git", "config", "user.email", "t@example.com")
	for _, d := range []string{"rivolt-preview", "rivolt-admin-preview"} {
		if err := os.MkdirAll(filepath.Join(seed, "apps", "platform", d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(seed, "apps", "platform", d, "values.yaml"), []byte("image:\n  repository: x\n  tag: sha-0598c55 # bumped by CI\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sh(seed, "git", "add", ".")
	sh(seed, "git", "commit", "-q", "-m", "seed")
	sh(seed, "git", "push", "-q", "origin", "HEAD:main")
	work := filepath.Join(tmp, "work")
	sh(tmp, "git", "clone", "-q", origin, work)
	abs, _ := filepath.Abs(filepath.Join("..", "..", ".github", "ci", "preview-tag.sh"))
	src := strings.Repeat("a", 40)
	run := func(args ...string) (string, error) {
		c := exec.Command("bash", append([]string{abs}, args...)...)
		c.Dir = work
		out, err := c.CombinedOutput()
		return string(out), err
	}
	tagOn := func(dir string) string {
		return sh(dir, "git", "show", "origin/main:apps/platform/rivolt-admin-preview/values.yaml")
	}
	head := func() string { sh(work, "git", "fetch", "-q", "origin", "main"); return sh(work, "git", "rev-parse", "origin/main") }

	before := head()
	if out, err := run("unpin", "sha-1234567", src, "m"); err != nil || !strings.Contains(out, "not pinned") || head() != before {
		t.Fatalf("unpin of an unpinned preview: %v\n%s", err, out)
	}
	for _, bad := range [][]string{{"pin", "sha-1234567", src, "m"}, {"pin", "0.1.2", src, "m"}, {"pin", "0.1.2-rc.0", src, "m"}, {"unpin", "0.1.2-rc.1", src, "m"}, {"pin", "0.1.2-rc.1", "abc", "m"}, {"nope", "0.1.2-rc.1", src, "m"}} {
		if out, err := run(bad...); err == nil || head() != before {
			t.Errorf("%v accepted: %s", bad, out)
		}
	}
	if out, err := run("pin", "0.41.19-rc.1", src, "pin it"); err != nil {
		t.Fatalf("pin: %v\n%s", err, out)
	}
	for _, f := range []string{"rivolt-preview", "rivolt-admin-preview"} {
		if v := sh(work, "git", "show", "origin/main:apps/platform/"+f+"/values.yaml"); !strings.Contains(v, `tag: "0.41.19-rc.1" # bumped by CI`) {
			t.Errorf("%s after the pin:\n%s", f, v)
		}
	}
	if msg := sh(work, "git", "log", "-1", "--format=%B", "origin/main"); !strings.Contains(msg, "Source-SHA: "+src) {
		t.Errorf("pin commit message %q lacks the Source-SHA line", msg)
	}
	pinned := head()
	if out, err := run("pin", "0.41.19-rc.1", src, "again"); err != nil || head() != pinned {
		t.Fatalf("repeat pin changed main or failed: %v\n%s", err, out)
	}
	if out, err := run("unpin", "sha-76fd801", src, "unpin it"); err != nil {
		t.Fatalf("unpin: %v\n%s", err, out)
	}
	if v := tagOn(work); !strings.Contains(v, "tag: sha-76fd801 # bumped by CI") {
		t.Errorf("admin-preview after the unpin:\n%s", v)
	}
}
