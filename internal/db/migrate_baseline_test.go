package db

import (
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// The embedded baseline names its tag and the ids it covers, sorted and without repeats, and every covered id that still has a file
// is one of the files this tree ships.
func TestEmbeddedBaselineHeader(t *testing.T) {
	b, err := loadBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.tag, "v") || len(b.covers) < 100 {
		t.Fatalf("tag %q, %d covered ids", b.tag, len(b.covers))
	}
	if !sort.StringsAreSorted(b.covers) {
		t.Fatal("covers is not sorted")
	}
	for i, id := range b.covers {
		if i > 0 && id == b.covers[i-1] {
			t.Fatalf("covers repeats %s", id)
		}
	}
	files := map[string]bool{}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		files[strings.TrimSuffix(e.Name(), ".sql")] = true
	}
	last := b.covers[len(b.covers)-1]
	for id := range files {
		if id <= last {
			found := false
			for _, c := range b.covers {
				found = found || c == id
			}
			if !found {
				t.Errorf("%s is shipped and not newer than %s, but the baseline does not cover it", id, last)
			}
		}
	}
}

func TestParseBaselineRejectsAHeaderWithoutIDs(t *testing.T) {
	for _, in := range []string{"", "-- baseline-of: v1\nSELECT 1;", "-- covers: 0001_a\nSELECT 1;"} {
		if _, err := parseBaseline(in); err == nil {
			t.Errorf("parseBaseline(%q) accepted it", in)
		}
	}
	b, err := parseBaseline("-- baseline-of: v1.2.3\n-- covers: 0002_b 0001_a\n--\nSELECT 1;")
	if err != nil || b.tag != "v1.2.3" || strings.Join(b.covers, ",") != "0001_a,0002_b" {
		t.Fatalf("got %+v, %v", b, err)
	}
}
