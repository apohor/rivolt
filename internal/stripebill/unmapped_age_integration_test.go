//go:build integration

package stripebill

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var unmappedCreated = regexp.MustCompile(`"created": [0-9]+`)

// An event for no known account is retried (500) while it is younger than UnmappedMaxAge, and acknowledged as unmapped_expired once older.
// Both sides of the boundary, for a subscription event and a refund. (Pro 29, DA's ruling.)
func TestUnmappedEventsAreRetriedUntilTheyAreADayOld(t *testing.T) {
	r := newRig(t, "unmapped-age")
	age := int64(UnmappedMaxAge.Seconds())
	for _, c := range []struct {
		file    string
		ageSecs int64
		want    int
		outcome string
	}{
		{"subscription_created_active.json", age - 60, http.StatusInternalServerError, ""},
		{"subscription_created_active.json", age + 60, http.StatusOK, "customer.subscription.created:" + OutcomeUnmappedExpired},
		{"charge_refunded_full.json", age - 60, http.StatusInternalServerError, ""},
		{"charge_refunded_full.json", age + 60, http.StatusOK, "charge.refunded:" + OutcomeUnmappedExpired},
	} {
		id := fmt.Sprintf("evt_unmapped_%s_%d", strings.TrimSuffix(c.file, ".json"), c.ageSecs)
		code := r.deliver(c.file, func(s string) string {
			// Nobody owns this customer and the metadata names no account.
			s = strings.ReplaceAll(s, r.uid.String(), uuid.NewString())
			s = strings.ReplaceAll(s, "cus_"+r.uid.String()[:8], "cus_nobody")
			s = evtID.ReplaceAllString(s, `"id": "`+id+`"`)
			return unmappedCreated.ReplaceAllString(s, fmt.Sprintf(`"created": %d`, t0.Unix()-c.ageSecs))
		})
		if code != c.want {
			t.Errorf("%s aged %ds: answered %d, want %d", c.file, c.ageSecs, code, c.want)
		}
		r.mu.Lock()
		got := strings.Join(r.outcomes, " ")
		r.mu.Unlock()
		if c.outcome != "" && !strings.Contains(got, c.outcome) {
			t.Errorf("%s aged %ds: outcomes %q lack %q", c.file, c.ageSecs, got, c.outcome)
		}
		if c.want == http.StatusInternalServerError && claimedEvent(t, r, id) {
			t.Errorf("%s aged %ds: a retried event was recorded as handled", c.file, c.ageSecs)
		}
	}
}

func claimedEvent(t *testing.T, r *rig, id string) bool {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(`SELECT count(*) FROM stripe_events WHERE event_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}
