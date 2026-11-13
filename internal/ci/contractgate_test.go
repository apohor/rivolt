package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes a repo with one commit per entry of steps; each step maps file path to content and, when tag is set, tags the commit.
type step struct {
	files map[string]string
	tag   string
}

func gateRun(t *testing.T, steps []step, tag string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	for i, s := range steps {
		for p, body := range s.files {
			full := filepath.Join(dir, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("add", "-A")
		git("commit", "-q", "-m", "c"+string(rune('a'+i)), "--allow-empty", "--no-gpg-sign")
		if s.tag != "" {
			git("tag", "-a", "-m", s.tag, s.tag)
		}
	}
	c := exec.Command("bash", mustAbs(t, filepath.Join("..", "..", ".github", "ci", "contract-gate.sh")), tag)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

const mig = "internal/db/migrations/"

// The gate stops a contract migration, passes plain ones, and fails closed when it can't tell what the release changes.
func TestContractGate(t *testing.T) {
	base := step{files: map[string]string{mig + "0001_a.sql": "create table a();"}, tag: "v1.0.0"}
	cases := []struct {
		name    string
		steps   []step
		tag     string
		wantErr bool
		wantOut string
	}{
		{"no migration change", []step{base, {files: map[string]string{"x": "1"}, tag: "v1.0.1"}}, "v1.0.1", false, "no migration changes"},
		{"plain new migration", []step{base, {files: map[string]string{mig + "0002_b.sql": "alter table a add b int;"}, tag: "v1.0.1"}}, "v1.0.1", false, "0002_b.sql"},
		{"contract marker", []step{base, {files: map[string]string{mig + "0002_b.sql": "-- contract: drop old column\nalter table a drop b;"}, tag: "v1.0.1"}}, "v1.0.1", true, "destructive"},
		{"indented marker", []step{base, {files: map[string]string{mig + "0002_b.sql": "  --contract: x\nselect 1;"}, tag: "v1.0.1"}}, "v1.0.1", true, "destructive"},
		{"marker added to an existing file", []step{base, {files: map[string]string{mig + "0001_a.sql": "-- contract: x\ndrop table a;"}, tag: "v1.0.1"}}, "v1.0.1", true, "destructive"},
		{"marker in an older release is not this release's", []step{base, {files: map[string]string{mig + "0002_b.sql": "-- contract: x\nselect 1;"}, tag: "v1.0.1"}, {files: map[string]string{mig + "0003_c.sql": "select 2;"}, tag: "v1.0.2"}}, "v1.0.2", false, "0003_c.sql"},
		{"rc between finals is skipped as the base", []step{base, {files: map[string]string{mig + "0002_b.sql": "-- contract: x\nselect 1;"}, tag: "v1.0.1-rc.1"}, {files: map[string]string{"y": "1"}, tag: "v1.0.1"}}, "v1.0.1", true, "destructive"},
		{"no previous final tag", []step{{files: map[string]string{mig + "0001_a.sql": "select 1;"}, tag: "v1.0.0"}}, "v1.0.0", true, "no previous final"},
	}
	for _, c := range cases {
		out, err := gateRun(t, c.steps, c.tag)
		if (err != nil) != c.wantErr || !strings.Contains(out, c.wantOut) {
			t.Errorf("%s: err=%v out=%q, want error=%v containing %q", c.name, err, out, c.wantErr, c.wantOut)
		}
	}
}

// bump-prod must not run unless the gate has passed.
func TestBumpProdNeedsContractGate(t *testing.T) {
	b := job(t, "bump-prod")
	if !strings.Contains(b, "contract-gate") {
		t.Errorf("bump-prod does not need the contract-gate job")
	}
}
