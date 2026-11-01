package entitlements

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// What --apply says when it refuses. The repeat after a successful apply used to read "the accounts changed since the dry run", which
// sent an operator hunting for a change that never happened.
func TestFoundingRefusalSaysWhatHappened(t *testing.T) {
	ids := []uuid.UUID{uuid.MustParse("00000000-0000-0000-0000-000000000001"), uuid.MustParse("00000000-0000-0000-0000-000000000002")}
	pending := FoundingReport{Eligible: 5, Already: 3, ToGrant: 2, IDs: ids}
	done := FoundingReport{Eligible: 5, Already: 5, ToGrant: 0}

	if got := foundingRefusal(pending, 2, pending.IDsHash()); got != "" {
		t.Fatalf("the dry run's own count and hash refused: %q", got)
	}

	// A repeat after the grant was applied: say so, with the counts, and that nothing was written.
	rep := foundingRefusal(done, 2, pending.IDsHash())
	for _, want := range []string{"already applied", "5 account(s) at launch", "all 5 already founding", "Nothing was written", "to grant 0"} {
		if !strings.Contains(rep, want) {
			t.Errorf("repeat apply: %q lacks %q", rep, want)
		}
	}
	if strings.Contains(rep, "accounts changed") {
		t.Errorf("repeat apply still blames a change: %q", rep)
	}

	// Typed wrong while the grant is still pending: name both pairs, not a change.
	wrong := foundingRefusal(pending, 99, pending.IDsHash())
	for _, want := range []string{"you passed 99", "to grant now is 2", "Nothing was written", "same --launch instant"} {
		if !strings.Contains(wrong, want) {
			t.Errorf("wrong count: %q lacks %q", wrong, want)
		}
	}
	if strings.Contains(foundingRefusal(pending, 2, strings.Repeat("0", 64)), "already applied") {
		t.Error("a wrong hash on a pending grant claims it was already applied")
	}

	// The expected values left out entirely.
	for _, c := range []struct {
		n   int
		sha string
	}{{-1, ""}, {2, ""}, {-1, pending.IDsHash()}} {
		if got := foundingRefusal(pending, c.n, c.sha); !strings.Contains(got, "needs --expect-count and --expect-sha") {
			t.Errorf("(%d, %q): %q", c.n, c.sha, got)
		}
	}
}
