package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// verdict runs release-verdict.sh for a final in a throwaway repo: a v1.0.0 commit, then one more commit that carries the given files. gh says
// the build is green, releasectl accepts the no-rc reason. It returns the summary the script prints.
func verdict(t *testing.T, files map[string]string, extra ...string) string {
	t.Helper()
	repo := t.TempDir()
	run := func(name string, args ...string) {
		t.Helper()
		c := exec.Command(name, args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", name, args, err, out)
		}
	}
	run("git", "init", "-q", "-b", "main")
	run("git", "commit", "-q", "--allow-empty", "--no-gpg-sign", "-m", "first")
	run("git", "tag", "v1.0.0")
	for name, body := range files {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("git", "add", "-A")
	run("git", "commit", "-q", "--allow-empty", "--no-gpg-sign", "-m", "second")

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/usr/bin/env bash\necho 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	infra := t.TempDir()
	if err := os.MkdirAll(filepath.Join(infra, "ops", "release"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infra, "ops", "release", "releasectl.py"), []byte("import sys\nsys.exit(0)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("bash", mustAbs(t, filepath.Join("..", "..", ".github", "ci", "release-verdict.sh")))
	c.Dir = repo
	c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "VERSION=v1.0.1", "FINAL_BY_WORKFLOW=true", "NO_RC_REASON=fix the sign-in loop",
		"GH_REPO=o/r", "INFRA_DIR="+infra, "SHA=HEAD", "GITHUB_OUTPUT=", "GITHUB_STEP_SUMMARY=")
	c.Env = append(c.Env, extra...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("verdict: %v %s", err, out)
	}
	return string(out)
}

// A final is refused, in the dry run and the real run (both read this verdict), when its hand-written notes are not in the commit to be tagged:
// the tag could not be taken back afterwards.
func TestReleaseVerdictNeedsNotes(t *testing.T) {
	const want = "refuse: release-notes/v1.0.1.md missing or empty in "
	if out := verdict(t, nil); !strings.Contains(out, want) || !strings.Contains(out, "WOULD REFUSE") {
		t.Errorf("no notes file: %q", out)
	}
	if out := verdict(t, map[string]string{"release-notes/v1.0.1.md": ""}); !strings.Contains(out, want) {
		t.Errorf("empty notes file: %q", out)
	}
	if out := verdict(t, map[string]string{"release-notes/v1.0.1.md": " \n\t\n\n"}); !strings.Contains(out, want) {
		t.Errorf("whitespace-only notes file: %q", out)
	}
	if out := verdict(t, map[string]string{"release-notes/v1.0.0.md": "other version\n"}); !strings.Contains(out, want) {
		t.Errorf("another version's notes: %q", out)
	}
	if out := verdict(t, map[string]string{"release-notes/v1.0.1.md": "- A fix.\n"}); strings.Contains(out, "release-notes") || !strings.Contains(out, "WOULD TAG v1.0.1") {
		t.Errorf("notes present: %q", out)
	}
	// A release candidate has no notes file.
	if out := verdict(t, nil, "RC=1", "FINAL_BY_WORKFLOW="); strings.Contains(out, "release-notes") {
		t.Errorf("rc: %q", out)
	}
}
