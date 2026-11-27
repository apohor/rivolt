//go:build integration

package stripebill

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// refundStripe is the three calls the self-serve refund makes (charges, refunds, subscriptions), in Stripe's shapes.
type refundStripe struct {
	srv         *httptest.Server
	mu          sync.Mutex
	charges     []Payment
	refundPosts []string // Idempotency-Key of every POST /v1/refunds
	refundCode  int      // answer to POST /v1/refunds (0 = 200)
	refundError string   // Stripe error code in that answer (default: a plain refusal)
}

func newRefundStripe(t *testing.T) *refundStripe {
	f := &refundStripe{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/charges":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": f.charges})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/refunds":
			f.refundPosts = append(f.refundPosts, r.Header.Get("Idempotency-Key"))
			if f.refundCode != 0 {
				w.WriteHeader(f.refundCode)
				code := f.refundError
				if code == "" {
					code = "amount_too_large"
				}
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"` + code + `"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"re_TEST1","status":"succeeded"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/subscriptions":
			_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *refundStripe) set(charges ...Payment) {
	f.mu.Lock()
	f.charges = charges
	f.mu.Unlock()
}

func (f *refundStripe) posts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refundPosts...)
}

func paid(id string, created time.Time) Payment {
	return Payment{ID: id, Created: created.Unix(), Amount: 3499, Currency: "usd", Paid: true, Status: "succeeded"}
}

// refundRig is a Pro account with a Stripe customer and a live subscription, and a Service talking to the fake.
func refundRig(t *testing.T, name string) (*rig, *Service, *refundStripe) {
	t.Helper()
	r := newRig(t, name)
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatalf("subscription: %d", code)
	}
	if code := r.deliver("checkout_session_completed.json", nil); code != 200 {
		t.Fatalf("checkout: %d", code)
	}
	f := newRefundStripe(t)
	t.Cleanup(func() { _, _ = r.pool.Exec(`DELETE FROM billing_self_refunds WHERE user_id = $1`, r.uid) })
	svc := &Service{Client: &Client{BaseURL: f.srv.URL, SecretKey: testKey}, DB: r.pool, Now: func() time.Time { return t0 }}
	if cust, _ := svc.customerOf(t.Context(), r.uid); cust == "" {
		t.Fatal("the account has no Stripe customer")
	}
	return r, svc, f
}

// The 14-day window is inclusive of its last second and closed one second after.
func TestSelfRefundWindowEdge(t *testing.T) {
	r, svc, f := refundRig(t, "refund-edge")
	for _, c := range []struct {
		age    time.Duration
		want   bool
		reason string
	}{
		{0, true, ""},
		{RefundWindow - time.Second, true, ""},
		{RefundWindow, true, ""},
		{RefundWindow + time.Second, false, RefundWindowClosed},
		{30 * 24 * time.Hour, false, RefundWindowClosed},
	} {
		f.set(paid("ch_edge", t0.Add(-c.age)))
		o, err := svc.RefundOffer(t.Context(), r.uid)
		if err != nil {
			t.Fatal(err)
		}
		if o.Eligible != c.want || o.Reason != c.reason {
			t.Errorf("a payment %v old: eligible=%v reason=%q, want %v %q", c.age, o.Eligible, o.Reason, c.want, c.reason)
		}
	}
	// Nothing paid, an unpaid charge, an already (partly) refunded one: no offer.
	for name, p := range map[string]Payment{
		"unpaid":   {ID: "ch_x", Created: t0.Unix(), Amount: 3499, Currency: "usd", Paid: false, Status: "failed"},
		"refunded": {ID: "ch_x", Created: t0.Unix(), Amount: 3499, Currency: "usd", Paid: true, Status: "succeeded", Refunded: true, AmountRefunded: 3499},
		"partial":  {ID: "ch_x", Created: t0.Unix(), Amount: 3499, Currency: "usd", Paid: true, Status: "succeeded", AmountRefunded: 500},
		"disputed": {ID: "ch_x", Created: t0.Unix(), Amount: 3499, Currency: "usd", Paid: true, Status: "succeeded", Disputed: true},
	} {
		f.set(p)
		if o, _ := svc.RefundOffer(t.Context(), r.uid); o.Eligible || o.Reason != RefundNoPayment {
			t.Errorf("%s charge: %+v, want no_payment", name, o)
		}
	}
	// A window that has closed is refused by the refund itself, and Stripe is never asked.
	f.set(paid("ch_old", t0.Add(-RefundWindow-time.Hour)))
	if _, err := svc.SelfRefund(t.Context(), r.uid); !errors.Is(err, ErrRefundUnavailable) {
		t.Fatalf("a refund after the window: %v", err)
	}
	if n := len(f.posts()); n != 0 {
		t.Errorf("Stripe was asked to refund %d times after the window closed", n)
	}
	if !r.state(t0).Pro {
		t.Error("a refused refund ended Pro")
	}
}

