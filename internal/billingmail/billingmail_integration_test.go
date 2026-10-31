//go:build integration

package billingmail

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/launchnotice"
	"github.com/apohor/rivolt/internal/stripebill"
	"github.com/apohor/rivolt/internal/testpg"
)

type recMailer struct {
	mu   sync.Mutex
	sent []email.Message
	fail bool
}

func (m *recMailer) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("resend down")
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *recMailer) to(addr string) []email.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []email.Message
	for _, s := range m.sent {
		if s.To == addr {
			out = append(out, s)
		}
	}
	return out
}

const whsec = "whsec_billingmail_test"

type rig struct {
	t    *testing.T
	pool *sql.DB
	uid  uuid.UUID
	addr string
	mail *recMailer
	job  *Job
	wh   *stripebill.Handler
}

func newRig(t *testing.T, name string, verified bool) *rig {
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
	addr := name + "@billing.example.test"
	if _, err := pool.ExecContext(ctx, `UPDATE users SET email = $2, email_verified = $3 WHERE id = $1`, uid, addr, verified); err != nil {
		t.Fatal(err)
	}
	m := &recMailer{}
	r := &rig{t: t, pool: pool, uid: uid, addr: addr, mail: m}
	r.job = &Job{DB: pool, Mailer: m, Now: func() time.Time { return time.Unix(1790500000, 0) }}
	r.wh = &stripebill.Handler{DB: pool, Secret: whsec, Now: func() time.Time { return time.Unix(1790000500, 0) }}
	return r
}

// deliver posts a stripebill fixture for this account (the webhook writes the state the job reads).
func (r *rig) deliver(file string, mutate func(string) string) {
	r.t.Helper()
	b, err := os.ReadFile("../stripebill/testdata/" + file)
	if err != nil {
		r.t.Fatal(err)
	}
	body := strings.ReplaceAll(string(b), "USER_ID", r.uid.String())
	body = strings.ReplaceAll(body, "cus_TEST001", "cus_"+r.uid.String()[:8])
	body = strings.ReplaceAll(body, "sub_TEST001", "sub_"+r.uid.String()[:8])
	if mutate != nil {
		body = mutate(body)
	}
	body = strings.ReplaceAll(body, `"id": "evt_`, `"id": "evt_`+r.uid.String()[:8]+`_`)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", stripebill.Sign([]byte(body), whsec, time.Unix(1790000500, 0)))
	rec := httptest.NewRecorder()
	r.wh.ServeHTTP(rec, req)
	if rec.Code != 200 {
		r.t.Fatalf("webhook %s: %d", file, rec.Code)
	}
}

// From a real webhook delivery to the inbox: the confirmation states what the subscription costs and when it renews (read from the event's
// price), goes to the verified address once, is retried after a failed send, and is never repeated.
func TestPurchaseConfirmationFromTheWebhookState(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, "bm-purchase", true)
	r.deliver("subscription_created_active.json", nil)

	// A failing mailer releases the claim: nothing is lost, the next run sends.
	r.mail.fail = true
	if st, err := r.job.RunOnce(ctx); err != nil || st.Failed != 1 || st.Sent != 0 {
		t.Fatalf("a failing mailer: %+v %v", st, err)
	}
	r.mail.fail = false
	st, err := r.job.RunOnce(ctx)
	if err != nil || st.Sent != 1 {
		t.Fatalf("the retry: %+v %v", st, err)
	}
	got := r.mail.to(r.addr)
	if len(got) != 1 {
		t.Fatalf("%d emails to the account, want 1", len(got))
	}
	for _, want := range []string{"$34.99 a year", "renews automatically every year until you cancel", "The next charge is on September 21, 2027", "https://rivolt.dev/settings#plan"} {
		if !strings.Contains(got[0].Text, want) {
			t.Errorf("the confirmation lacks %q:\n%s", want, got[0].Text)
		}
	}
	if got[0].IdempotencyKey == "" || !strings.Contains(got[0].IdempotencyKey, r.uid.String()) {
		t.Errorf("IdempotencyKey = %q", got[0].IdempotencyKey)
	}
	// Never twice: a rerun, a webhook update of the same subscription, a second replica.
	r.deliver("subscription_updated_cancel.json", nil)
	for i := 0; i < 3; i++ {
		if _, err := r.job.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(r.mail.to(r.addr)); n != 1 {
		t.Errorf("%d emails after reruns and a later subscription update, want exactly 1", n)
	}
	// The in-app side is not used: the notice row is created already expired.
	var shown int
	if err := r.pool.QueryRow(`SELECT count(*) FROM account_notices WHERE user_id = $1 AND kind LIKE 'billing_%' AND shows_until > $2`, r.uid, time.Unix(1790500000, 0)).Scan(&shown); err != nil || shown != 0 {
		t.Errorf("a billing email left %d in-app banners (%v)", shown, err)
	}
}

