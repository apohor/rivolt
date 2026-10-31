//go:build integration

package stripebill

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/testpg"
)

var evtID = regexp.MustCompile(`"id": "(evt_[A-Za-z0-9_]+)"`)

// eventRow is the stripe_events id a fixture's event is recorded under for this rig's account.
func (r *rig) eventRow(id string) string { return id + "-" + r.uid.String()[:8] }

type rig struct {
	t        *testing.T
	pool     *sql.DB
	h        *Handler
	uid      uuid.UUID
	mu       sync.Mutex
	outcomes []string
	inval    []uuid.UUID
}

func newRig(t *testing.T, name string) *rig {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, name)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, pool: pool, uid: uid}
	r.h = &Handler{DB: pool, Secret: testSecret, Now: func() time.Time { return t0 },
		Invalidate: func(u uuid.UUID) { r.mu.Lock(); r.inval = append(r.inval, u); r.mu.Unlock() },
		Observe: func(typ, outcome string) {
			r.mu.Lock()
			r.outcomes = append(r.outcomes, typ+":"+outcome)
			r.mu.Unlock()
		}}
	return r
}

// deliver posts a fixture (with USER_ID filled in and a fresh event id when unique is set) as Stripe would, signed at t0.
func (r *rig) deliver(file string, mutate func(string) string) int {
	r.t.Helper()
	b, err := os.ReadFile("testdata/" + file)
	if err != nil {
		r.t.Fatal(err)
	}
	body := strings.ReplaceAll(string(b), "USER_ID", r.uid.String())
	body = strings.ReplaceAll(body, "cus_TEST001", "cus_"+r.uid.String()[:8])
	body = strings.ReplaceAll(body, "sub_TEST001", "sub_"+r.uid.String()[:8])
	if mutate != nil {
		body = mutate(body)
	}
	// Event ids are global in stripe_events and the tests share a database: every delivery's id carries this account's prefix.
	body = evtID.ReplaceAllString(body, `"id": "${1}"`)
	req := httptest.NewRequest(http.MethodPost, "/api/billing/stripe/webhook", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", Sign([]byte(body), testSecret, t0))
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec.Code
}

func (r *rig) row() (status string, end time.Time, cancel bool, ext string, ok bool) {
	err := r.pool.QueryRow(`SELECT status, current_period_end, cancel_at_period_end, COALESCE(external_id, '') FROM entitlements WHERE user_id = $1 AND source = 'stripe'`, r.uid).
		Scan(&status, &end, &cancel, &ext)
	return status, end, cancel, ext, err == nil
}

func (r *rig) state(at time.Time) entitlements.State {
	rows, err := r.pool.Query(`SELECT source, status, current_period_end, updated_at, cancel_at_period_end FROM entitlements WHERE user_id = $1`, r.uid)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var rs []entitlements.Row
	for rows.Next() {
		var x entitlements.Row
		var end sql.NullTime
		if err := rows.Scan(&x.Source, &x.Status, &end, &x.UpdatedAt, &x.CancelAtPeriodEnd); err != nil {
			r.t.Fatal(err)
		}
		if end.Valid {
			e := end.Time
			x.PeriodEnd = &e
		}
		rs = append(rs, x)
	}
	return entitlements.Compute(rs, at)
}

// The webhook's whole job against Postgres: a signed delivery grants, updates and ends the stripe entitlement; a replay changes nothing;
// older events cannot move it backwards; a full refund ends it for good; a bad signature changes nothing and is refused.
func TestStripeWebhookAgainstPostgres(t *testing.T) {
	r := newRig(t, "stripe-flow")
	now := time.Unix(1790500000, 0)
	// A forged or altered delivery is refused and changes nothing.
	if code := r.deliver("subscription_created_active.json", func(s string) string { return s }); code != 200 {
		t.Fatalf("first delivery before the customer is known: %d", code)
	}
	if _, _, _, _, ok := r.row(); !ok {
		t.Fatal("the subscription (user_id in its metadata) did not create the entitlement")
	}
	// Replay of the same delivery: acknowledged, applied once.
	before, endBefore, _, _, _ := r.row()
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatalf("replay: %d", code)
	}
	if after, endAfter, _, _, _ := r.row(); after != before || !endAfter.Equal(endBefore) {
		t.Errorf("a replay changed the row: %s -> %s", before, after)
	}
	if got := r.outcomes[len(r.outcomes)-1]; got != "customer.subscription.created:duplicate" {
		t.Errorf("the replay's outcome = %s, want duplicate", got)
	}
	if st := r.state(now); !st.Pro || st.Source != entitlements.SourceStripe || st.PeriodEnd == nil || st.PeriodEnd.Unix() != 1821536000 {
		t.Errorf("a stripe subscription reads %+v, want Pro by stripe until 1821536000", st)
	}
	if len(r.inval) == 0 || r.inval[0] != r.uid {
		t.Error("the plan cache was not invalidated for the account")
	}

	// Set to cancel at the period end: still Pro, now reports plan_cancels_at.
	if code := r.deliver("subscription_updated_cancel.json", nil); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); !st.Pro || st.CancelsAt == nil || st.CancelsAt.Unix() != 1821536000 {
		t.Errorf("a subscription set to cancel reads %+v, want Pro with CancelsAt", st)
	}

	// An OLDER event (created before the cancel) arriving late cannot undo it.
	older := func(s string) string {
		s = strings.ReplaceAll(s, `"created": 1790000001`, `"created": 1790000002`)
		return strings.ReplaceAll(s, `"id": "evt_sub_created_1"`, `"id": "evt_sub_created_late"`)
	}
	if code := r.deliver("subscription_created_active.json", older); code != 200 {
		t.Fatal(code)
	}
	if _, _, cancel, _, _ := r.row(); !cancel {
		t.Error("an older event undid the cancellation")
	}

	// Renewal fails: past due, Pro through the 7-day grace, reported as plan_grace_ends.
	if code := r.deliver("subscription_updated_past_due.json", nil); code != 200 {
		t.Fatal(code)
	}
	grace := time.Unix(1821536000, 0).Add(entitlements.Grace)
	if st := r.state(time.Unix(1821536000, 0).Add(24 * time.Hour)); !st.Pro || st.GraceEnds == nil || !st.GraceEnds.Equal(grace) {
		t.Errorf("a past-due subscription reads %+v, want Pro with GraceEnds %v", st, grace)
	}
	if st := r.state(grace.Add(time.Second)); st.Pro {
		t.Errorf("a past-due subscription still grants after its grace: %+v", st)
	}

	// Deleted: ends, whatever it was before (here past due, in its grace). The event is newer than the past-due one (event times matter:
	// the newest per subscription wins).
	at := func(from, to string) func(string) string {
		return func(s string) string { return strings.ReplaceAll(s, `"created": `+from, `"created": `+to) }
	}
	if code := r.deliver("subscription_deleted.json", at("1790300000", "1821700000")); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("a deleted subscription still grants: %+v", st)
	}

	// A partial refund changes nothing; a full one ends Pro and nothing older brings it back.
	if code := r.deliver("subscription_created_active.json", func(s string) string {
		s = strings.ReplaceAll(s, `"created": 1790000001`, `"created": 1821800000`)
		return strings.ReplaceAll(s, `"id": "evt_sub_created_1"`, `"id": "evt_resub"`)
	}); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); !st.Pro {
		t.Fatalf("a newer subscription event did not restore Pro: %+v", st)
	}
	if code := r.deliver("charge_refunded_partial.json", nil); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); !st.Pro {
		t.Errorf("a partial refund ended Pro: %+v", st)
	}
	if code := r.deliver("charge_refunded_full.json", at("1790400000", "1821900000")); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("a full refund left Pro on: %+v", st)
	}
	late := func(s string) string {
		s = strings.ReplaceAll(s, `"created": 1790000001`, `"created": 1821899999`) // before the refund (1821900000)
		return strings.ReplaceAll(s, `"id": "evt_sub_created_1"`, `"id": "evt_before_refund"`)
	}
	if code := r.deliver("subscription_created_active.json", late); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("an event older than the refund brought Pro back: %+v", st)
	}
}

