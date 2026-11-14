package ci

import (
	"strings"
	"testing"
)

// release.yml cuts tags and probe refs only from a run on main: the tag and probe jobs carry the ref guard, and the verdict job fails a
// dispatch on any other ref instead of letting both jobs skip and the run end green with nothing tagged.
func TestReleaseOnlyFromMain(t *testing.T) {
	for _, name := range []string{"tag", "probe"} {
		if j := jobOf(t, "release.yml", name); !strings.Contains(j, "github.ref == 'refs/heads/main'") {
			t.Errorf("release.yml job %s has no main-ref guard", name)
		}
	}
	if j := jobOf(t, "release.yml", "verdict"); !strings.Contains(j, "github.ref != 'refs/heads/main'") {
		t.Errorf("release.yml verdict job does not fail a run off main")
	}
}
