//go:build integration

package stripebill

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

var sameSecondCreated = regexp.MustCompile(`"created": [0-9]+`)

// A subscription ended and an "active" update stamped with the SAME second (Stripe's created has one-second resolution): whichever the endpoint
// receives last, the ended subscription must not be Pro again. Both orders. (Pro 29.)
func TestASubscriptionEndedInTheSameSecondAsAnActiveUpdateStaysEnded(t *testing.T) {
	now := time.Unix(1790500000, 0)
	const T = 1790600000
	at := func(id string) func(string) string {
		return func(s string) string {
			s = evtID.ReplaceAllString(s, `"id": "`+id+`"`)
			return sameSecondCreated.ReplaceAllString(s, fmt.Sprintf(`"created": %d`, T))
		}
	}
	for _, order := range []string{"ended then active", "active then ended"} {
		r := newRig(t, "tie-"+strings.ReplaceAll(order, " ", "-"))
		ended := func() int { return r.deliver("subscription_deleted.json", at("evt_tie_ended")) }
		active := func() int { return r.deliver("subscription_created_active.json", at("evt_tie_active")) }
		a, b := ended, active
		if order == "active then ended" {
			a, b = active, ended
		}
		if code := a(); code != 200 {
			t.Fatalf("%s, first: %d", order, code)
		}
		if code := b(); code != 200 {
			t.Fatalf("%s, second: %d", order, code)
		}
		if st := r.state(now); st.Pro {
			t.Errorf("%s: a subscription ended at second %d is Pro again: %+v", order, T, st)
		}
	}
}
