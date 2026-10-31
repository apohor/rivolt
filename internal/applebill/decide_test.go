package applebill

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/entitlements"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	future := TransactionInfo{OriginalTransactionID: "rv-test-orig-1", ExpiresDate: now.Add(30 * 24 * time.Hour).UnixMilli()}
	past := TransactionInfo{OriginalTransactionID: "rv-test-orig-1", ExpiresDate: now.Add(-time.Hour).UnixMilli()}
	revoked := future
	revoked.RevocationDate = now.Add(-time.Minute).UnixMilli()
	tr, fl := true, false
	cases := []struct {
		name, typ, sub string
		ti             TransactionInfo
		want           Decision
	}{
		{"subscribed", "SUBSCRIBED", "INITIAL_BUY", future, Decision{Change: true, Status: entitlements.StatusActive}},
		{"renew", "DID_RENEW", "", future, Decision{Change: true, Status: entitlements.StatusActive}},
		{"renew with an expiry already past", "DID_RENEW", "", past, Decision{Change: true, Status: entitlements.StatusEnded}},
		{"offer redeemed", "OFFER_REDEEMED", "", future, Decision{Change: true, Status: entitlements.StatusActive}},
		{"renewal extended", "RENEWAL_EXTENDED", "", future, Decision{Change: true, Status: entitlements.StatusActive}},
		{"auto-renew off", "DID_CHANGE_RENEWAL_STATUS", "AUTO_RENEW_DISABLED", future, Decision{Change: true, Status: entitlements.StatusActive, CancelAtPeriodEnd: &tr}},
		{"auto-renew on", "DID_CHANGE_RENEWAL_STATUS", "AUTO_RENEW_ENABLED", future, Decision{Change: true, Status: entitlements.StatusActive, CancelAtPeriodEnd: &fl}},
		{"renewal status, unknown subtype", "DID_CHANGE_RENEWAL_STATUS", "", future, Decision{}},
		{"failed to renew, grace period", "DID_FAIL_TO_RENEW", "GRACE_PERIOD", future, Decision{Change: true, Status: entitlements.StatusPastDue}},
		{"failed to renew, no grace", "DID_FAIL_TO_RENEW", "", future, Decision{Change: true, Status: entitlements.StatusActive}},
		{"grace expired", "GRACE_PERIOD_EXPIRED", "", future, Decision{Change: true, Status: entitlements.StatusEnded, CancelAtPeriodEnd: &fl}},
		{"expired", "EXPIRED", "VOLUNTARY", past, Decision{Change: true, Status: entitlements.StatusEnded, CancelAtPeriodEnd: &fl}},
		{"refund", "REFUND", "", future, Decision{Change: true, Status: entitlements.StatusEnded, CancelAtPeriodEnd: &fl, Revoke: true}},
		{"revoke", "REVOKE", "", future, Decision{Change: true, Status: entitlements.StatusEnded, CancelAtPeriodEnd: &fl, Revoke: true}},
		{"refund reversed", "REFUND_REVERSED", "", future, Decision{Change: true, Status: entitlements.StatusActive, Unrevoke: true}},
		{"a renewal on a revoked transaction", "DID_RENEW", "", revoked, Decision{Change: true, Status: entitlements.StatusEnded, CancelAtPeriodEnd: &fl, Revoke: true}},
		{"test", "TEST", "", TransactionInfo{}, Decision{}},
		{"consumption request", "CONSUMPTION_REQUEST", "", future, Decision{}},
		{"price increase", "PRICE_INCREASE", "ACCEPTED", future, Decision{}},
		{"refund declined", "REFUND_DECLINED", "", future, Decision{}},
	}
	for _, c := range cases {
		got := Decide(c.typ, c.sub, c.ti, now)
		if got.Change != c.want.Change || got.Status != c.want.Status || got.Revoke != c.want.Revoke || got.Unrevoke != c.want.Unrevoke ||
			(got.CancelAtPeriodEnd == nil) != (c.want.CancelAtPeriodEnd == nil) || (got.CancelAtPeriodEnd != nil && *got.CancelAtPeriodEnd != *c.want.CancelAtPeriodEnd) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
