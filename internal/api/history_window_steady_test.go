package api

import (
	"time"

	"github.com/apohor/rivolt/internal/histwin"
)

// The window tests run in the steady state: the 30 days' notice of the 12 to 6 month change has run, so every free account has
// histwin.Months, set to 6 here. (A setting of 12, the default, or no notice is 12 for everyone: histwin's own tests cover it, and the notice period itself is
// TestWindowNoticeAgainstPostgres.)
func init() {
	histwin.Months = 6
	histwin.SetNotice(time.Unix(0, 0))
}
