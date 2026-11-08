package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The e2e_forward build tag (the Rivian endpoint override, rivian_endpoint_e2e.go) must never reach a release binary or an image: no file that
// builds one may pass it. The multi-pod forward e2e workflow and its harness are the only places the tag may appear.
func TestReleaseBuildsDoNotUseTheE2ETag(t *testing.T) {
	root := filepath.Join("..", "..")
	var builders []string
	builders = append(builders, filepath.Join(root, "Dockerfile"), filepath.Join(root, "Makefile"))
	wf, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	builders = append(builders, wf...)
	for _, p := range []string{".goreleaser.yml", ".goreleaser.yaml", "scripts"} {
		builders = append(builders, filepath.Join(root, p))
	}
	checked := 0
	for _, p := range builders {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		var files []string
		if info.IsDir() {
			_ = filepath.Walk(p, func(q string, i os.FileInfo, err error) error {
				if err == nil && !i.IsDir() {
					files = append(files, q)
				}
				return nil
			})
		} else {
			files = []string{p}
		}
		for _, f := range files {
			if strings.HasSuffix(filepath.ToSlash(f), ".github/workflows/forward-e2e.yml") {
				continue // the workflow that builds the test binary
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			checked++
			if strings.Contains(string(b), "e2e_forward") {
				t.Errorf("%s mentions the e2e_forward build tag: only the forward e2e workflow and harness may build with it", f)
			}
		}
	}
	if checked < 3 {
		t.Fatalf("only %d build files checked; the paths are wrong", checked)
	}
}
