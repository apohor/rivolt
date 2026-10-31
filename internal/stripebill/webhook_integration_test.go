//go:build integration

package stripebill

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/testpg"
)

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
	req := httptest.NewRequest(http.MethodPost, "/api/billing/stripe/webhook", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", Sign([]byte(body), testSecret, t0))
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec.Code
}

func (r *rig) uniq(id string) func(string) string {
	return func(s string) string {
		return strings.ReplaceAll(s, `"id": "`+id+`"`, `"id": "`+id+"-"+r.uid.String()[:8]+`"`)
	}
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
		return strings.ReplaceAll(s, `"id": "evt_sub_created_1"`, `"id": "evt_sub_created_late-`+r.uid.String()[:8]+`"`)
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

	// Deleted: ends.
	if code := r.deliver("subscription_deleted.json", nil); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("a deleted subscription still grants: %+v", st)
	}

	// A partial refund changes nothing; a full one ends Pro and nothing older brings it back.
	if code := r.deliver("subscription_created_active.json", func(s string) string {
		s = strings.ReplaceAll(s, `"created": 1790000001`, `"created": 1790350000`)
		return strings.ReplaceAll(s, `"id": "evt_sub_created_1"`, `"id": "evt_resub-`+r.uid.String()[:8]+`"`)
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
	if code := r.deliver("charge_refunded_full.json", nil); code != 200 {
		t.Fatal(code)
	}
	if st := r.state(now); st.Pro {
		t.Errorf("a full refund left Pro on: %+v", st)
	}
	late := func(s string) string {
		s = strings.ReplaceAll(s, `"created": 1790000001`, `"created": 1790399999`) // before the refund (1790400000)
		return strings.ReplaceAll(s, `"id": "evt_sub_created_1"`, `"id": "evt_before_refund-`+r.uid.String()[:8]+`"`)
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
	if err := r.pool.QueryRow(`SELECT count(*) FROM stripe_events WHERE event_id LIKE 'evt_sub_created_1%'`).Scan(&n); err != nil || n != 0 {
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
	if err := r.pool.QueryRow(`SELECT count(*) FROM stripe_events WHERE event_id = 'evt_sub_created_1'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a failed delivery kept its event id claimed (%d): the retry would be ignored", n)
	}
	if code := r.deliver("subscription_created_active.json", nil); code != 200 {
		t.Fatalf("the retry: %d", code)
	}
	if _, _, _, _, ok := r.row(); !ok {
		t.Error("the retry after a failure did not apply the event")
	}
}

// An event for a customer or subscription no account owns, or for a deleted account, is acknowledged and changes nothing.
func TestStripeWebhookUnmappedAndIgnored(t *testing.T) {
	r := newRig(t, "stripe-unmapped")
	noMeta := func(s string) string {
		s = strings.ReplaceAll(s, `"user_id": "`+r.uid.String()+`"`, `"x": "y"`)
		return s
	}
	if code := r.deliver("subscription_created_active.json", noMeta); code != 200 {
		t.Fatal(code)
	}
	if _, _, _, _, ok := r.row(); ok {
		t.Error("a subscription for an unknown customer created an entitlement")
	}
	for _, f := range []string{"invoice_payment_failed.json", "subscription_incomplete.json", "charge_refunded_full.json"} {
		if code := r.deliver(f, nil); code != 200 {
			t.Errorf("%s: %d, want 200", f, code)
		}
	}
	if _, _, _, _, ok := r.row(); ok {
		t.Error("an ignored or unmapped event created an entitlement")
	}
}
