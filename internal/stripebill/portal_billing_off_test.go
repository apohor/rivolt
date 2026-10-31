package stripebill

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// The Customer Portal is not gated by RIVOLT_BILLING (a subscription keeps renewing whatever the flag says, so its holder must be able
// to cancel it), while Checkout stays refused with billing off. Without a database there is no customer, so the portal gets as far as
// ErrNoCustomer; the account-with-a-customer half is the Postgres test beside the webhook's.
func TestPortalIsNotGatedByBilling(t *testing.T) {
	f := newFakeStripe(t)
	svc := newService(f, fakePlans{billing: false}, launchDay)
	if _, err := svc.Portal(context.Background(), uuid.New()); !errors.Is(err, ErrNoCustomer) {
		t.Errorf("Portal with billing off: %v, want ErrNoCustomer (past the billing gate, no customer)", err)
	}
	if _, err := svc.Checkout(context.Background(), uuid.New(), "year"); !errors.Is(err, ErrBillingOff) {
		t.Errorf("Checkout with billing off: %v, want ErrBillingOff", err)
	}
	if len(f.posts) != 0 {
		t.Errorf("%d requests reached Stripe", len(f.posts))
	}
}
