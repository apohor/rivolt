package api

import (
	"time"

	"github.com/apohor/rivolt/internal/histwin"
)

// The window tests run with a 6-month setting and a cutoff long past, so a free account created now has histwin.Months (6). (A setting of
// 12, the default, or no cutoff is 12 for everyone: histwin's own tests cover it, and the cutoff itself is TestWindowCutoffAgainstPostgres.)
func init() {
	histwin.Months = 6
	histwin.SetCutoff(time.Unix(0, 0))
}

// testNewAccount is the creation time of an account that has the shorter window under the steady-state setting above.
var testNewAccount = time.Unix(0, 0).Add(time.Hour)
