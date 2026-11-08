package stripebill

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Opt-in: the Checkout form against Stripe's real API in TEST mode. The unit tests send the form to a fake, and a fake accepts anything: the
// preview's 502 came from Stripe rejecting payment_method_types (no longer supported on a Checkout Session) while the form test was green.
// Set RIVOLT_STRIPE_SANDBOX_KEY (an sk_test_ or rk_test_ key that can create Checkout Sessions), RIVOLT_STRIPE_SANDBOX_PRICE (a recurring
// price in that account) and RIVOLT_STRIPE_SANDBOX_PMC (a payment method configuration there) to run it; it is skipped otherwise, which is
// every CI run. It creates one unpaid Checkout Session (they expire on their own) and prints nothing from the account.
func TestCheckoutAgainstTheStripeSandbox(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("RIVOLT_STRIPE_SANDBOX_KEY"))
	price := strings.TrimSpace(os.Getenv("RIVOLT_STRIPE_SANDBOX_PRICE"))
	pmc := strings.TrimSpace(os.Getenv("RIVOLT_STRIPE_SANDBOX_PMC"))
	if key == "" || price == "" || pmc == "" {
		t.Skip("opt-in: set RIVOLT_STRIPE_SANDBOX_KEY, _PRICE and _PMC to run against Stripe's test mode")
	}
	if !strings.HasPrefix(key, "sk_test_") && !strings.HasPrefix(key, "rk_test_") {
		t.Fatal("RIVOLT_STRIPE_SANDBOX_KEY must be a test-mode key: this test creates a Checkout Session")
	}
	svc := &Service{
		Client: &Client{SecretKey: key}, Plans: fakePlans{billing: true},
		Prices: map[string]string{"month": price, "year": price}, BaseURL: "https://rivolt.example", PaymentMethodConfig: pmc,
	}
	url, err := svc.Checkout(context.Background(), uuid.New(), "year")
	if err != nil {
		t.Fatalf("Stripe refused the Checkout Session the form builds: %v", err)
	}
	if !strings.HasPrefix(url, "https://checkout.stripe.com/") {
		t.Errorf("the session URL is not Stripe's hosted Checkout")
	}
}

