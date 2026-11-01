//go:build integration

package stripebill

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/entitlements"
)

// Pro 29: adversarial deliveries against the real tables. Test data only: made-up ids, a made-up secret, no real money or account. Each case
// says what the endpoint must do; a case that fails is a finding, listed for DA.

// with replaces the event's id and created time (and optionally more) in a fixture body.
func with(id string, created int64, more ...[2]string) func(string) string {
	return func(s string) string {
		s = evtID.ReplaceAllString(s, `"id": "`+id+`"`)
		s = createdRE.ReplaceAllString(s, fmt.Sprintf(`"created": %d`, created))
		for _, m := range more {
			s = strings.ReplaceAll(s, m[0], m[1])
		}
		return s
	}
}

var createdRE = regexp.MustCompile(`"created": [0-9]+`)

func (r *rig) claimed(id string) bool {
	r.t.Helper()
	var n int
	if err := r.pool.QueryRow(`SELECT count(*) FROM stripe_events WHERE event_id = $1`, id).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n > 0
}

func (r *rig) customer() (string, bool) {
	var c string
	err := r.pool.QueryRow(`SELECT stripe_customer_id FROM billing_customers WHERE user_id = $1`, r.uid).Scan(&c)
	return c, err == nil
}

// A subscription ended and an "active" update stamped with the SAME second (Stripe's created has one-second resolution), delivered after it:
// whichever Stripe sends last must not bring a ended subscription back. Both orders.
func TestAdversarialSameSecondEndedBeatsActive(t *testing.T) {
	now := time.Unix(1790500000, 0)
	for _, order := range []string{"ended then active", "active then ended"} {
		r := newRig(t, "adv-tie-"+strings.ReplaceAll(order, " ", "-"))
		const T = 1790600000
		ended := func() int { return r.deliver("subscription_deleted.json", with("evt_tie_ended", T)) }
		active := func() int { return r.deliver("subscription_created_active.json", with("evt_tie_active", T)) }
		var a, b func() int
		if order == "ended then active" {
			a, b = ended, active
		} else {
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

// An event for account A that names a Stripe customer which is already account B's must not poison the endpoint (a 500 Stripe retries for days)
// and must not touch B: B keeps its customer and its plan. (billing_customers.stripe_customer_id is UNIQUE.)
func TestAdversarialASubscriptionNamingAnotherAccountsCustomer(t *testing.T) {
	b := newRig(t, "adv-cust-b")
	if code := b.deliver("checkout_session_completed.json", nil); code != 200 {
		t.Fatalf("B's checkout: %d", code)
	}
	if code := b.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatalf("B's subscription: %d", code)
	}
	bCustomer, ok := b.customer()
	if !ok {
		t.Fatal("B has no customer after its checkout")
	}
	bBefore, _, _, bExt, _ := b.row()

	// A's event, with A's id in the metadata but B's customer.
	a := newRig(t, "adv-cust-a")
	cross := with("evt_cross_1", 1790000500,
		[2]string{`"customer": "cus_` + a.uid.String()[:8] + `"`, `"customer": "` + bCustomer + `"`},
		[2]string{`"id": "sub_` + a.uid.String()[:8] + `"`, `"id": "sub_cross_a"`})
	code := a.deliver("subscription_created_active.json", cross)
	if code == http.StatusInternalServerError {
		t.Errorf("an event naming another account's customer was answered 500: Stripe would retry it for days and the endpoint would be switched off")
	}
	if c, ok := b.customer(); !ok || c != bCustomer {
		t.Errorf("B's customer link changed: %q (had %q)", c, bCustomer)
	}
	if st, _, _, ext, ok := b.row(); !ok || st != bBefore || ext != bExt {
		t.Errorf("B's entitlement changed: %s/%s (had %s/%s)", st, ext, bBefore, bExt)
	}
	if c, ok := a.customer(); ok && c == bCustomer {
		t.Errorf("A was given B's customer %q", c)
	}
}

// A subscription priced in another currency, or for nothing, is Rivolt Pro all the same (the US-only guard reports and never refuses; the
// operator controls the Stripe prices): ACCEPTED BY DESIGN, pinned here so a change is deliberate. What the terms record is what the event said.
func TestAdversarialWrongCurrencyAndFreeSubscriptionsAreAcceptedByDesign(t *testing.T) {
	now := time.Unix(1790500000, 0)
	r := newRig(t, "adv-currency")
	eur := func(s string) string {
		s = strings.ReplaceAll(s, `"currency": "usd"`, `"currency": "eur"`)
		return s
	}
	if code := r.deliver("subscription_created_active.json", eur); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); !st.Pro {
		t.Errorf("a EUR subscription: %+v", st)
	}
	var cur sql.NullString
	if err := r.pool.QueryRow(`SELECT currency FROM billing_stripe_subs WHERE user_id = $1`, r.uid).Scan(&cur); err != nil || cur.String != "eur" {
		t.Errorf("recorded currency = %q (%v), want eur", cur.String, err)
	}
	r2 := newRig(t, "adv-free")
	free := func(s string) string { return strings.ReplaceAll(s, `"unit_amount": 3499`, `"unit_amount": 0`) }
	if code := r2.deliver("subscription_created_active.json", free); code != 200 {
		t.Fatal(code)
	}
	if st := r2.state(now); !st.Pro {
		t.Errorf("a zero-amount subscription: %+v", st)
	}
}

// A full refund for a customer that is not linked yet is answered 500 (the checkout event may not have arrived) and leaves no trace; once the
// customer is linked the same event, retried, ends Pro. A refund with no customer at all is acknowledged.
func TestAdversarialRefundForAnUnknownCustomer(t *testing.T) {
	now := time.Unix(1790500000, 0)
	r := newRig(t, "adv-refund")
	const id = "evt_adv_refund_1"
	refund := with(id, 1790700000)
	if code := r.deliver("charge_refunded_full.json", refund); code != http.StatusInternalServerError {
		t.Fatalf("a refund for an unlinked customer: %d, want 500 (retry)", code)
	}
	if r.claimed(id) {
		t.Fatal("the refused refund stayed claimed: its retry would be ignored")
	}
	if _, _, _, _, ok := r.row(); ok {
		t.Fatal("an unmapped refund created an entitlement")
	}
	// The account subscribes, the customer is linked, and Stripe's retry of the refund is applied.
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatal(code)
	}
	if code := r.deliver("checkout_session_completed.json", nil); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); !st.Pro {
		t.Fatalf("setup: not Pro: %+v", st)
	}
	if code := r.deliver("charge_refunded_full.json", refund); code != 200 {
		t.Fatalf("the retry: %d", code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("the retried refund did not end Pro: %+v", st)
	}
	// No customer on the charge: nothing to do, acknowledged.
	noCust := with("evt_adv_refund_nocust", 1790700001, [2]string{`"customer": "cus_` + r.uid.String()[:8] + `"`, `"customer": null`})
	if code := r.deliver("charge_refunded_full.json", noCust); code != 200 {
		t.Errorf("a refund with no customer: %d, want 200", code)
	}
}

