package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func carryDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWithCarryServesOnlyWhatTheBuildLacksAndOnlyHashedAssets(t *testing.T) {
	base := fstest.MapFS{
		"index.html":               {Data: []byte("shell")},
		"assets/index-AAAAAAAA.js": {Data: []byte("own")},
	}
	dir := carryDir(t, map[string]string{
		"assets/index-AAAAAAAA.js": "carry copy must not win",
		"assets/old-BBBBBBBB.js":   "old chunk",
		"assets/notes.txt":         "not hashed",
		"MANIFEST.json":            `{"files":{"assets/old-BBBBBBBB.js":1},"missing":["0.40.0"]}`,
		"secret.txt":               "x",
	})
	f, info := WithCarry(base, dir)
	if info.Files != 1 || len(info.Missing) != 1 {
		t.Fatalf("info %+v", info)
	}
	read := func(name string) (string, error) {
		b, err := fs.ReadFile(f, name)
		return string(b), err
	}
	if s, err := read("assets/index-AAAAAAAA.js"); err != nil || s != "own" {
		t.Errorf("a file the build has: %q %v, want the build's own", s, err)
	}
	if s, err := read("assets/old-BBBBBBBB.js"); err != nil || s != "old chunk" {
		t.Errorf("a chunk only in the carry dir: %q %v, want it served", s, err)
	}
	for _, name := range []string{"assets/notes.txt", "MANIFEST.json", "secret.txt", "assets/missing-CCCCCCCC.js", "assets", "../secret.txt", "assets/../secret.txt"} {
		if _, err := read(name); err == nil {
			t.Errorf("%q was served from the carry dir", name)
		}
	}
	// No listing of the carry dir through the directory: the directory is the build's own.
	if ents, err := fs.ReadDir(f, "assets"); err != nil || len(ents) != 1 {
		t.Errorf("assets listing %v %v, want only the build's own file", ents, err)
	}
}

func TestWithCarryIgnoresASymlinkAndAMissingDir(t *testing.T) {
	base := fstest.MapFS{"index.html": {Data: []byte("shell")}}
	dir := carryDir(t, map[string]string{"outside.js": "secret"})
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside.js"), filepath.Join(dir, "assets", "link-DDDDDDDD.js")); err != nil {
		t.Skip("no symlinks here")
	}
	f, _ := WithCarry(base, dir)
	if _, err := fs.ReadFile(f, "assets/link-DDDDDDDD.js"); err == nil {
		t.Error("a symlink in the carry dir was followed")
	}
	if g, info := WithCarry(base, filepath.Join(dir, "nope")); g == nil || info.Files != 0 {
		t.Error("a missing dir must return the base unchanged")
	} else if _, ok := g.(fstest.MapFS); !ok {
		t.Error("a missing dir wrapped the base")
	}
	if g, _ := WithCarry(nil, dir); g != nil {
		t.Error("a nil base must stay nil")
	}
}
