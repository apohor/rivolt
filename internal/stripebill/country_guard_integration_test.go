//go:build integration

package stripebill

import (
	"strings"
	"sync"
	"testing"
)

// A billing address outside the US is reported and nothing more: the Checkout event is applied (200, applied) and the subscription that
// follows still grants the account its Pro. Refusing silently would be worse.
func TestNonUSAccountStillGetsPro(t *testing.T) {
	r := newRig(t, "stripe-non-us")
	var mu sync.Mutex
	var counted []string
	r.h.OnNonUS = func(c string) { mu.Lock(); counted = append(counted, c); mu.Unlock() }
	if code := r.deliver("checkout_session_completed.json", func(b string) string {
		return strings.Replace(b, `"payment_status": "paid",`, `"payment_status": "paid", "customer_details": {"address": {"country": "DE"}},`, 1)
	}); code != 200 {
		t.Fatalf("checkout.session.completed from Germany: %d, want 200", code)
	}
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatalf("subscription.created: %d", code)
	}
	if status, _, _, _, ok := r.row(); !ok || status != "active" {
		t.Errorf("entitlement row after a non-US checkout: %q (found %v), want active", status, ok)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(counted) != 1 || counted[0] != "DE" {
		t.Errorf("counted %v, want [DE]", counted)
	}
}
