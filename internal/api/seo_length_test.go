package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Search engines cut a title at about 60 characters and a description at about 155 to 160; every public page stays within a small margin of both
// (titles 70, descriptions 160 with the unofficial line included).
func TestSEOTitlesAndDescriptionsFitTheirLimits(t *testing.T) {
	for host, pages := range seoPages {
		for path, p := range pages {
			if n := len([]rune(p.title)); n > 70 {
				t.Errorf("%s%s: title is %d characters (limit 70): %q", host, path, n, p.title)
			}
			if n := len([]rune(p.desc)); n > 160 {
				t.Errorf("%s%s: description is %d characters (limit 160): %s", host, path, n, strings.TrimSpace(p.desc))
			}
		}
	}
}

// The operator is ZeroDowntime LLC: no page, description or llms.txt line says it is run by "an individual".
func TestNoPublicTextSaysRunByAnIndividual(t *testing.T) {
	for host, pages := range seoPages {
		for path, p := range pages {
			if strings.Contains(p.desc+p.summary+p.title, "an individual") {
				t.Errorf("%s%s: says \"an individual\"", host, path)
			}
		}
	}
	for host := range seoPages {
		if txt, ok := llmsTxt(host); ok && strings.Contains(txt, "an individual") {
			t.Errorf("%s llms.txt says \"an individual\"", host)
		}
	}
	// The Terms' own "an individual claim" in Disputes is legal wording and stays; the other pages don't use the phrase.
	files, err := filepath.Glob("../../web/src/content/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("content files: %v %d", err, len(files))
	}
	for _, f := range files {
		if filepath.Base(f) == "terms.md" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "run by an individual") {
			t.Errorf("%s says it is run by an individual", f)
		}
	}
}
