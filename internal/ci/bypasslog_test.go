package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A stand-in for rivolt-infra's releasectl.py with the two commands bypass-log.sh calls.
const stubCtl = `import sys
if sys.argv[1] == "no-rc-check":
    sys.exit(0 if len(sys.argv[2]) >= 10 else 1)
print("2026-11-13T00:00:00Z | %s | %s | %s" % (sys.argv[2], sys.argv[3], sys.argv[4]))
`

// bypassRun makes a bare rivolt-infra origin and a checkout of it, runs bypass-log.sh on the checkout and returns the origin's log.
func bypassRun(t *testing.T, reason string, rejectPush bool) (log string, out string, err error) {
	t.Helper()
	d := t.TempDir()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir, c.Env = dir, env
		if o, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v\n%s", args, e, o)
		}
	}
	origin, work := filepath.Join(d, "origin.git"), filepath.Join(d, "infra")
	git(d, "init", "-q", "--bare", "-b", "main", origin)
	git(d, "clone", "-q", origin, work)
	if err := os.MkdirAll(filepath.Join(work, "ops", "release"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "ops", "release", "releasectl.py"), []byte(stubCtl), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "-A")
	git(work, "commit", "-q", "--no-gpg-sign", "-m", "init")
	git(work, "push", "-q", "origin", "HEAD:main")
	if rejectPush {
		hook := filepath.Join(origin, "hooks", "pre-receive")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command("bash", mustAbs(t, filepath.Join("..", "..", ".github", "ci", "bypass-log.sh")), work)
	c.Env = append(env, "BYPASS_PUSH_SLEEP=0", "TAG=v1.2.3", "SHA="+finalSHA, "REASON="+reason)
	o, err := c.CombinedOutput()
	show := exec.Command("git", "--git-dir", origin, "show", "main:ops/release/rc-bypass.log")
	l, _ := show.Output()
	return string(l), strings.TrimSpace(string(o)), err
}

// The bypass line reaches rivolt-infra main before the tag; a refused reason or a rejected push fails the job, so no tag follows.
func TestBypassLog(t *testing.T) {
	log, out, err := bypassRun(t, "fix the sign-in loop", false)
	if err != nil || !strings.Contains(log, " | v1.2.3 | "+finalSHA+" | fix the sign-in loop") {
		t.Errorf("accepted bypass: err=%v log=%q out=%q", err, log, out)
	}
	if log, out, err = bypassRun(t, "short", false); err == nil || log != "" {
		t.Errorf("a vague reason must be refused and not logged: err=%v log=%q out=%q", err, log, out)
	}
	if log, out, err = bypassRun(t, "fix the sign-in loop", true); err == nil || !strings.Contains(out, "the final is not tagged") || log != "" {
		t.Errorf("a rejected push must fail the job: err=%v log=%q out=%q", err, log, out)
	}
}