// One tap refunds the payment in full and ends Pro; a second refund is refused and Stripe sees one call; the webhook that follows, and its
// replay, change nothing.
func TestSelfRefundOnceThenWebhookReplay(t *testing.T) {
	r, svc, f := refundRig(t, "refund-once")
	f.set(paid("ch_TEST001", t0.Add(-3*24*time.Hour)))
	if !r.state(t0).Pro {
		t.Fatal("the account is not Pro to start with")
	}
	var dropped []uuid.UUID
	svc.Invalidate = func(u uuid.UUID) { dropped = append(dropped, u) }
	got, err := svc.SelfRefund(t.Context(), r.uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != r.uid {
		t.Errorf("the plan cache was dropped for %v, want the account once (the next /me must say free)", dropped)
	}
	if got.AmountCents != 3499 || got.Currency != "usd" {
		t.Errorf("refunded %+v, want the whole 3499 usd payment", got)
	}
	if posts := f.posts(); len(posts) != 1 || posts[0] != refundKey(r.uid) {
		t.Fatalf("Stripe saw refunds %v, want one with the account's idempotency key", posts)
	}
	if r.state(t0).Pro {
		t.Error("Pro is still on after the refund")
	}
	var state, refundID string
	if err := r.pool.QueryRow(`SELECT state, COALESCE(refund_id, '') FROM billing_self_refunds WHERE user_id = $1`, r.uid).Scan(&state, &refundID); err != nil || state != "done" || refundID != "re_TEST1" {
		t.Errorf("the record: state=%q refund=%q err=%v", state, refundID, err)
	}

	// A second refund, even of a newer payment inside the window: refused, no second call.
	f.set(paid("ch_NEWER", t0.Add(-time.Hour)))
	if _, err := svc.SelfRefund(t.Context(), r.uid); !errors.Is(err, ErrRefundUnavailable) {
		t.Fatalf("a second refund: %v", err)
	}
	if o, _ := svc.RefundOffer(t.Context(), r.uid); o.Eligible || o.Reason != RefundUsed {
		t.Errorf("after the refund the offer is %+v, want used", o)
	}
	if n := len(f.posts()); n != 1 {
		t.Errorf("Stripe saw %d refund calls, want 1", n)
	}

	// Stripe's charge.refunded for it: applied without harm; the same delivery again is a duplicate; Pro stays off, nothing else changes.
	at := func(created string) func(string) string {
		return func(s string) string { return strings.ReplaceAll(s, `"created": 1790400000`, `"created": `+created) }
	}
	if code := r.deliver("charge_refunded_full.json", at("1790500001")); code != 200 {
		t.Fatalf("webhook: %d", code)
	}
	if code := r.deliver("charge_refunded_full.json", at("1790500001")); code != 200 {
		t.Fatalf("webhook replay: %d", code)
	}
	if got := r.outcomes[len(r.outcomes)-1]; got != "charge.refunded:duplicate" {
		t.Errorf("the replay's outcome = %s, want duplicate", got)
	}
	if r.state(t0).Pro {
		t.Error("the webhook brought Pro back")
	}
	var n int
	if err := r.pool.QueryRow(`SELECT count(*) FROM billing_self_refunds WHERE user_id = $1`, r.uid).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d refund rows, want 1 (err %v)", n, err)
	}
}

