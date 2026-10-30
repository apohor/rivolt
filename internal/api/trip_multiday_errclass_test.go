package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

// A refusal by our own Rivian budget is logged as such, not as "other".
func TestErrClassNamesOurRateLimit(t *testing.T) {
	err := fmt.Errorf("plan day 2: %w", &rivian.ErrRateLimited{RetryAfter: time.Second})
	if got := errClass(err); got != "rate_limited" {
		t.Errorf("errClass = %q, want rate_limited", got)
	}
	if got := errClass(fmt.Errorf("something else")); got != "other" {
		t.Errorf("errClass(other) = %q, want other", got)
	}
}