// A trialing subscription says the first charge is on the trial's end. A subscription long untouched is not announced as new. An
// unverified address, a disabled account and the demo get nothing.
func TestPurchaseConfirmationRules(t *testing.T) {
	ctx := context.Background()
	trial := newRig(t, "bm-trial", true)
	trial.deliver("subscription_created_trialing.json", nil)
	if st, err := trial.job.RunOnce(ctx); err != nil || st.Sent != 1 {
		t.Fatalf("trialing: %+v %v", st, err)
	}
	if body := trial.mail.to(trial.addr)[0].Text; !strings.Contains(body, "free period is running. Your first charge of $34.99 is on October 5, 2026") {
		t.Errorf("trialing email:\n%s", body)
	}

	old := newRig(t, "bm-old", true)
	old.deliver("subscription_created_active.json", nil)
	if _, err := old.pool.Exec(`UPDATE billing_stripe_subs SET updated_at = now() - interval '30 days' WHERE user_id = $1`, old.uid); err != nil {
		t.Fatal(err)
	}
	old.job.Now = func() time.Time { return time.Now() }
	if st, _ := old.job.RunOnce(ctx); old.mail.to(old.addr) != nil || st.Sent != 0 {
		t.Errorf("a subscription untouched for 30 days was announced as new: %+v", st)
	}

	unverified := newRig(t, "bm-unverified", false)
	unverified.deliver("subscription_created_active.json", nil)
	if _, err := unverified.job.RunOnce(ctx); err != nil || unverified.mail.to(unverified.addr) != nil {
		t.Errorf("an unverified address got a billing email (%v)", err)
	}

	skipped := newRig(t, "bm-skipped", true)
	skipped.deliver("subscription_created_active.json", nil)
	skipped.job.Skip = map[uuid.UUID]bool{skipped.uid: true}
	if _, err := skipped.job.RunOnce(ctx); err != nil || skipped.mail.to(skipped.addr) != nil {
		t.Errorf("a skipped account got a billing email (%v)", err)
	}
}

// A failed renewal during its grace: one notice per failed period saying when the grace ends; none once the grace is over.
func TestPaymentFailedNoticeInTheGrace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, "bm-failed", true)
	r.deliver("subscription_created_active.json", nil)
	r.deliver("subscription_updated_past_due.json", nil) // period ended 1821536000 (2027-09-21), unpaid
	// Inside the grace.
	r.job.Now = func() time.Time { return time.Unix(1821536000, 0).Add(24 * time.Hour) }
	if _, err := r.job.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var failed []email.Message
	for _, m := range r.mail.to(r.addr) {
		if strings.Contains(m.Subject, "payment didn't go through") {
			failed = append(failed, m)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("%d payment-failed emails, want 1", len(failed))
	}
	if !strings.Contains(failed[0].Text, "stays on until September 28, 2027") || !strings.Contains(failed[0].Text, "https://rivolt.dev/settings#plan") {
		t.Errorf("payment-failed email:\n%s", failed[0].Text)
	}
	for i := 0; i < 2; i++ {
		_, _ = r.job.RunOnce(ctx)
	}
	n := 0
	for _, m := range r.mail.to(r.addr) {
		if strings.Contains(m.Subject, "payment didn't go through") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d payment-failed emails after reruns, want exactly 1", n)
	}

	// After the grace: nothing (the account is already on the free plan).
	late := newRig(t, "bm-failed-late", true)
	late.deliver("subscription_created_active.json", nil)
	late.deliver("subscription_updated_past_due.json", nil)
	late.job.Now = func() time.Time { return time.Unix(1821536000, 0).Add(10 * 24 * time.Hour) }
	for _, m := range func() []email.Message { _, _ = late.job.RunOnce(ctx); return late.mail.to(late.addr) }() {
		if strings.Contains(m.Subject, "payment didn't go through") {
			t.Errorf("a payment-failed notice after the grace ended: %s", m.Subject)
		}
	}
}