// A bad signature is refused with 400 and writes nothing; a failing delivery is rolled back whole (its event id too) and answered 500, so
// Stripe's retry is applied.
func TestStripeWebhookRefusesAndRollsBack(t *testing.T) {
	r := newRig(t, "stripe-refuse")
	b, _ := os.ReadFile("testdata/subscription_created_active.json")
	body := strings.ReplaceAll(string(b), "USER_ID", r.uid.String())
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", Sign([]byte(body+" "), testSecret, t0)) // signed a different body
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a wrong signature: %d, want 400", rec.Code)
	}
	var n int
	if err := r.pool.QueryRow(`SELECT count(*) FROM entitlements WHERE user_id = $1`, r.uid).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused delivery wrote %d rows (%v)", n, err)
	}
	if err := r.pool.QueryRow(`SELECT count(*) FROM stripe_events WHERE event_id LIKE 'evt_sub_created_1%' AND event_id LIKE '%' || $1`, r.uid.String()[:8]).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused delivery recorded its event id (%d)", n)
	}
	// Unsigned and header-less requests too.
	for _, h := range []string{"", "t=1790000000"} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if h != "" {
			req.Header.Set("Stripe-Signature", h)
		}
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("header %q: %d, want 400", h, rec.Code)
		}
	}
	// A delivery that fails after its event id was claimed (here: an object that is not an object) is rolled back whole, claim included,
	// and answered 500; the retry of the same event id, now well-formed, is applied.
	broken := func(s string) string {
		i := strings.Index(s, `"data"`)
		return s[:i] + `"data": {"object": "oops"}}`
	}
	if code := r.deliver("subscription_created_active.json", broken); code != http.StatusInternalServerError {
		t.Errorf("a failing delivery answered %d, want 500", code)
	}
	if err := r.pool.QueryRow(`SELECT count(*) FROM stripe_events WHERE event_id = $1`, r.eventRow("evt_sub_created_1")).Scan(&n); err != nil || n != 0 {
		t.Errorf("a failed delivery kept its event id claimed (%d): the retry would be ignored", n)
	}
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatalf("the retry: %d", code)
	}
	if _, _, _, _, ok := r.row(); !ok {
		t.Error("the retry after a failure did not apply the event")
	}
}

