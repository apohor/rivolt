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
	if expectCount != rep.ToGrant || expectSHA != rep.IDsHash() {
		fmt.Fprintf(errOut, "entitlements-founding: the accounts changed since the dry run (expected %d / %s, now %d / %s); run the dry run again\n",
			expectCount, expectSHA, rep.ToGrant, rep.IDsHash())
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
