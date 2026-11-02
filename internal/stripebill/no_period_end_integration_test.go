//go:build integration

package stripebill

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

var noPeriodCreated = regexp.MustCompile(`"created": [0-9]+`)

// A cancel that carries no period end anywhere (neither on the subscription nor its items) must still end the subscription: it is the one fact
// the event exists to say. (Pro 29.)
func TestACancelWithoutAPeriodEndStillEndsPro(t *testing.T) {
	now := time.Unix(1790500000, 0)
	r := newRig(t, "noperiod")
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); !st.Pro {
		t.Fatalf("setup: not Pro after the active subscription: %+v", st)
	}
	code := r.deliver("subscription_deleted.json", func(s string) string {
		s = strings.ReplaceAll(s, `"current_period_end": 1821536000`, `"x_removed_period_end": 0`)
		s = evtID.ReplaceAllString(s, `"id": "evt_noend"`)
		return noPeriodCreated.ReplaceAllString(s, fmt.Sprintf(`"created": %d`, 1790950000))
	})
	if code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("a cancel without a period end left Pro on: %+v", st)
	}
}

// The stale guard still applies to such a cancel: an older delete (no period end) delivered after a newer renewal must not end the renewed
// subscription. (Pro 29, DA's check.)
func TestAnOlderCancelWithoutAPeriodEndCannotEndARenewedSubscription(t *testing.T) {
	now := time.Unix(1790500000, 0)
	r := newRig(t, "noperiod-stale")
	at := func(id string, created int64) func(string) string {
		return func(s string) string {
			s = evtID.ReplaceAllString(s, `"id": "`+id+`"`)
			return noPeriodCreated.ReplaceAllString(s, fmt.Sprintf(`"created": %d`, created))
		}
	}
	if code := r.deliver("subscription_created_active.json", at("evt_renewed", 1790900000)); code != 200 {
		t.Fatal(code)
	}
	code := r.deliver("subscription_deleted.json", func(s string) string {
		return at("evt_older_cancel", 1790800000)(strings.ReplaceAll(s, `"current_period_end": 1821536000`, `"x_removed_period_end": 0`))
	})
	if code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); !st.Pro {
		t.Errorf("an older cancel ended a subscription renewed after it: %+v", st)
	}
}