// A cancel delivered twice (the same event id, or Stripe's duplicate under a new id) ends the subscription once and never moves it back; the
// replay is acknowledged and applies nothing; nobody is told "payment failed" for a cancel.
func TestAdversarialDoubleDeliveredCancel(t *testing.T) {
	now := time.Unix(1790500000, 0)
	r := newRig(t, "adv-cancel")
	var failed atomic.Int32
	r.h.OnBilling = func(kind string) {
		if kind == "payment_failed" {
			failed.Add(1)
		}
	}
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatal(code)
	}
	cancel := with("evt_adv_cancel", 1790800000)
	if code := r.deliver("subscription_deleted.json", cancel); code != 200 {
		t.Fatalf("the cancel: %d", code)
	}
	invalBefore := len(r.inval)
	if code := r.deliver("subscription_deleted.json", cancel); code != 200 {
		t.Fatalf("the cancel again, same id: %d", code)
	}
	if got := r.outcomes[len(r.outcomes)-1]; got != "customer.subscription.deleted:duplicate" {
		t.Errorf("the replayed cancel's outcome = %s, want duplicate", got)
	}
	if len(r.inval) != invalBefore {
		t.Error("a replayed cancel invalidated the plan cache again")
	}
	if code := r.deliver("subscription_deleted.json", with("evt_adv_cancel_again", 1790800000)); code != 200 {
		t.Fatalf("the cancel again, new id: %d", code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("a twice-delivered cancel left Pro on: %+v", st)
	}
	if status, _, _, _, ok := r.row(); !ok || status != entitlements.StatusEnded {
		t.Errorf("row status = %q (present %v), want ended", status, ok)
	}
	if n := failed.Load(); n != 0 {
		t.Errorf("a cancel reported payment_failed %d times", n)
	}
	// And an older "active" event arriving after both cannot undo it.
	if code := r.deliver("subscription_created_active.json", with("evt_adv_old_active", 1790700000)); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("an older active event undid the cancel: %+v", st)
	}
}

