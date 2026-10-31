package stripebill

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

type fakeCountries struct {
	mu      sync.Mutex
	country string
	err     error
	calls   int
}

func (f *fakeCountries) CustomerCountry(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.country, f.err
}

func evt(typ, obj string) Event {
	var e Event
	e.ID, e.Type = "evt_1", typ
	e.Data.Object = []byte(obj)
	return e
}

// The US-only guard reports a billing country other than the US, once per customer and country, with the country code only, and does nothing
// else: it neither returns an error nor changes the event.
func TestCountryGuard(t *testing.T) {
	var logs bytes.Buffer
	var counted []string
	fc := &fakeCountries{}
	h := &Handler{Log: slog.New(slog.NewTextHandler(&logs, nil)), Countries: fc, OnNonUS: func(c string) { counted = append(counted, c) }}
	ctx := context.Background()

	// Checkout's own country, from the event.
	h.checkCountry(ctx, evt("checkout.session.completed", `{"mode":"subscription","customer":"cus_a","customer_details":{"address":{"country":"DE","line1":"1 Private Street"}}}`), OutcomeApplied)
	h.checkCountry(ctx, evt("checkout.session.completed", `{"mode":"subscription","customer":"cus_a","customer_details":{"address":{"country":"DE"}}}`), OutcomeApplied) // repeat: once
	h.checkCountry(ctx, evt("checkout.session.completed", `{"mode":"subscription","customer":"cus_b","customer_details":{"address":{"country":"US"}}}`), OutcomeApplied)
	h.checkCountry(ctx, evt("checkout.session.completed", `{"mode":"subscription","customer":"cus_c"}`), OutcomeApplied)                                                  // no address
	h.checkCountry(ctx, evt("checkout.session.completed", `{"mode":"subscription","customer":"cus_d","customer_details":{"address":{"country":"FR"}}}`), OutcomeUnmapped) // not applied
	if len(counted) != 1 || counted[0] != "DE" {
		t.Fatalf("after checkout events: counted %v, want [DE]", counted)
	}

	// An address change is caught at the next customer.subscription.updated (customer.updated is not subscribed to): the customer is read
	// from Stripe then.
	fc.country = "CA"
	h.checkCountry(ctx, evt("customer.subscription.updated", `{"id":"sub_1","customer":"cus_e"}`), OutcomeApplied)
	fc.country = "US"
	h.checkCountry(ctx, evt("customer.subscription.updated", `{"id":"sub_2","customer":"cus_f"}`), OutcomeApplied)
	fc.err = errors.New("stripe: 500")
	h.checkCountry(ctx, evt("customer.subscription.updated", `{"id":"sub_3","customer":"cus_g"}`), OutcomeApplied) // unreadable: nothing, no panic
	h.checkCountry(ctx, evt("customer.subscription.created", `{"id":"sub_4","customer":"cus_h"}`), OutcomeApplied) // not asked
	if fc.calls != 3 {
		t.Errorf("%d customer reads, want 3 (only subscription.updated asks)", fc.calls)
	}
	if strings.Join(counted, ",") != "DE,CA" {
		t.Errorf("counted %v, want [DE CA]", counted)
	}

	out := logs.String()
	if n := strings.Count(out, "billing address outside the US"); n != 2 {
		t.Errorf("%d warn lines, want 2:\n%s", n, out)
	}
	for _, leak := range []string{"Private Street", "sub_", "evt_"} {
		if strings.Contains(out, leak) {
			t.Errorf("the log carries %q:\n%s", leak, out)
		}
	}
	// The operator finds the customer by the opaque Stripe id (Stripe's search does not filter by country): the id is in the line, the address
	// and the subscription and event ids are not.
	for _, want := range []string{"country=DE", "customer=cus_a", "country=CA", "customer=cus_e"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
	// No reader configured: only Checkout's own country is checked.
	h2 := &Handler{}
	h2.checkCountry(ctx, evt("customer.subscription.updated", `{"id":"sub_5","customer":"cus_i"}`), OutcomeApplied)
}
