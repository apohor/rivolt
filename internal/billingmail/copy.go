package billingmail

import (
	"encoding/json"
	"time"

	"github.com/apohor/rivolt/internal/launchnotice"
)

// THE COPY BELOW IS A DRAFT for growth's wording and DA's review. What it must keep: the price and period, when the next charge is, that it
// renews until cancelled, how to cancel in one step (the Plan card, never a portal URL), what cancelling does, and no card data.

const footer = "\nTerms: https://rivolt.dev/terms · Privacy: https://rivolt.dev/privacy\n\nThe Rivolt team\nsupport@rivolt.dev\nRivolt is unofficial and not affiliated with Rivian.\n"

func longDate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("January 2, 2006")
}

// PurchaseSubject is the subject of the purchase confirmation.
func PurchaseSubject(PurchaseParams) string { return "Your Rivolt Pro subscription" }

// RenderPurchase is the purchase confirmation: the renewal terms, how to cancel and the refund rule (Stripe's receipt has none of them).
// Wording by growth. While the subscription is trialing the free period may be the 14-day trial or the founding year, so the line does not
// name which.
func RenderPurchase(p PurchaseParams) string {
	var first string
	if p.Trialing {
		first = "Your free period is running. Your first charge of " + p.Amount + " is on " + longDate(p.Date) + "; nothing is charged before then."
	} else {
		first = "You were charged " + p.Amount + " today. The next charge is on " + longDate(p.Date) + "."
	}
	return "Hi,\n\nThanks for subscribing to Rivolt Pro.\n\n" + first + "\n\n" +
		"The plan is " + p.Price + " and renews automatically every " + p.Interval + " until you cancel.\n\n" +
		"To cancel, open your Plan card (sign in first): " + launchnotice.PlanURL + "\n" +
		"Cancelling keeps Pro until the end of the period you have paid for. Nothing is deleted when a plan ends, and the full export is always free.\n" +
		"First charge not what you wanted? Email support@rivolt.dev within 14 days of it for a full refund, no questions.\n" +
		footer
}

// FailureSubject is the subject of the payment-failed notice.
func FailureSubject(FailureParams) string { return "Your Rivolt Pro payment didn't go through" }

// RenderFailure is the payment-failed notice: when the grace ends and how to update the card.
func RenderFailure(p FailureParams) string {
	end := longDate(p.GraceEnds)
	return "Hi,\n\nThe payment for your Rivolt Pro subscription did not go through. Pro stays on until " + end + " while your bank and Stripe retry.\n\n" +
		"To keep Pro, tap Update card on your Plan card (sign in first): " + launchnotice.PlanURL + "\n\n" +
		"If it still has not gone through by " + end + ", your account moves to the free plan. Nothing is deleted, every drive and charge keeps being recorded, and the full export is always free.\n" +
		footer
}

// render decodes a claimed notice's params and renders its email.
func render(kind string, raw json.RawMessage) (subject, text string, ok bool) {
	switch kind {
	case KindPurchase:
		var p PurchaseParams
		if json.Unmarshal(raw, &p) != nil || p.Price == "" {
			return "", "", false
		}
		return PurchaseSubject(p), RenderPurchase(p), true
	case KindPaymentFailed:
		var p FailureParams
		if json.Unmarshal(raw, &p) != nil || p.GraceEnds == "" {
			return "", "", false
		}
		return FailureSubject(p), RenderFailure(p), true
	}
	return "", "", false
}
