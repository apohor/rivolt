//go:build integration

package applebill

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// StoreKit against real rows: the account's own report of its purchase links the subscription (only with its appAccountToken), then Apple's
// notifications move the entitlement: renew, cancel, grace, expiry, refund and its reversal; replays and out-of-order events change nothing; an
// unlinked subscription is answered 500 and left unrecorded so Apple's retry applies it; every change drops the cached plan.
func TestAppleBillingAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, _ := db.EnsureUser(ctx, pool, "iap-alice")
	bob, _ := db.EnsureUser(ctx, pool, "iap-bob")
	chain := newChain(t, chainOpts{})
	var outcomes []string
	var invalidated []uuid.UUID
	inv := func(u uuid.UUID) { invalidated = append(invalidated, u) }
	notif := &Handler{DB: pool, Verifier: chain.verifier, Config: testConfig(), Invalidate: inv, Observe: func(o string) { outcomes = append(outcomes, o) }}
	txn := &Transaction{DB: pool, Verifier: chain.verifier, Config: testConfig(), Invalidate: inv}

	now := time.Now().UTC().Truncate(time.Second)
	end1, end2 := now.Add(30*24*time.Hour), now.Add(60*24*time.Hour)
	const orig = "rv-test-orig-iap-1"
	entitlement := func(u uuid.UUID) (status string, end time.Time, cancelFlag bool, ok bool) {
		var e sql.NullTime
		err := pool.QueryRowContext(ctx, `SELECT status, current_period_end, cancel_at_period_end FROM entitlements WHERE user_id = $1 AND source = 'apple'`, u).Scan(&status, &e, &cancelFlag)
		if err == sql.ErrNoRows {
			return "", time.Time{}, false, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return status, e.Time.UTC(), cancelFlag, true
	}
	report := func(u uuid.UUID, signed string) (int, string) {
		b, _ := json.Marshal(map[string]string{"signedTransaction": signed})
		rec := httptest.NewRecorder()
		txn.Serve(u, rec, httptest.NewRequest(http.MethodPost, "/api/billing/apple/transaction", strings.NewReader(string(b))))
		return rec.Code, rec.Body.String()
	}
	send := func(typ, sub, id string, at time.Time, tx TransactionInfo) int {
		outcomes = nil
		return post(notif, notification(chain, t, typ, sub, id, at, tx)).Code
	}
	last := func() string {
		if len(outcomes) == 0 {
			return ""
		}
		return outcomes[len(outcomes)-1]
	}

	// An unlinked subscription: 500, and the notification is not recorded, so Apple's retry can apply it after the link.
	t0 := now.Add(-time.Hour)
	first := ti(orig, end1, alice.String())
	first.SignedDate = now.Add(-10 * time.Minute).UnixMilli() // older than everything below, newer than the retried SUBSCRIBED at t0
	nid := uuid.NewString()
	if code := send("SUBSCRIBED", "INITIAL_BUY", nid, t0, first); code != http.StatusInternalServerError || last() != OutcomeUnmapped {
		t.Fatalf("unlinked: %d %v", code, outcomes)
	}
	var recorded int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM apple_events WHERE notification_uuid = $1`, nid).Scan(&recorded); err != nil || recorded != 0 {
		t.Fatalf("an unmapped notification was recorded: %d %v", recorded, err)
	}

	// The account's report links it, only with its own token.
	if code, _ := report(bob, chain.sign(t, first)); code != http.StatusBadRequest {
		t.Errorf("bob reported alice's transaction: %d, want 400", code)
	}
	if code, body := report(alice, chain.sign(t, first)); code != http.StatusOK || !strings.Contains(body, `"pro":true`) {
		t.Fatalf("alice's report: %d %s", code, body)
	}
	if st, end, cancelFlag, ok := entitlement(alice); !ok || st != "active" || !end.Equal(end1) || cancelFlag {
		t.Fatalf("after the report: %v %v %v %v", st, end, cancelFlag, ok)
	}
	if code, _ := report(alice, chain.sign(t, first)); code != http.StatusOK {
		t.Errorf("the same report again: %d, want idempotent", code)
	}
	// The same subscription under another account's token: refused as already linked.
	stolen := ti(orig, end1, bob.String())
	if code, _ := report(bob, chain.sign(t, stolen)); code != http.StatusConflict {
		t.Errorf("a linked subscription posted by another account: %d, want 409", code)
	}
	if _, _, _, ok := entitlement(bob); ok {
		t.Error("bob got an entitlement")
	}

	// The retried notification now applies (SUBSCRIBED at t0 is older than the report's signedDate: stale or applied, but never harmful).
	if code := send("SUBSCRIBED", "INITIAL_BUY", nid, t0, first); code != http.StatusOK {
		t.Fatalf("retried subscribe: %d", code)
	}

	// Renewal moves the period; a replay of the same UUID changes nothing; an older event is stale.
	invalidated = nil
	renew := ti(orig, end2, alice.String())
	rid := uuid.NewString()
	if code := send("DID_RENEW", "", rid, now, renew); code != http.StatusOK || last() != OutcomeApplied {
		t.Fatalf("renew: %d %v", code, outcomes)
	}
	if _, end, _, _ := entitlement(alice); !end.Equal(end2) {
		t.Errorf("period end after the renewal: %v, want %v", end, end2)
	}
	if len(invalidated) != 1 || invalidated[0] != alice {
		t.Errorf("the plan cache was not dropped on a renewal: %v", invalidated)
	}
	if code := send("DID_RENEW", "", rid, now, renew); code != http.StatusOK || last() != OutcomeDuplicate {
		t.Errorf("replay: %d %v", code, outcomes)
	}
	older := ti(orig, end1, alice.String())
	if code := send("DID_RENEW", "", uuid.NewString(), now.Add(-30*time.Minute), older); code != http.StatusOK || last() != OutcomeStale {
		t.Errorf("out of order: %d %v", code, outcomes)
	}
	if _, end, _, _ := entitlement(alice); !end.Equal(end2) {
		t.Errorf("a stale event moved the period end back: %v", end)
	}

	// A report re-signed later than the renewal but carrying an older, shorter transaction must not shorten the period (Pro 30 F2).
	stale := ti(orig, end1, alice.String())
	stale.SignedDate = now.Add(time.Second).UnixMilli()
	if code, _ := report(alice, chain.sign(t, stale)); code != http.StatusOK {
		t.Errorf("a stale re-signed report: %d", code)
	}
	if _, end, _, _ := entitlement(alice); !end.Equal(end2) {
		t.Errorf("a stale re-signed report moved the period end to %v, want %v", end, end2)
	}

	// Auto-renew off, then on; a failed renewal in a grace period; the grace ending.
	step := now
	next := func() time.Time { step = step.Add(time.Minute); return step }
	send("DID_CHANGE_RENEWAL_STATUS", "AUTO_RENEW_DISABLED", uuid.NewString(), next(), renew)
	if st, _, cancelFlag, _ := entitlement(alice); st != "active" || !cancelFlag {
		t.Errorf("auto-renew off: %s cancel=%v", st, cancelFlag)
	}
	send("DID_CHANGE_RENEWAL_STATUS", "AUTO_RENEW_ENABLED", uuid.NewString(), next(), renew)
	if _, _, cancelFlag, _ := entitlement(alice); cancelFlag {
		t.Error("auto-renew on did not clear the flag")
	}
	send("DID_FAIL_TO_RENEW", "GRACE_PERIOD", uuid.NewString(), next(), renew)
	if st, _, _, _ := entitlement(alice); st != "past_due" {
		t.Errorf("grace: %s", st)
	}
	send("GRACE_PERIOD_EXPIRED", "", uuid.NewString(), next(), renew)
	if st, _, _, _ := entitlement(alice); st != "ended" {
		t.Errorf("grace expired: %s", st)
	}
	send("DID_RENEW", "", uuid.NewString(), next(), renew) // a late payment succeeds
	if st, _, _, _ := entitlement(alice); st != "active" {
		t.Errorf("renewed after grace: %s", st)
	}

	// A refund ends Pro at once and nothing older may bring it back; the reversal lifts it.
	invalidated = nil
	refundAt := next()
	refund := renew
	refund.RevocationDate = refundAt.UnixMilli()
	send("REFUND", "", uuid.NewString(), refundAt, refund)
	if st, _, _, _ := entitlement(alice); st != "ended" {
		t.Fatalf("refund: %s", st)
	}
	if len(invalidated) != 1 {
		t.Errorf("the plan cache was not dropped on a refund: %v", invalidated)
	}
	if code := send("DID_RENEW", "", uuid.NewString(), refundAt.Add(-time.Second), renew); code != http.StatusOK || last() != OutcomeStale {
		t.Errorf("an older renewal after a refund: %d %v", code, outcomes)
	}
	if st, _, _, _ := entitlement(alice); st != "ended" {
		t.Errorf("an older event revived a refunded subscription: %s", st)
	}
	send("REFUND_REVERSED", "", uuid.NewString(), next(), renew)
	if st, end, _, _ := entitlement(alice); st != "active" || !end.Equal(end2) {
		t.Errorf("refund reversed: %s %v", st, end)
	}

	// Things that change nothing: a TEST notification, a consumption request. (A TEST has no data object.)
	tb, _ := json.Marshal(map[string]string{"signedPayload": chain.sign(t, map[string]any{"notificationType": "TEST", "notificationUUID": uuid.NewString(), "signedDate": time.Now().UnixMilli()})})
	outcomes = nil
	if rec := post(notif, string(tb)); rec.Code != http.StatusOK || last() != OutcomeIgnored {
		t.Errorf("TEST: %d %v", rec.Code, outcomes)
	}
	if code := send("CONSUMPTION_REQUEST", "", uuid.NewString(), next(), renew); code != http.StatusOK || last() != OutcomeIgnored {
		t.Errorf("consumption request: %d %v", code, outcomes)
	}

	// A refunded transaction reported by the app grants nothing.
	const orig2 = "rv-test-orig-iap-2"
	refunded := ti(orig2, end2, alice.String())
	refunded.RevocationDate = now.UnixMilli()
	if code, body := report(alice, chain.sign(t, refunded)); code != http.StatusOK {
		t.Errorf("refunded report: %d %s", code, body)
	}
	if st, _, _, _ := entitlement(alice); st != "active" {
		t.Errorf("a refunded second transaction changed the entitlement: %s", st)
	}
}