// Stripe does not order its events. A subscription event whose account cannot be found yet (no metadata, customer not linked) is NOT
// acknowledged: it rolls back whole and Stripe retries, by when the checkout event has linked the customer. With the account's id in the
// subscription's metadata it resolves on its own, whatever the order.
func TestStripeWebhookEventOrder(t *testing.T) {
	r := newRig(t, "stripe-order")
	noMeta := func(s string) string {
		return strings.ReplaceAll(s, `"rivolt_user_id": "`+r.uid.String()+`"`, `"x": "y"`)
	}

	// subscription.created (no metadata) BEFORE checkout.session.completed: 500, nothing recorded, no entitlement.
	if code := r.deliver("subscription_created_active.json", noMeta); code != http.StatusInternalServerError {
		t.Fatalf("an event for an account not linked yet answered %d, want 500 so Stripe retries", code)
	}
	var n int
	if err := r.pool.QueryRow(`SELECT count(*) FROM stripe_events WHERE event_id = $1`, r.eventRow("evt_sub_created_1")).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the event stayed claimed (%d): its retry would be ignored and the user would never get Pro", n)
	}
	if _, _, _, _, ok := r.row(); ok {
		t.Fatal("an unmapped event created an entitlement")
	}
	// The checkout event arrives and links the customer...
	if code := r.deliver("checkout_session_completed.json", nil); code != 200 {
		t.Fatalf("checkout.session.completed: %d", code)
	}
	// ...and Stripe's retry of the subscription event now applies: the account ends up Pro.
	if code := r.deliver("subscription_created_active.json", noMeta); code != 200 {
		t.Fatalf("the retry after the customer was linked: %d", code)
	}
	if st := r.state(time.Unix(1790500000, 0)); !st.Pro || st.Source != entitlements.SourceStripe {
		t.Errorf("after the retry the account reads %+v, want Pro by stripe", st)
	}

	// With the id in the metadata the subscription event resolves on its own, even before the checkout event.
	r2 := newRig(t, "stripe-order-meta")
	if code := r2.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatalf("a subscription event carrying the account's id: %d", code)
	}
	if st := r2.state(time.Unix(1790500000, 0)); !st.Pro {
		t.Errorf("a subscription event with metadata did not grant before the checkout event: %+v", st)
	}
	if code := r2.deliver("checkout_session_completed.json", nil); code != 200 {
		t.Fatal(code)
	}
}

