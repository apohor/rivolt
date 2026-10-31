package billingmail

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPriceLabel(t *testing.T) {
	for _, c := range []struct {
		amount   int64
		cur, per string
		want     string
	}{
		{3499, "usd", "year", "$34.99 a year"},
		{399, "USD", "month", "$3.99 a month"},
		{1000, "eur", "year", "€10.00 a year"},
		{500, "chf", "month", "CHF 5.00 a month"},
		{3499, "usd", "week", "$34.99 per week"},
	} {
		if got := PriceLabel(c.amount, c.cur, c.per); got != c.want {
			t.Errorf("PriceLabel(%d,%s,%s) = %q, want %q", c.amount, c.cur, c.per, got, c.want)
		}
	}
}

func TestNoticeKindsCarryWhatTheyAreAbout(t *testing.T) {
	// A real Stripe id is ~28 characters of mixed case; the kind must still fit account_notices' CHECK (^[a-z0-9_]{1,40}$).
	real := "sub_1MowQVLkdIwHu7ixeRlqHVzs"
	kind := PurchaseKind(real)
	if !noticeKindOK.MatchString(kind) {
		t.Errorf("PurchaseKind(%q) = %q does not satisfy the notice kind pattern", real, kind)
	}
	if kind != PurchaseKind(real) || kind == PurchaseKind(real+"x") {
		t.Error("PurchaseKind is not deterministic and distinct per subscription")
	}
	if !noticeKindOK.MatchString(FailureKind(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))) {
		t.Error("FailureKind does not satisfy the notice kind pattern")
	}
	end := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if got := FailureKind(end); got != "billing_failed_20261201" {
		t.Errorf("FailureKind = %q", got)
	}
	if FailureKind(end) == FailureKind(end.AddDate(0, 1, 0)) {
		t.Error("two failed periods share a kind: the second failure would never be announced")
	}
}

// What the purchase confirmation must say: the price and period, when the next charge is, that it renews until cancelled, how to cancel in one
// step, and what cancelling does. And what it must not: a raw billing-portal URL (a forwarded email would be a bearer link) or anything about a card.
func TestPurchaseEmailStatesTheRenewalTermsAndHowToCancel(t *testing.T) {
	paid := RenderPurchase(PurchaseParams{Interval: "year", Amount: "$34.99", Price: "$34.99 a year", Date: "2027-12-01"})
	trial := RenderPurchase(PurchaseParams{Interval: "year", Amount: "$34.99", Price: "$34.99 a year", Date: "2026-12-11", Trialing: true})
	for name, body := range map[string]string{"paid": paid, "trial": trial} {
		for _, want := range []string{"$34.99 a year", "renews automatically every year until you cancel", "https://rivolt.dev/settings#plan", "Cancelling keeps Pro until the end of the period you have paid for", "the full export is always free", "https://rivolt.dev/terms", "https://rivolt.dev/privacy", "within 14 days of it for a full refund", "support@rivolt.dev"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s email lacks %q:\n%s", name, want, body)
			}
		}
		for _, bad := range []string{"billing.stripe.com", "/p/session", "card number", "CVC"} {
			if strings.Contains(strings.ToLower(body), strings.ToLower(bad)) {
				t.Errorf("%s email contains %q", name, bad)
			}
		}
	}
	if !strings.Contains(paid, "charged $34.99 today. The next charge is on December 1, 2027") {
		t.Errorf("paid email's charge line:\n%s", paid)
	}
	if !strings.Contains(trial, "free period is running. Your first charge of $34.99 is on December 11, 2026; nothing is charged before then") {
		t.Errorf("trial email's charge line:\n%s", trial)
	}
}

func TestPaymentFailedEmailStatesWhenGraceEndsAndHowToFixIt(t *testing.T) {
	body := RenderFailure(FailureParams{GraceEnds: "2026-12-08"})
	for _, want := range []string{"did not go through", "stays on until December 8, 2026", "Update card", "https://rivolt.dev/settings#plan", "moves to the free plan", "Nothing is deleted", "while your bank and Stripe retry"} {
		if !strings.Contains(body, want) {
			t.Errorf("payment-failed email lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "billing.stripe.com") {
		t.Error("the payment-failed email carries a raw portal URL")
	}
	if FailureSubject(FailureParams{}) == "" || PurchaseSubject(PurchaseParams{}) == "" {
		t.Error("an email has no subject")
	}
}

func TestRenderRefusesBadParams(t *testing.T) {
	for _, c := range []struct{ kind, raw string }{
		{KindPurchase, `not json`}, {KindPurchase, `{}`}, {KindPaymentFailed, `{}`}, {"billing_unknown", `{"price":"x"}`},
	} {
		if _, _, ok := render(c.kind, []byte(c.raw)); ok {
			t.Errorf("render(%s, %s) succeeded", c.kind, c.raw)
		}
	}
	if _, _, ok := render(KindPurchase, []byte(`{"price":"$34.99 a year","amount":"$34.99","interval":"year","date":"2027-01-01"}`)); !ok {
		t.Error("a valid purchase does not render")
	}
}

// account_notices.kind: CHECK (kind ~ '^[a-z0-9_]{1,40}$') (migration 0100).
var noticeKindOK = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
