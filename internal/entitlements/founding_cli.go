package entitlements

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

// RunFounding is the body of `rivolt entitlements-founding` (here, not in cmd/rivolt, so a test drives it against Postgres without pulling
// in the web bundle). It returns the process exit code.
func RunFounding(ctx context.Context, pool *sql.DB, out, errOut io.Writer, demoID uuid.UUID, launch time.Time, apply bool, expectCount int, expectSHA string) int {
	rep, err := PlanFounding(ctx, pool, demoID, launch)
	if err != nil {
		fmt.Fprintln(errOut, "entitlements-founding:", err)
		return 1
	}
	fmt.Fprintf(out, "launch %s, founding year ends %s\n", launch.UTC().Format(time.RFC3339), FoundingEnd(launch).Format(time.RFC3339))
	fmt.Fprintf(out, "accounts at launch (demo excluded): %d, already founding: %d, to grant: %d\n", rep.Eligible, rep.Already, rep.ToGrant)
	fmt.Fprintf(out, "ids sha256: %s\n", rep.IDsHash())
	if !apply {
		for _, id := range rep.IDs {
			fmt.Fprintf(out, "  %s\n", id.String()[:8])
		}
		fmt.Fprintln(out, "dry run: nothing written (--apply --expect-count N --expect-sha SHA to grant)")
		return 0
	}
	if msg := foundingRefusal(rep, expectCount, expectSHA); msg != "" {
		fmt.Fprintln(errOut, "entitlements-founding:", msg)
		return 1
	}
	n, err := ApplyFounding(ctx, pool, demoID, launch)
	if err != nil {
		fmt.Fprintln(errOut, "entitlements-founding:", err)
		return 1
	}
	fmt.Fprintf(out, "granted %d; founding rows now equal the accounts at launch minus the demo\n", n)
	return 0
}

// foundingRefusal is why --apply must not go ahead, or "" when it may. Nothing is written when it is not empty. It tells three refusals
// apart, because the operator has to do something different for each:
//   - nothing left to grant (to grant 0): the grant is already applied for this launch instant; a repeat is a refusal, not a no-op, and the
//     dry run shows to grant 0;
//   - no --expect-count / --expect-sha at all: the values come from the dry run;
//   - anything else: the expected values are not the dry run's (typed wrong, a different --launch instant, or accounts really changed).
func foundingRefusal(rep FoundingReport, expectCount int, expectSHA string) string {
	if expectCount == rep.ToGrant && expectSHA == rep.IDsHash() {
		return ""
	}
	switch {
	case rep.ToGrant == 0:
		return fmt.Sprintf("the founding grant is already applied for this launch instant: %d account(s) at launch, all %d already founding, nothing to grant "+
			"(you passed --expect-count %d --expect-sha %s). Nothing was written. A repeat --apply is refused; the dry run shows to grant 0",
			rep.Eligible, rep.Already, expectCount, expectSHA)
	case expectCount < 0 || expectSHA == "":
		return fmt.Sprintf("--apply needs --expect-count and --expect-sha from the dry run (now: to grant %d, ids sha256 %s). Nothing was written",
			rep.ToGrant, rep.IDsHash())
	}
	return fmt.Sprintf("the expected count/hash is not the dry run's (you passed %d / %s; to grant now is %d / %s). Nothing was written. "+
		"Use the dry run's own count and hash with the same --launch instant; if you did, the accounts changed since the dry run, so run it again",
		expectCount, expectSHA, rep.ToGrant, rep.IDsHash())
}