// Opt-in, same key as above (test mode only): the void of a deleted account's open invoices against Stripe's real API. The fake in the unit
// tests answers whatever the code expects; this replays the three cases that matter on the real thing: an open renewal-sized invoice (voided,
// the customer deleted), an invoice over the Pro annual cap (left open, the customer kept) and a partly paid one (left open, the customer
// kept). It creates test customers with invoices, runs CloseCustomer, logs the outcomes (ids and statuses only) and cleans up.
// RIVOLT_STRIPE_SANDBOX_PRICE is only read for the cap: its amount and currency are the Pro annual price of that account.
func TestVoidOpenInvoicesAgainstTheStripeSandbox(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("RIVOLT_STRIPE_SANDBOX_KEY"))
	price := strings.TrimSpace(os.Getenv("RIVOLT_STRIPE_SANDBOX_PRICE"))
	if key == "" || price == "" {
		t.Skip("opt-in: set RIVOLT_STRIPE_SANDBOX_KEY and RIVOLT_STRIPE_SANDBOX_PRICE to run against Stripe's test mode")
	}
	if !strings.HasPrefix(key, "sk_test_") && !strings.HasPrefix(key, "rk_test_") {
		t.Fatal("RIVOLT_STRIPE_SANDBOX_KEY must be a test-mode key: this test creates and voids invoices")
	}
	ctx := context.Background()
	c := &Client{SecretKey: key}
	p, err := c.Price(ctx, price)
	if err != nil {
		t.Fatalf("cannot read the price: %v", err)
	}
	limit := VoidCap{Cents: p.UnitAmount, Currency: p.Currency}
	t.Logf("cap = %d %s", limit.Cents, limit.Currency)

	// The account's default API version (what a request without a Stripe-Version gets) is in the response header; the open-invoice list pins
	// invoiceListVersion instead, because from basil on invoice.payment_intent is gone.
	if req, err := http.NewRequestWithContext(ctx, "GET", DefaultAPIBase+"/v1/customers?limit=1", nil); err == nil {
		req.Header.Set("Authorization", "Bearer "+key)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			t.Logf("account default Stripe-Version = %s; the open-invoice list pins %s", resp.Header.Get("Stripe-Version"), invoiceListVersion)
			resp.Body.Close()
		}
	}
	// Every helper takes the t of the (sub)test that uses it, so a failure stops that subtest and its cleanups run.
	call := func(t *testing.T, method, path string, form url.Values) map[string]any {
		t.Helper()
		var req *http.Request
		if form != nil {
			req, _ = http.NewRequestWithContext(ctx, method, DefaultAPIBase+path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req, _ = http.NewRequestWithContext(ctx, method, DefaultAPIBase+path, nil)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode/100 != 2 {
			// Stripe's own error text names the parameter that was wrong; it never carries the key.
			e, _ := out["error"].(map[string]any)
			t.Fatalf("%s %s: %d code=%v param=%v message=%v", method, path, resp.StatusCode, e["code"], e["param"], e["message"])
		}
		return out
	}
	// A customer with one open invoice of the given subtotal: a send_invoice invoice is finalized and open with no payment attempt. The invoice
	// item is created first, then attached to the invoice by id (an invoice item with no invoice is pending until an invoice includes it).
	openInvoice := func(t *testing.T, cents int64) (cust, inv string) {
		t.Helper()
		cust = call(t, "POST", "/v1/customers", url.Values{"description": {"rivolt void replay"}})["id"].(string)
		t.Cleanup(func() { // best effort; a customer with an open invoice cannot always be deleted
			if out := call(t, "GET", "/v1/invoices?status=open&customer="+cust, nil); out["data"] != nil {
				for _, d := range out["data"].([]any) {
					_ = call(t, "POST", "/v1/invoices/"+d.(map[string]any)["id"].(string)+"/void", url.Values{})
				}
			}
			req, _ := http.NewRequestWithContext(ctx, "DELETE", DefaultAPIBase+"/v1/customers/"+cust, nil)
			req.Header.Set("Authorization", "Bearer "+key)
			if r, err := http.DefaultClient.Do(req); err == nil {
				r.Body.Close()
			}
		})
		in := call(t, "POST", "/v1/invoices", url.Values{"customer": {cust}, "collection_method": {"send_invoice"}, "days_until_due": {"7"}, "auto_advance": {"false"}})
		inv = in["id"].(string)
		call(t, "POST", "/v1/invoiceitems", url.Values{"customer": {cust}, "invoice": {inv}, "amount": {strconv.FormatInt(cents, 10)}, "currency": {limit.Currency}, "description": {"replay"}})
		call(t, "POST", "/v1/invoices/"+inv+"/finalize", url.Values{})
		return cust, inv
	}
	status := func(t *testing.T, inv string) string {
		return call(t, "GET", "/v1/invoices/"+inv, nil)["status"].(string)
	}
	gone := func(t *testing.T, cust string) bool {
		out := call(t, "GET", "/v1/customers/"+cust, nil)
		d, _ := out["deleted"].(bool)
		return d
	}

	t.Run("open renewal invoice is voided and the customer deleted", func(t *testing.T) {
		cust, inv := openInvoice(t, limit.Cents)
		rep, err := c.CloseCustomer(ctx, cust, limit)
		t.Logf("voided=%d left=%d err=%v invoice=%s", len(rep.Voided), len(rep.Left), err, status(t, inv))
		if err != nil || len(rep.Voided) != 1 || status(t, inv) != "void" || !gone(t, cust) {
			t.Errorf("want the invoice void and the customer deleted: %+v %v", rep, err)
		}
	})
	t.Run("over-cap invoice is left open", func(t *testing.T) {
		cust, inv := openInvoice(t, limit.Cents+1)
		rep, err := c.CloseCustomer(ctx, cust, limit)
		t.Logf("voided=%d left=%v err=%v invoice=%s", len(rep.Voided), rep.Left, err, status(t, inv))
		if !errors.Is(err, ErrOpenItems) || len(rep.Voided) != 0 || status(t, inv) != "open" || gone(t, cust) {
			t.Errorf("want the invoice open and the customer kept: %+v %v", rep, err)
		}
	})
	t.Run("part-paid invoice is left open", func(t *testing.T) {
		cust, inv := openInvoice(t, limit.Cents)
		// A partial payment: a succeeded PaymentIntent for less than the invoice, attached to it.
		pm := call(t, "POST", "/v1/payment_intents", url.Values{"amount": {"500"}, "currency": {limit.Currency}, "customer": {cust}, "payment_method": {"pm_card_visa"}, "confirm": {"true"}, "automatic_payment_methods[enabled]": {"true"}, "automatic_payment_methods[allow_redirects]": {"never"}})
		call(t, "POST", "/v1/invoices/"+inv+"/attach_payment", url.Values{"payment_intent": {pm["id"].(string)}})
		if got := call(t, "GET", "/v1/invoices/"+inv, nil); got["status"] != "open" || got["amount_paid"].(float64) == 0 {
			t.Skipf("could not make a part-paid open invoice here (status %v, amount_paid %v)", got["status"], got["amount_paid"])
		}
		rep, err := c.CloseCustomer(ctx, cust, limit)
		t.Logf("voided=%d left=%v err=%v invoice=%s", len(rep.Voided), rep.Left, err, status(t, inv))
		if !errors.Is(err, ErrOpenItems) || len(rep.Voided) != 0 || status(t, inv) != "open" || gone(t, cust) {
			t.Errorf("want the invoice open and the customer kept: %+v %v", rep, err)
		}
	})
}
