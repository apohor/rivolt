//go:build integration

package stripebill

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// An event for account A that names a Stripe customer which is already account B's must not poison the endpoint (a 500 Stripe retries for days)
// and must not touch B: B keeps its customer and its plan. (billing_customers.stripe_customer_id is UNIQUE.) Pro 29.
func TestAnEventNamingAnotherAccountsCustomerIsAcknowledgedAndChangesNothing(t *testing.T) {
	b := newRig(t, "cust-b")
	if code := b.deliver("checkout_session_completed.json", nil); code != 200 {
		t.Fatalf("B's checkout: %d", code)
	}
	if code := b.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatalf("B's subscription: %d", code)
	}
	customerOf := func(u uuid.UUID) (string, bool) {
		var c string
		err := b.pool.QueryRow(`SELECT stripe_customer_id FROM billing_customers WHERE user_id = $1`, u).Scan(&c)
		return c, err == nil
	}
	bCustomer, ok := customerOf(b.uid)
	if !ok {
		t.Fatal("B has no customer after its checkout")
	}
	bBefore, _, _, bExt, _ := b.row()

	// Account A lives in the same database (a rig of its own would be a new one), with its own handler.
	aUID, err := db.EnsureUser(context.Background(), b.pool, "cust-a")
	if err != nil {
		t.Fatal(err)
	}
	a := &rig{t: t, pool: b.pool, uid: aUID}
	a.h = &Handler{DB: b.pool, Secret: testSecret, Now: func() time.Time { return t0 }}
	cross := func(s string) string {
		s = evtID.ReplaceAllString(s, `"id": "evt_cross_1"`)
		s = strings.ReplaceAll(s, `"customer": "cus_`+aUID.String()[:8]+`"`, `"customer": "`+bCustomer+`"`)
		return strings.ReplaceAll(s, `"id": "sub_`+aUID.String()[:8]+`"`, `"id": "sub_cross_a"`)
	}
	if code := a.deliver("subscription_created_active.json", cross); code != http.StatusOK {
		t.Errorf("an event naming another account's customer was answered %d, want 200 (a 5xx is retried for days)", code)
	}
	if c, ok := customerOf(b.uid); !ok || c != bCustomer {
		t.Errorf("B's customer link changed: %q (had %q)", c, bCustomer)
	}
	if st, _, _, ext, ok := b.row(); !ok || st != bBefore || ext != bExt {
		t.Errorf("B's entitlement changed: %s/%s (had %s/%s)", st, ext, bBefore, bExt)
	}
	if c, ok := customerOf(aUID); ok && c == bCustomer {
		t.Errorf("A was given B's customer %q", c)
	}
}
