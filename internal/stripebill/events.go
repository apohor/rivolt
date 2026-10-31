package stripebill

import (
	"encoding/json"
	"time"

	"github.com/apohor/rivolt/internal/entitlements"
)

// Event is the envelope of a webhook delivery (https://docs.stripe.com/api/events/object); Data.Object is the changed object, parsed by type.
type Event struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// Subscription is the part of a Stripe Subscription object Rivolt reads. current_period_end is on the subscription in older API versions
// and on its items in newer ones; both are read, the subscription's first.
type Subscription struct {
	ID                string `json:"id"`
	Customer          string `json:"customer"`
	Status            string `json:"status"`
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
	CancelAt          int64  `json:"cancel_at"`
	CurrentPeriodEnd  int64  `json:"current_period_end"`
	Items             struct {
		Data []struct {
			CurrentPeriodEnd int64 `json:"current_period_end"`
			Price            struct {
				UnitAmount int64  `json:"unit_amount"`
				Currency   string `json:"currency"`
				Recurring  struct {
					Interval string `json:"interval"`
				} `json:"recurring"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
	Metadata map[string]string `json:"metadata"`
}

// PeriodEnd is the end of the current period (zero: unknown).
func (s Subscription) PeriodEnd() time.Time {
	end := s.CurrentPeriodEnd
	if end == 0 {
		for _, it := range s.Items.Data {
			if it.CurrentPeriodEnd > end {
				end = it.CurrentPeriodEnd
			}
		}
	}
	if end == 0 {
		return time.Time{}
	}
	return time.Unix(end, 0).UTC()
}

// Terms is what the subscription costs and how often it renews, from its first item's price (empty and zero when the event carries none).
func (s Subscription) Terms() (interval string, amount int64, currency string) {
	for _, it := range s.Items.Data {
		if it.Price.Recurring.Interval != "" && it.Price.UnitAmount > 0 {
			return it.Price.Recurring.Interval, it.Price.UnitAmount, it.Price.Currency
		}
	}
	return "", 0, ""
}

// WillCancel is a subscription set to stop at the period end, by either of Stripe's two ways of saying so.
func (s Subscription) WillCancel() bool { return s.CancelAtPeriodEnd || s.CancelAt != 0 }

// EntitlementStatus maps a Stripe subscription status to an entitlement status. ok is false for a status that must change nothing
// (incomplete: the first payment has not happened, so there is nothing to grant and nothing to take away).
func EntitlementStatus(stripeStatus string) (status string, ok bool) {
	switch stripeStatus {
	case "active", "trialing":
		return entitlements.StatusActive, true
	case "past_due", "unpaid":
		return entitlements.StatusPastDue, true
	case "canceled", "incomplete_expired", "paused":
		return entitlements.StatusEnded, true
	}
	return "", false
}

// CheckoutSession is the part of a completed Checkout Session Rivolt reads.
type CheckoutSession struct {
	Mode              string `json:"mode"`
	Customer          string `json:"customer"`
	Subscription      string `json:"subscription"`
	ClientReferenceID string `json:"client_reference_id"`
	// CustomerDetails.Address.Country is the billing country the customer entered (two letters), when Checkout collected it.
	CustomerDetails struct {
		Address struct {
			Country string `json:"country"`
		} `json:"address"`
	} `json:"customer_details"`
}

// Charge is the part of a Charge (charge.refunded) Rivolt reads. (A dispute object carries no customer, so mapping it to an account needs
// an API call; disputes are left to the Stripe dashboard until the live account exists.)
type Charge struct {
	ID       string `json:"id"`
	Customer string `json:"customer"`
	Refunded bool   `json:"refunded"`
}