// The yearly-renewal reminder's source: annual, active, not set to cancel, renewing inside the window, with the price the subscription has.
func TestRenewalSource(t *testing.T) {
	ctx := context.Background()
	annual := newRig(t, "bm-renew-annual", true)
	annual.deliver("subscription_created_active.json", nil)
	cancelling := newRig(t, "bm-renew-cancel", true)
	cancelling.deliver("subscription_created_active.json", nil)
	cancelling.deliver("subscription_updated_cancel.json", nil)
	src := RenewalSource{DB: annual.pool}
	// The window is [now, now+45 days]; the renewal is 40 days after "now" here, so it is inside it (the first version of this test ended the
	// window 10 days BEFORE the renewal).
	renewal := time.Unix(1821536000, 0)
	from, to := renewal.Add(-40*24*time.Hour), renewal.Add(5*24*time.Hour)
	rs, err := src.AnnualRenewals(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	var mine, cancels bool
	for _, r := range rs {
		switch r.UserID {
		case annual.uid:
			mine = true
			if r.PriceLabel != "$34.99 a year" || !r.Verified || r.Email != annual.addr || r.RenewsOn.Unix() != 1821536000 {
				t.Errorf("renewal = %+v", r)
			}
		case cancelling.uid:
			cancels = true
		}
	}
	if !mine {
		t.Error("an active annual subscription renewing in the window is not listed")
	}
	if cancels {
		t.Error("a subscription set to cancel (it will not renew) is listed for a renewal reminder")
	}
	if rs, _ := src.AnnualRenewals(ctx, renewal.Add(-100*24*time.Hour), renewal.Add(-50*24*time.Hour)); func() bool {
		for _, r := range rs {
			if r.UserID == annual.uid {
				return true
			}
		}
		return false
	}() {
		t.Error("a subscription outside the window is listed")
	}
}

// A real Stripe subscription id (about 28 characters, MIXED case) goes through the real notice INSERT (account_notices' kind must match
// ^[a-z0-9_]{1,40}$), is confirmed once, and the billing rows never appear in the in-app notice list (they are email-only).
func TestRealStripeSubscriptionIdsAndTheNoticeList(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, "bm-realid", true)
	realID := "sub_1MowQVLk" + strings.ToUpper(r.uid.String()[:4]) + "dIwHu7ix" + r.uid.String()[:4] + "eRlqHVzs"
	if len(realID) < 28 || strings.ToLower(realID) == realID {
		t.Fatalf("setup: %q is not a realistic mixed-case id", realID)
	}
	r.deliver("subscription_created_active.json", func(s string) string {
		return strings.ReplaceAll(s, "sub_"+r.uid.String()[:8], realID)
	})
	st, err := r.job.RunOnce(ctx)
	if err != nil || st.Sent != 1 || st.Failed != 0 {
		t.Fatalf("a realistic subscription id: %+v %v (the notice INSERT must accept its kind)", st, err)
	}
	if n := len(r.mail.to(r.addr)); n != 1 {
		t.Fatalf("%d emails, want 1", n)
	}
	var kind string
	if err := r.pool.QueryRow(`SELECT kind FROM account_notices WHERE user_id = $1 AND kind LIKE 'billing_purchase_%'`, r.uid).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != PurchaseKind(realID) {
		t.Errorf("stored kind %q, want %q (the SQL and Go hashes must agree, or the 'already sent' check never matches)", kind, PurchaseKind(realID))
	}
	// Idempotent through the SQL hash: a rerun finds it already sent.
	if st, _ := r.job.RunOnce(ctx); st.Sent != 0 || len(r.mail.to(r.addr)) != 1 {
		t.Errorf("a rerun sent again: %+v", st)
	}
	// A billing row that is within its window is still never listed as an in-app notice.
	store := launchnotice.Store{DB: r.pool}
	future := time.Now().Add(24 * time.Hour)
	if _, err := store.EnsureAny(ctx, r.uid, "billing_failed_20991231", FailureParams{GraceEnds: "2099-12-31"}, future); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureAny(ctx, r.uid, "launch_notice_probe", map[string]string{"x": "y"}, future); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx, r.uid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var sawProbe bool
	for _, n := range list {
		if strings.HasPrefix(n.Kind, "billing_") {
			t.Errorf("the in-app notice list contains a billing row: %s", n.Kind)
		}
		if n.Kind == "launch_notice_probe" {
			sawProbe = true
		}
	}
	if !sawProbe {
		t.Error("the filter hid an ordinary notice too")
	}
}
