package webcarry

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/web-carry.sh against a fake docker: what it collects, what it drops by age, and that a source it cannot take is flagged, not fatal.
const fakeDocker = `#!/usr/bin/env bash
# pull -q REF | create REF | cp CID:/app/web-carry DIR | rm CID, over fixtures in $FIX (one dir per image ref, ':' as '_').
case "$1" in
  pull) ref=$3; [ -d "$FIX/${ref//[:\/]/_}" ] || [ -f "$FIX/${ref//[:\/]/_}.nocarry" ] || exit 1 ;;
  create) echo "${2//[:\/]/_}" ;;
  cp) cid=${2%%:*}; [ -d "$FIX/$cid/web-carry" ] || exit 1; cp -R "$FIX/$cid/web-carry" "$3" ;;
  rm) ;;
esac
`

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWebCarryScript(t *testing.T) {
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	root := t.TempDir()
	fix, dist, out, bin := filepath.Join(root, "fix"), filepath.Join(root, "dist"), filepath.Join(root, "out"), filepath.Join(root, "bin")
	const now = 1_000_000_000
	hour := 3600
	img := "ghcr.io/apohor/rivolt"
	// This build's own asset.
	write(t, filepath.Join(dist, "assets", "own-DDDDDDDD.js"), "own")
	// The previous main image: one file 10 h old (kept), one 60 h old (dropped by the 48 h window).
	m := filepath.Join(fix, "ghcr.io_apohor_rivolt_main", "web-carry")
	write(t, filepath.Join(m, "assets", "recent-AAAAAAAA.js"), "recent")
	write(t, filepath.Join(m, "assets", "stale-BBBBBBBB.js"), "stale")
	mf, _ := json.Marshal(map[string]any{"files": map[string]int{"assets/recent-AAAAAAAA.js": now - 10*hour, "assets/stale-BBBBBBBB.js": now - 60*hour}})
	write(t, filepath.Join(m, "MANIFEST.json"), string(mf))
	// The newest final: an old file, kept whatever its age.
	f := filepath.Join(fix, "ghcr.io_apohor_rivolt_0.2.0", "web-carry")
	write(t, filepath.Join(f, "assets", "release-CCCCCCCC.js"), "release")
	ff, _ := json.Marshal(map[string]any{"files": map[string]int{"assets/release-CCCCCCCC.js": now - 500*hour}})
	write(t, filepath.Join(f, "MANIFEST.json"), string(ff))
	// The final before it predates the carry: the image exists, it has no /app/web-carry.
	write(t, filepath.Join(fix, "ghcr.io_apohor_rivolt_0.1.0.nocarry"), "")
	write(t, filepath.Join(bin, "docker"), fakeDocker)
	if err := os.Chmod(filepath.Join(bin, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}

	script, _ := filepath.Abs("../../scripts/web-carry.sh")
	cmd := exec.Command("bash", script, dist, out)
	cmd.Env = append(os.Environ(), "DOCKER="+filepath.Join(bin, "docker"), "FIX="+fix, "REGISTRY_IMAGE="+img, "FINAL_TAGS=0.2.0 0.1.0", "NOW=1000000000")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed (it must not): %v\n%s", err, b)
	}
	if !strings.Contains(string(b), "::warning::") {
		t.Errorf("no warning annotation for the missing source:\n%s", b)
	}
	for name, want := range map[string]bool{"own-DDDDDDDD.js": true, "recent-AAAAAAAA.js": true, "release-CCCCCCCC.js": true, "stale-BBBBBBBB.js": false} {
		_, err := os.Stat(filepath.Join(out, "assets", name))
		if (err == nil) != want {
			t.Errorf("%s collected=%v, want %v", name, err == nil, want)
		}
	}
	raw, err := os.ReadFile(filepath.Join(out, "MANIFEST.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Files   map[string]int64 `json:"files"`
		Sources []string         `json:"sources"`
		Missing []string         `json:"missing"`
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, raw)
	}
	if len(man.Files) != 3 || man.Files["assets/own-DDDDDDDD.js"] != now || man.Files["assets/release-CCCCCCCC.js"] != now-500*int64(hour) {
		t.Errorf("manifest files %v", man.Files)
	}
	if len(man.Missing) != 1 || man.Missing[0] != "0.1.0" || len(man.Sources) != 2 {
		t.Errorf("sources %v missing %v, want 0.1.0 missing (it predates the carry)", man.Sources, man.Missing)
	}

	// The same run with a registry that answers nothing is still green and flags every source.
	out2 := filepath.Join(root, "out2")
	cmd = exec.Command("bash", script, dist, out2)
	cmd.Env = append(os.Environ(), "DOCKER=/bin/false", "REGISTRY_IMAGE="+img, "FINAL_TAGS=0.2.0 0.1.0", "NOW=1000000000")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("a dead registry must not fail the build: %v\n%s", err, b)
	}
	raw, _ = os.ReadFile(filepath.Join(out2, "MANIFEST.json"))
	man.Files, man.Missing, man.Sources = nil, nil, nil
	if err := json.Unmarshal(raw, &man); err != nil || len(man.Missing) != 3 || len(man.Files) != 1 {
		t.Errorf("dead registry: missing %v files %v (%v)\n%s", man.Missing, man.Files, err, raw)
	}
}
