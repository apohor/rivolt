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
	if !strings.Contains(j, "statuses: read") {
		t.Error("tag-validate can't read the commit status without statuses: read")
	}
	for _, want := range []string{"steps.kind.outputs.kind == 'final'", ".github/ci/final-gate.sh", "INFRA_TOKEN"} {
		if !strings.Contains(step, want) {
			t.Errorf("final-gate step lacks %q", want)
		}
	}
}

// A final tag needs final=true; one with no gated rc passes through the release-bypass environment and its log job before the tag job.
func TestReleaseFinalPath(t *testing.T) {
	tag := jobOf(t, "release.yml", "tag")
	bypass := jobOf(t, "release.yml", "bypass")
	if !strings.Contains(bypass, "environment: release-bypass") || !strings.Contains(bypass, "bypass-log.sh") {
		t.Error("the bypass job must run in the release-bypass environment and write the log")
	}
	if !strings.Contains(bypass, "inputs.gated_rc == ''") {
		t.Error("a final that names a gated rc must not log a bypass")
	}
	for _, want := range []string{"needs: [verdict, bypass]", "needs.bypass.result == 'success'", "needs.bypass.result == 'skipped'", "inputs.final"} {
		if !strings.Contains(tag, want) {
			t.Errorf("tag job lacks %q", want)
		}
	}
	if !strings.Contains(jobOf(t, "release.yml", "verdict"), "FINAL_BY_WORKFLOW: ${{ inputs.final }}") {
		t.Error("the verdict is not told final=true")
	}
}
