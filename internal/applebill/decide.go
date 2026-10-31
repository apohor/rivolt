package applebill

import (
	"time"

	"github.com/apohor/rivolt/internal/entitlements"
)

// Outcomes of an event, the metric's vocabulary (no user data).
const (
	OutcomeApplied   = "applied"
	OutcomeDuplicate = "duplicate"
	OutcomeIgnored   = "ignored"
	OutcomeUnmapped  = "unmapped"
	OutcomeStale     = "stale"
	OutcomeRejected  = "rejected"
)

// Outcomes lists them; the metric pre-creates each at 0.
var Outcomes = []string{OutcomeApplied, OutcomeDuplicate, OutcomeIgnored, OutcomeUnmapped, OutcomeStale, OutcomeRejected}

// Decision is what a notification does to the account's apple entitlement.
type Decision struct {
	// Change: write the row (false: the notification changes nothing).
	Change bool
	Status string // entitlements.StatusActive / StatusPastDue / StatusEnded
	// CancelAtPeriodEnd, when non-nil, sets the flag (auto-renew turned off or on); nil leaves it.
	CancelAtPeriodEnd *bool
	// Revoke: a refund or revoke ended Pro for good: older events may not bring it back. Unrevoke lifts that (REFUND_REVERSED).
	Revoke, Unrevoke bool
}

func boolp(b bool) *bool { return &b }

// Decide maps a notification (type, subtype) and its transaction to a Decision, at now. It is pure.
//
//	SUBSCRIBED, DID_RENEW, OFFER_REDEEMED, RENEWAL_EXTENDED   active until the transaction's expiry (ended if that has passed)
//	DID_CHANGE_RENEWAL_STATUS                                 the cancel-at-period-end flag follows the auto-renew subtype
//	DID_FAIL_TO_RENEW                                         past_due in a grace period; otherwise Pro runs to its expiry
//	GRACE_PERIOD_EXPIRED, EXPIRED                             ended
//	REFUND, REVOKE                                            ended at once, and nothing older may revive it
//	REFUND_REVERSED                                           lifts the revoke: active until the expiry
//	anything else (TEST, CONSUMPTION_REQUEST, PRICE_INCREASE, REFUND_DECLINED, ...)   changes nothing
func Decide(typ, subtype string, ti TransactionInfo, now time.Time) Decision {
	end := ti.Expires()
	live := func() string {
		if !end.IsZero() && end.After(now) {
			return entitlements.StatusActive
		}
		return entitlements.StatusEnded
	}
	if ti.Revoked() && typ != "REFUND_REVERSED" {
		return Decision{Change: true, Status: entitlements.StatusEnded, CancelAtPeriodEnd: boolp(false), Revoke: true}
	}
	switch typ {
	case "SUBSCRIBED", "DID_RENEW", "OFFER_REDEEMED", "RENEWAL_EXTENDED", "RENEWAL_EXTENSION":
		return Decision{Change: true, Status: live()}
	case "REFUND_REVERSED":
		return Decision{Change: true, Status: live(), Unrevoke: true}
	case "DID_CHANGE_RENEWAL_STATUS":
		switch subtype {
		case "AUTO_RENEW_DISABLED":
			return Decision{Change: true, Status: live(), CancelAtPeriodEnd: boolp(true)}
		case "AUTO_RENEW_ENABLED":
			return Decision{Change: true, Status: live(), CancelAtPeriodEnd: boolp(false)}
		}
		return Decision{}
	case "DID_FAIL_TO_RENEW":
		if subtype == "GRACE_PERIOD" {
			return Decision{Change: true, Status: entitlements.StatusPastDue}
		}
		return Decision{Change: true, Status: live()}
	case "GRACE_PERIOD_EXPIRED", "EXPIRED":
		return Decision{Change: true, Status: entitlements.StatusEnded, CancelAtPeriodEnd: boolp(false)}
	case "REFUND", "REVOKE":
		return Decision{Change: true, Status: entitlements.StatusEnded, CancelAtPeriodEnd: boolp(false), Revoke: true}
	}
	return Decision{}
}