// The same delivery arriving on several replicas at once is applied exactly once: one applied, the rest duplicates, every one acknowledged, and
// a past-due event is reported once.
func TestAdversarialConcurrentDuplicateDeliveries(t *testing.T) {
	r := newRig(t, "adv-concurrent")
	var failed atomic.Int32
	r.h.OnBilling = func(kind string) {
		if kind == "payment_failed" {
			failed.Add(1)
		}
	}
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatal(code)
	}
	const n = 12
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i] = r.deliver("subscription_updated_past_due.json", with("evt_adv_pastdue_race", 1790900000))
		}(i)
	}
	close(start)
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Errorf("delivery %d answered %d", i, c)
		}
	}
	r.mu.Lock()
	applied, dup := 0, 0
	for _, o := range r.outcomes {
		switch o {
		case "customer.subscription.updated:applied":
			applied++
		case "customer.subscription.updated:duplicate":
			dup++
		}
	}
	r.mu.Unlock()
	if applied != 1 || dup != n-1 {
		t.Errorf("applied %d, duplicate %d; want 1 and %d", applied, dup, n-1)
	}
	if got := failed.Load(); got != 1 {
		t.Errorf("payment_failed reported %d times, want 1", got)
	}
	if status, _, _, _, _ := r.row(); status != entitlements.StatusPastDue {
		t.Errorf("status = %q, want past_due", status)
	}
}

// Reordered terminal events: an ended subscription stays ended against an older past-due, and a later re-subscription (newer) is Pro again.
func TestAdversarialReorderedTerminalEvents(t *testing.T) {
	now := time.Unix(1790500000, 0)
	r := newRig(t, "adv-reorder")
	if code := r.deliver("subscription_deleted.json", with("evt_adv_end", 1791000200)); code != 200 {
		t.Fatal(code)
	}
	if code := r.deliver("subscription_updated_past_due.json", with("evt_adv_old_pastdue", 1791000100)); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("an older past-due brought an ended subscription back: %+v", st)
	}
	if code := r.deliver("subscription_created_active.json", with("evt_adv_resub", 1791000300)); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); !st.Pro {
		t.Errorf("a newer subscription event did not restore Pro: %+v", st)
	}
}

// A delivery exactly at the size cap (padded, correctly signed) is processed; one byte over is refused with nothing recorded.
func TestAdversarialBodyAtAndOverTheCap(t *testing.T) {
	r := newRig(t, "adv-cap")
	pad := func(total int, id string) []byte {
		pre := `{"id":"` + id + `","type":"ping","created":1790000001,"data":{"object":{}},"pad":"`
		suf := `"}`
		return []byte(pre + strings.Repeat("a", total-len(pre)-len(suf)) + suf)
	}
	post := func(b []byte) int {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(b)))
		req.Header.Set("Stripe-Signature", Sign(b, testSecret, t0))
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(pad(MaxBodyBytes, "evt_adv_cap_at")); code != 200 {
		t.Errorf("a body exactly at the cap: %d, want 200", code)
	}
	if !r.claimed("evt_adv_cap_at") {
		t.Error("the at-cap event was acknowledged but not recorded")
	}
	if code := post(pad(MaxBodyBytes+1, "evt_adv_cap_over")); code != http.StatusBadRequest {
		t.Errorf("a body one byte over the cap: %d, want 400", code)
	}
	if r.claimed("evt_adv_cap_over") {
		t.Error("an oversized body was recorded")
	}
}

// A cancel that carries no period end anywhere (neither on the subscription nor its items) must still end the subscription: it is the one fact
// the event exists to say.
func TestAdversarialCancelWithoutAPeriodEnd(t *testing.T) {
	now := time.Unix(1790500000, 0)
	r := newRig(t, "adv-noperiod")
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatal(code)
	}
	noEnd := func(s string) string {
		return strings.ReplaceAll(s, `"current_period_end": 1821536000`, `"x_removed_period_end": 0`)
	}
	if code := r.deliver("subscription_deleted.json", func(s string) string { return with("evt_adv_noend", 1790950000)(noEnd(s)) }); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("a cancel without a period end left Pro on: %+v", st)
	}
}

// compile-time use of the context package for the helper file
var _ = context.Background