// A refund whose answer never came back keeps its reservation and a retry sends the same key; a refund Stripe refuses for good releases
// the account's one refund; two taps at once refund once.
func TestSelfRefundFailuresAndRetries(t *testing.T) {
	r, svc, f := refundRig(t, "refund-retry")
	f.set(paid("ch_TEST001", t0.Add(-time.Hour)))

	f.refundCode = http.StatusInternalServerError
	if _, err := svc.SelfRefund(t.Context(), r.uid); err == nil || errors.Is(err, ErrRefundUnavailable) {
		t.Fatalf("a Stripe 500: %v", err)
	}
	if !r.state(t0).Pro {
		t.Error("Pro ended though Stripe did not refund")
	}
	f.refundCode = 0
	if _, err := svc.SelfRefund(t.Context(), r.uid); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if posts := f.posts(); len(posts) != 2 || posts[0] != posts[1] {
		t.Errorf("the retry used another idempotency key: %v", posts)
	}

	// A refusal for good frees the reservation.
	r2, svc2, f2 := refundRig(t, "refund-refused")
	f2.set(paid("ch_TEST001", t0.Add(-time.Hour)))
	f2.refundCode = http.StatusBadRequest
	if _, err := svc2.SelfRefund(t.Context(), r2.uid); !errors.Is(err, ErrRefundUnavailable) {
		t.Fatalf("a refusal: %v", err)
	}
	var n int
	if err := r2.pool.QueryRow(`SELECT count(*) FROM billing_self_refunds WHERE user_id = $1`, r2.uid).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused refund left %d rows (err %v)", n, err)
	}
	if !r2.state(t0).Pro {
		t.Error("Pro ended though Stripe refused")
	}

	// Two taps at once: one refund, both answers are success or "used", never a second Stripe refund of different key.
	r3, svc3, f3 := refundRig(t, "refund-double")
	f3.set(paid("ch_TEST001", t0.Add(-time.Hour)))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = svc3.SelfRefund(t.Context(), r3.uid) }()
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil && !errors.Is(e, ErrRefundUnavailable) {
			t.Errorf("tap %d: %v", i, e)
		}
	}
	for _, k := range f3.posts() {
		if k != refundKey(r3.uid) {
			t.Errorf("a refund call carried key %q", k)
		}
	}
	if r3.state(t0).Pro {
		t.Error("Pro is still on after the double tap")
	}
}

// Stripe says the charge is already refunded (the first answer was lost and its key expired, or a Dashboard refund): that spends the account's
// refund, Pro ends, and the offer does not come back. A charge.refunded for a still-pending reservation settles it the same way.
func TestSelfRefundAlreadyRefundedSpendsIt(t *testing.T) {
	r, svc, f := refundRig(t, "refund-already")
	f.set(paid("ch_TEST001", t0.Add(-time.Hour)))
	f.refundCode, f.refundError = http.StatusBadRequest, "charge_already_refunded"
	if _, err := svc.SelfRefund(t.Context(), r.uid); !errors.Is(err, ErrRefundUnavailable) {
		t.Fatalf("already refunded: %v", err)
	}
	var state string
	if err := r.pool.QueryRow(`SELECT state FROM billing_self_refunds WHERE user_id = $1`, r.uid).Scan(&state); err != nil || state != "done" {
		t.Errorf("the record: state=%q err=%v, want done", state, err)
	}
	if r.state(t0).Pro {
		t.Error("Pro is still on though the charge is refunded")
	}
	f.set(paid("ch_NEWER", t0.Add(-time.Minute)))
	if o, _ := svc.RefundOffer(t.Context(), r.uid); o.Eligible || o.Reason != RefundUsed {
		t.Errorf("the offer is %+v, want used", o)
	}

	// A lost answer leaves a pending reservation; the webhook for that charge settles it.
	r2, svc2, f2 := refundRig(t, "refund-webhook-settles")
	f2.set(paid("ch_TEST001", t0.Add(-time.Hour)))
	f2.refundCode = http.StatusInternalServerError
	_, _ = svc2.SelfRefund(t.Context(), r2.uid)
	if code := r2.deliver("charge_refunded_full.json", nil); code != 200 {
		t.Fatalf("webhook: %d", code)
	}
	if err := r2.pool.QueryRow(`SELECT state FROM billing_self_refunds WHERE user_id = $1`, r2.uid).Scan(&state); err != nil || state != "done" {
		t.Errorf("after the webhook: state=%q err=%v, want done", state, err)
	}
}
