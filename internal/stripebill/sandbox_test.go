package stripebill

import (
	"context"
	"os"
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