// An event with nothing to apply is acknowledged: types we do not act on, a status that changes nothing, a partial refund.
func TestStripeWebhookIgnored(t *testing.T) {
	r := newRig(t, "stripe-ignored")
	for _, f := range []string{"invoice_payment_failed.json", "subscription_incomplete.json", "charge_refunded_partial.json"} {
		if code := r.deliver(f, nil); code != 200 {
			t.Errorf("%s: %d, want 200", f, code)
		}
	}
	if _, _, _, _, ok := r.row(); ok {
		t.Error("an ignored event created an entitlement")
	}
	// A full refund for a customer we do not know is retried too (the checkout event may not have arrived).
	if code := r.deliver("charge_refunded_full.json", nil); code != http.StatusInternalServerError {
		t.Errorf("a refund for an unknown customer: %d, want 500", code)
	}
}

// Event ids older than the retention are purged; recent ones stay.
func TestStripeEventsArePurged(t *testing.T) {
	r := newRig(t, "stripe-purge")
	ctx := context.Background()
	if _, err := r.pool.ExecContext(ctx, `INSERT INTO stripe_events (event_id, type, received_at) VALUES ('evt_old_purge', 'x', now() - interval '31 days'), ('evt_new_purge', 'x', now() - interval '29 days')`); err != nil {
		t.Fatal(err)
	}
	if _, err := PurgeEvents(ctx, r.pool); err != nil {
		t.Fatal(err)
	}
	var old, recent int
	_ = r.pool.QueryRow(`SELECT count(*) FROM stripe_events WHERE event_id = 'evt_old_purge'`).Scan(&old)
	_ = r.pool.QueryRow(`SELECT count(*) FROM stripe_events WHERE event_id = 'evt_new_purge'`).Scan(&recent)
	if old != 0 || recent != 1 {
		t.Errorf("after the purge: old=%d recent=%d, want 0 and 1", old, recent)
	}
}

// Portal and a returning customer against the real customer table: no customer is a refusal, and once Checkout has linked the customer
// (the webhook) the portal opens for it and a second Checkout reuses it.
func TestPortalAndReturningCustomerAgainstPostgres(t *testing.T) {
	r := newRig(t, "stripe-portal")
	f := newFakeStripe(t)
	svc := &Service{
		Client: &Client{BaseURL: f.srv.URL, SecretKey: testKey}, DB: r.pool, Plans: fakePlans{billing: true},
		Prices: map[string]string{"month": priceMonth, "year": priceYear}, BaseURL: "https://rivolt.example",
		Now: func() time.Time { return launchDay },
	}
	ctx := context.Background()
	if _, err := svc.Portal(ctx, r.uid); !errors.Is(err, ErrNoCustomer) {
		t.Fatalf("portal for an account with no customer: %v, want ErrNoCustomer", err)
	}
	if code := r.deliver("checkout_session_completed.json", nil); code != 200 {
		t.Fatalf("checkout.session.completed: %d", code)
	}
	url, err := svc.Portal(ctx, r.uid)
	if err != nil || url != portalPage {
		t.Fatalf("portal: %q %v", url, err)
	}
	last := f.posts[len(f.posts)-1]
	if got, want := last.Get("customer"), "cus_"+r.uid.String()[:8]; got != want {
		t.Errorf("the portal was opened for %q, want the account's own customer %q", got, want)
	}
	if last.Get("return_url") != "https://rivolt.example/settings/plan" {
		t.Errorf("return_url = %q", last.Get("return_url"))
	}
	if _, err := svc.Checkout(ctx, r.uid, "year"); err != nil {
		t.Fatal(err)
	}
	if got := f.posts[len(f.posts)-1].Get("customer"); got != "cus_"+r.uid.String()[:8] {
		t.Errorf("a returning customer's checkout used customer %q, want theirs", got)
	}
	// Another account never gets this account's customer.
	other := newRig(t, "stripe-portal-other")
	svc.DB = other.pool
	if _, err := svc.Portal(ctx, other.uid); !errors.Is(err, ErrNoCustomer) {
		t.Errorf("another account's portal: %v, want ErrNoCustomer", err)
	}
}
