package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	finalSHA = "1111111111111111111111111111111111111111"
	otherSHA = "2222222222222222222222222222222222222222"
)

// fakeGH answers the three reads final-gate.sh makes: the commit status, rivolt-infra's two values files and its rc-bypass.log.
const fakeGH = `#!/usr/bin/env bash
case "$*" in
  *commits/*/status*) echo "$FAKE_QA" ;;
  *rivolt-admin-preview/values.yaml*) printf 'image:\n  tag: %s\n' "$FAKE_ADMIN" ;;
  *rivolt-preview/values.yaml*) printf 'image:\n  tag: %s\n' "$FAKE_PREVIEW" ;;
  *rc-bypass.log*) [ -n "${FAKE_NOLOG:-}" ] && exit 1; printf '%s\n' "$FAKE_LOG" ;;
  *) exit 1 ;;
esac
`

func finalGate(t *testing.T, refs string, env ...string) (string, error) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("bash", mustAbs(t, filepath.Join("..", "..", ".github", "ci", "final-gate.sh")), "v1.2.3", finalSHA)
	c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "GH_REPO=o/r", "INFRA_REPO=o/i", "RC_TAG_REFS="+refs,
		"FAKE_QA=success", `FAKE_PREVIEW="1.2.3-rc.2"`, "FAKE_ADMIN=1.2.3-rc.2")
	c.Env = append(c.Env, env...)
	out, err := c.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// rc.1 and rc.2 are annotated (object line, then the peeled commit); rc.1 is an older commit, rc.2 is the one the final points at.
var rcRefs = "aaaa\trefs/tags/v1.2.3-rc.1\n" + otherSHA + "\trefs/tags/v1.2.3-rc.1^{}\n" +
	"bbbb\trefs/tags/v1.2.3-rc.2\n" + finalSHA + "\trefs/tags/v1.2.3-rc.2^{}\n"

// A final is the gated rc's commit, qa/rc-gate is success and preview runs the rc image; each missing piece refuses the tag.
func TestFinalGate(t *testing.T) {
	cases := []struct {
		name, refs string
		env        []string
		ok         bool
		msg        string
	}{
		{"gated rc, preview on its image", rcRefs, nil, true, "OK: v1.2.3 is v1.2.3-rc.2"},
		{"qa not success", rcRefs, []string{"FAKE_QA=pending"}, false, "'pending', not success"},
		{"qa no status", rcRefs, []string{"FAKE_QA=none"}, false, "'none', not success"},
		{"preview on another image", rcRefs, []string{"FAKE_PREVIEW=sha-abc1234"}, false, "rivolt-preview runs image"},
		{"admin preview on another image", rcRefs, []string{`FAKE_ADMIN="1.2.3-rc.1"`}, false, "rivolt-admin-preview runs image"},
		{"rc at another commit, no bypass", "bbbb\trefs/tags/v1.2.3-rc.1\n" + otherSHA + "\trefs/tags/v1.2.3-rc.1^{}\n", []string{"FAKE_LOG=old | v0.0.1 | x | y"}, false, "no line for it"},
		{"no rc at all, no bypass", "", []string{"FAKE_LOG="}, false, "no line for it"},
		{"no rc, bypass line for tag and sha", "", []string{"FAKE_LOG=2026-11-13T00:00:00Z | v1.2.3 | " + finalSHA + " | fix the sign-in loop"}, true, "records the bypass"},
		{"bypass line for another sha", "", []string{"FAKE_LOG=2026-11-13T00:00:00Z | v1.2.3 | " + otherSHA + " | fix the sign-in loop"}, false, "no line for it"},
		{"bypass line for another tag", "", []string{"FAKE_LOG=2026-11-13T00:00:00Z | v1.2.30 | " + finalSHA + " | fix the sign-in loop"}, false, "no line for it"},
		{"bypass log unreadable", "", []string{"FAKE_NOLOG=1"}, false, "could not be read"},
		{"another version's rc at the commit", "bbbb\trefs/tags/v1.2.30-rc.1\n" + finalSHA + "\trefs/tags/v1.2.30-rc.1^{}\n", []string{"FAKE_LOG="}, false, "no line for it"},
	}
	for _, c := range cases {
		out, err := finalGate(t, c.refs, c.env...)
		if (err == nil) != c.ok || !strings.Contains(out, c.msg) {
			t.Errorf("%s: err=%v out=%q, want ok=%v containing %q", c.name, err, out, c.ok, c.msg)
		}
	}
}

// A lightweight rc tag lists once, with the commit itself.
func TestFinalGateLightweightRC(t *testing.T) {
	out, err := finalGate(t, finalSHA+"\trefs/tags/v1.2.3-rc.1\n", `FAKE_PREVIEW="1.2.3-rc.1"`, "FAKE_ADMIN=1.2.3-rc.1")
	if err != nil || !strings.Contains(out, "v1.2.3-rc.1") {
		t.Errorf("err=%v out=%q", err, out)
	}
}
