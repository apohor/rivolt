package api

import (
	"time"

	"github.com/apohor/rivolt/internal/histwin"
)

// The window tests run in the steady state: the 30 days' notice of the 12 to 6 month change has run, so every free account has
// histwin.Months. (Unset, the default, is LegacyMonths for everyone: histwin's own tests cover it, and the notice period itself is
// TestWindowNoticeAgainstPostgres.)
func init() { histwin.SetNotice(time.Unix(0, 0)) }
