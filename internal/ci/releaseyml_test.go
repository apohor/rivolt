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

// A final tag, however it was pushed, runs the release-candidate gate in tag-validate, and only for finals.
func TestTagValidateGatesFinals(t *testing.T) {
	j := jobOf(t, "build.yml", "tag-validate")
	i := strings.Index(j, "name: Require the gated release candidate (final tags)")
	if i < 0 {
		t.Fatal("tag-validate has no final-gate step")
	}
	step := j[i:]
	if n := strings.Index(step[10:], "- name:"); n > 0 {
		step = step[:n+10]
	}
	for _, want := range []string{"steps.kind.outputs.kind == 'final'", ".github/ci/final-gate.sh", "INFRA_TOKEN"} {
		if !strings.Contains(step, want) {
			t.Errorf("final-gate step lacks %q", want)
		}
	}
}
