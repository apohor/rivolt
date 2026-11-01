package stripebill

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/entitlements"
)

// fakeStripe records what Rivolt sends and answers the three calls Rivolt makes, in Stripe's documented shapes.
type fakeStripe struct {
	srv       *httptest.Server
	mu        sync.Mutex
	posts     []url.Values
	headers   []http.Header
	priceGets int
	fail      bool
	// subs: customer id -> [subscription id, status] in list order; pageSize 0 = one page.
	subs          map[string][][2]string
	pageSize      int
	listGets      int
	deleted       []string
	deleteQueries []url.Values
	// customers: open invoices per customer; charges per customer as [charge id, dispute status or ""]; gone = deleted customers.
	openInvoices     map[string]int
	charges          map[string][][2]string
	gone             map[string]bool
	customersDeleted []string
}

func (f *fakeStripe) missing(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such customer"}}`))
}

const (
	testKey      = "sk_test_unit_key"
	priceYear    = "price_TEST_YEAR"
	priceMonth   = "price_TEST_MONTH"
	checkoutPage = "https://checkout.stripe.test/c/pay/cs_test_1"
	portalPage   = "https://billing.stripe.test/p/session/bps_test_1"
)

func newFakeStripe(t *testing.T) *fakeStripe {
	f := &fakeStripe{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			http.Error(w, `{"error":{"type":"invalid_request_error","message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if f.fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error","code":"internal","message":"stripe says: sk_test_unit_key leaked in a message"}}`))
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/prices/"):
			f.priceGets++
			id := strings.TrimPrefix(r.URL.Path, "/v1/prices/")
			amount, interval := 3499, "year"
			if id == priceMonth {
				amount, interval = 399, "month"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "unit_amount": amount, "currency": "usd", "recurring": map[string]any{"interval": interval}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions":
			_ = r.ParseForm()
			f.posts = append(f.posts, r.PostForm)
			f.headers = append(f.headers, r.Header.Clone())
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "cs_test_1", "url": checkoutPage})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/billing_portal/sessions":
			_ = r.ParseForm()
			f.posts = append(f.posts, r.PostForm)
			f.headers = append(f.headers, r.Header.Clone())
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "bps_test_1", "url": portalPage})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/subscriptions":
			f.listGets++
			cust := r.URL.Query().Get("customer")
			if f.gone[cust] || f.subs == nil || f.subs[cust] == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such customer"}}`))
				return
			}
			all := f.subs[cust]
			start := 0
			if after := r.URL.Query().Get("starting_after"); after != "" {
				for i, sub := range all {
					if sub[0] == after {
						start = i + 1
					}
				}
			}
			end := start + f.pageSize
			if f.pageSize == 0 || end > len(all) {
				end = len(all)
			}
			var data []map[string]any
			for _, sub := range all[start:end] {
				data = append(data, map[string]any{"id": sub[0], "status": sub[1]})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": end < len(all)})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/invoices":
			cust := r.URL.Query().Get("customer")
			if f.gone[cust] {
				f.missing(w)
				return
			}
			var data []map[string]any
			for i := 0; i < f.openInvoices[cust]; i++ {
				data = append(data, map[string]any{"id": "in_open_" + strconv.Itoa(i), "status": "open"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": false})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/charges":
			cust := r.URL.Query().Get("customer")
			if f.gone[cust] {
				f.missing(w)
				return
			}
			var data []map[string]any
			for _, c := range f.charges[cust] {
				data = append(data, map[string]any{"id": c[0], "disputed": c[1] != ""})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": false})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/disputes":
			ch := r.URL.Query().Get("charge")
			var data []map[string]any
			for _, cs := range f.charges {
				for _, c := range cs {
					if c[0] == ch && c[1] != "" {
						data = append(data, map[string]any{"id": "dp_" + ch, "status": c[1]})
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": false})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/customers/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/customers/")
			if f.gone[id] {
				f.missing(w)
				return
			}
			if f.gone == nil {
				f.gone = map[string]bool{}
			}
			f.gone[id] = true
			f.customersDeleted = append(f.customersDeleted, id)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "deleted": true})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/subscriptions/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/subscriptions/")
			f.deleted = append(f.deleted, id)
			f.deleteQueries = append(f.deleteQueries, r.URL.Query())
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "canceled"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// fakePlans is an account's plan, set per test.
type fakePlans struct {
	billing bool
	state   entitlements.State
	trial   time.Time // zero: none
}

func (p fakePlans) Billing() bool { return p.billing }
func (p fakePlans) State(context.Context, uuid.UUID) (entitlements.State, error) {
	return p.state, nil
}
func (p fakePlans) FreeUntil(context.Context, uuid.UUID) (time.Time, bool, error) {
	return p.trial, !p.trial.IsZero(), nil
}

func newService(f *fakeStripe, plans fakePlans, now time.Time) *Service {
	return &Service{
		Client:  &Client{BaseURL: f.srv.URL, SecretKey: testKey},
		Plans:   plans,
		Prices:  map[string]string{"month": priceMonth, "year": priceYear},
		BaseURL: "https://rivolt.example",
		Now:     func() time.Time { return now },
		// A payment method configuration, as production sets (RIVOLT_STRIPE_PAYMENT_METHOD_CONFIG).
		PaymentMethodConfig: testPMC,
	}
}

const testPMC = "pmc_1TestRivoltProCardLink"

var launchDay = time.Date(2026, 12, 1, 15, 0, 0, 0, time.UTC)

func TestConsentLine(t *testing.T) {
	year := Price{UnitAmount: 3499, Currency: "usd", Interval: "year"}
	month := Price{UnitAmount: 399, Currency: "usd", Interval: "month"}
	for _, c := range []struct {
		p     Price
		first time.Time
		want  string
	}{
		{year, launchDay, "First charge today: $34.99, then $34.99 every year until you cancel. Cancel any time in Settings."},
		{month, launchDay.Add(10 * 24 * time.Hour), "First charge on December 11, 2026: $3.99, then $3.99 every month until you cancel. Cancel any time in Settings."},
		{Price{UnitAmount: 1000, Currency: "chf", Interval: "month"}, launchDay, "First charge today: CHF 10.00, then CHF 10.00 every month until you cancel. Cancel any time in Settings."},
		{month, launchDay.Add(3 * time.Hour), "First charge today: $3.99, then $3.99 every month until you cancel. Cancel any time in Settings."},
	} {
		if got := ConsentLine(c.p, c.first, launchDay); got != c.want {
			t.Errorf("ConsentLine:\n got %q\nwant %q", got, c.want)
		}
	}
}

// What is sent to Stripe for a new account: the account is the signed-in one in both places Stripe will echo it back, the consent line
// is built from Stripe's own price, the page asks for Terms acceptance, and nothing else names a person.
func TestBuildCheckoutForANewAccount(t *testing.T) {
	f := newFakeStripe(t)
	svc := newService(f, fakePlans{billing: true}, launchDay)
	uid := uuid.New()
	got, err := svc.Checkout(context.Background(), uid, "year")
	if err != nil || got != checkoutPage {
		t.Fatalf("Checkout = %q, %v", got, err)
	}
	form := f.posts[0]
	for k, want := range map[string]string{
		"mode":                    "subscription",
		"line_items[0][price]":    priceYear,
		"line_items[0][quantity]": "1",
		"client_reference_id":     uid.String(),
		"subscription_data[metadata][rivolt_user_id]": uid.String(),
		"success_url":                          "https://rivolt.example/settings?checkout=success#plan",
		"cancel_url":                           "https://rivolt.example/settings?checkout=cancelled#plan",
		"consent_collection[terms_of_service]": "required",
		"custom_text[submit][message]":         "First charge today: $34.99, then $34.99 every year until you cancel. Cancel any time in Settings.",
	} {
		if form.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, form.Get(k), want)
		}
	}
	for _, absent := range []string{"subscription_data[trial_end]", "customer", "automatic_tax[enabled]", "customer_email", "customer_update[address]", "custom_text[after_submit][message]"} {
		if _, ok := form[absent]; ok {
			t.Errorf("%s is set for a new account with tax off", absent)
		}
	}
	if k := f.headers[0].Get("Idempotency-Key"); !strings.HasPrefix(k, "rivolt-checkout-"+uid.String()+"-year-") {
		t.Errorf("Idempotency-Key = %q", k)
	}
	// The key is the bearer only; nothing else carries it.
	for _, vs := range form {
		for _, v := range vs {
			if strings.Contains(v, testKey) {
				t.Errorf("the secret key appears in the form: %q", v)
			}
		}
	}
}

// A price is read once per process.
func TestPriceIsCached(t *testing.T) {
	f := newFakeStripe(t)
	svc := newService(f, fakePlans{billing: true}, launchDay)
	for i := 0; i < 3; i++ {
		if _, err := svc.Checkout(context.Background(), uuid.New(), "month"); err != nil {
			t.Fatal(err)
		}
	}
	if f.priceGets != 1 {
		t.Errorf("the price was read %d times, want once", f.priceGets)
	}
}

// Mid-trial: billing starts when the trial ends (Stripe's trial_end), and the consent line names that date. A trial ending within 48 hours is
// pushed to Stripe's minimum, and the line says the date that will actually apply.
func TestCheckoutDuringATrial(t *testing.T) {
	f := newFakeStripe(t)
	end := launchDay.Add(10 * 24 * time.Hour)
	svc := newService(f, fakePlans{billing: true, trial: end}, launchDay)
	if _, err := svc.Checkout(context.Background(), uuid.New(), "year"); err != nil {
		t.Fatal(err)
	}
	form := f.posts[0]
	if got := form.Get("subscription_data[trial_end]"); got != strconv.FormatInt(end.Unix(), 10) {
		t.Errorf("trial_end = %q, want the trial's end %d", got, end.Unix())
	}
	if got := form.Get("custom_text[submit][message]"); !strings.HasPrefix(got, "First charge on December 11, 2026: $34.99") {
		t.Errorf("consent line = %q", got)
	}

	soon := launchDay.Add(20 * time.Hour)
	svc = newService(f, fakePlans{billing: true, trial: soon}, launchDay)
	if _, err := svc.Checkout(context.Background(), uuid.New(), "month"); err != nil {
		t.Fatal(err)
	}
	form = f.posts[len(f.posts)-1]
	want := launchDay.Add(MinTrialLead + time.Minute)
	if got := form.Get("subscription_data[trial_end]"); got != strconv.FormatInt(want.Unix(), 10) {
		t.Errorf("trial_end for a trial ending in 20 h = %q, want Stripe's minimum %d", got, want.Unix())
	}
	if got := form.Get("custom_text[submit][message]"); !strings.HasPrefix(got, "First charge on December 3, 2026: $3.99") {
		t.Errorf("consent line for a short trial = %q", got)
	}
}

func TestCheckoutRefusals(t *testing.T) {
	f := newFakeStripe(t)
	ctx := context.Background()
	uid := uuid.New()
	subscribed := func(src string) fakePlans {
		return fakePlans{billing: true, state: entitlements.State{Pro: true, Source: src}}
	}
	for _, c := range []struct {
		name     string
		plans    fakePlans
		interval string
		want     error
	}{
		{"billing off", fakePlans{}, "year", ErrBillingOff},
		{"an interval that is not offered", fakePlans{billing: true}, "week", ErrBadInterval},
		{"no interval", fakePlans{billing: true}, "", ErrBadInterval},
		{"a Stripe subscriber", subscribed(entitlements.SourceStripe), "year", ErrAlreadySubscribed},
		{"an Apple subscriber (no double billing)", subscribed(entitlements.SourceApple), "month", ErrAlreadySubscribed},
	} {
		if _, err := newService(f, c.plans, launchDay).Checkout(ctx, uid, c.interval); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if len(f.posts) != 0 {
		t.Errorf("a refused request still created %d Stripe sessions", len(f.posts))
	}
	// A founding, grant or trial account may buy.
	for _, src := range []string{entitlements.SourceFounding, entitlements.SourceGrant, entitlements.SourceTrial} {
		if _, err := newService(f, subscribed(src), launchDay).Checkout(ctx, uid, "year"); err != nil {
			t.Errorf("a %s account was refused: %v", src, err)
		}
	}
}

// Stripe failing or refusing is an error to the handler; the key never leaks into it.
func TestStripeFailureIsAnErrorWithoutTheKey(t *testing.T) {
	f := newFakeStripe(t)
	svc := newService(f, fakePlans{billing: true}, launchDay)
	if _, err := svc.Checkout(context.Background(), uuid.New(), "year"); err != nil {
		t.Fatal(err)
	}
	f.fail = true
	svc.Client.prices = nil // force the price read to fail too
	_, err := svc.Checkout(context.Background(), uuid.New(), "year")
	var se *Error
	if !errors.As(err, &se) || se.Status != http.StatusInternalServerError {
		t.Fatalf("a failing Stripe: %v, want a *Error with status 500", err)
	}
	bad := newService(f, fakePlans{billing: true}, launchDay)
	bad.Client.SecretKey = "sk_test_wrong"
	if _, err := bad.Checkout(context.Background(), uuid.New(), "year"); err == nil || strings.Contains(err.Error(), "sk_test_wrong") {
		t.Errorf("a wrong key: %v (must fail without echoing the key)", err)
	}
	if _, err := (&Client{}).Price(context.Background(), "x"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("a client with no key: %v, want ErrNotConfigured", err)
	}
}

func TestAutomaticTaxIsOptIn(t *testing.T) {
	f := newFakeStripe(t)
	svc := newService(f, fakePlans{billing: true}, launchDay)
	svc.AutomaticTax = true
	if _, err := svc.Checkout(context.Background(), uuid.New(), "year"); err != nil {
		t.Fatal(err)
	}
	if got := f.posts[0].Get("automatic_tax[enabled]"); got != "true" {
		t.Errorf("automatic_tax[enabled] = %q with tax on", got)
	}
}

// The pages Stripe sends the buyer back to must exist: the Plan card is /settings#plan (there is no /settings/plan route), and the card reads
// ?checkout=success|cancelled to say "Pro is on" or "no charge was made".
func TestReturnURLsPointAtThePlanCard(t *testing.T) {
	f := newFakeStripe(t)
	svc := newService(f, fakePlans{billing: true}, launchDay)
	if _, err := svc.Checkout(context.Background(), uuid.New(), "year"); err != nil {
		t.Fatal(err)
	}
	form := f.posts[0]
	if got := form.Get("success_url"); got != "https://rivolt.example/settings?checkout=success#plan" {
		t.Errorf("success_url = %q", got)
	}
	if got := form.Get("cancel_url"); got != "https://rivolt.example/settings?checkout=cancelled#plan" {
		t.Errorf("cancel_url = %q", got)
	}
	for _, k := range []string{"success_url", "cancel_url"} {
		if strings.Contains(form.Get(k), "/settings/plan") {
			t.Errorf("%s points at a route that does not exist: %q", k, form.Get(k))
		}
	}
}

// A founding account that subscribes early is not billed while its free founding year runs: billing starts when it ends (trial_end), and the
// consent line names that date. A founding year ending within Stripe's 48-hour minimum is floored like a trial.
func TestCheckoutDuringTheFoundingYear(t *testing.T) {
	f := newFakeStripe(t)
	end := launchDay.AddDate(0, 0, 200)
	svc := newService(f, fakePlans{billing: true, state: entitlements.State{Pro: true, Source: entitlements.SourceFounding}, trial: end}, launchDay)
	if _, err := svc.Checkout(context.Background(), uuid.New(), "year"); err != nil {
		t.Fatalf("a founding account was refused: %v", err)
	}
	form := f.posts[0]
	if got := form.Get("subscription_data[trial_end]"); got != strconv.FormatInt(end.Unix(), 10) {
		t.Errorf("trial_end = %q, want the founding year's end %d", got, end.Unix())
	}
	if got, want := form.Get("custom_text[submit][message]"), "First charge on June 19, 2027: $34.99, then $34.99 every year until you cancel. Cancel any time in Settings."; got != want {
		t.Errorf("consent line:\n got %q\nwant %q", got, want)
	}

	soon := launchDay.Add(30 * time.Hour)
	svc = newService(f, fakePlans{billing: true, state: entitlements.State{Pro: true, Source: entitlements.SourceFounding}, trial: soon}, launchDay)
	if _, err := svc.Checkout(context.Background(), uuid.New(), "month"); err != nil {
		t.Fatal(err)
	}
	form = f.posts[len(f.posts)-1]
	floor := launchDay.Add(MinTrialLead + time.Minute)
	if got := form.Get("subscription_data[trial_end]"); got != strconv.FormatInt(floor.Unix(), 10) {
		t.Errorf("trial_end for a founding year ending in 30 h = %q, want the 48 h floor %d", got, floor.Unix())
	}
}

// Account deletion cancels every live subscription of the customer (pages included), skips the ones already over, never prorates or
// invoices, and treats an unknown customer as nothing to do.
func TestCancelCustomerSubscriptions(t *testing.T) {
	f := newFakeStripe(t)
	f.pageSize = 2
	f.subs = map[string][][2]string{"cus_TEST_A": {
		{"sub_1", "canceled"}, {"sub_2", "active"}, {"sub_3", "trialing"}, {"sub_4", "past_due"}, {"sub_5", "incomplete_expired"}, {"sub_6", "active"},
	}}
	c := &Client{BaseURL: f.srv.URL, SecretKey: testKey}
	n, err := c.CancelCustomerSubscriptions(context.Background(), "cus_TEST_A")
	if err != nil || n != 4 {
		t.Fatalf("cancelled %d, %v; want 4 (active, trialing, past_due, active)", n, err)
	}
	if got := strings.Join(f.deleted, ","); got != "sub_2,sub_3,sub_4,sub_6" {
		t.Errorf("cancelled %s", got)
	}
	for _, q := range f.deleteQueries {
		if q.Get("invoice_now") != "false" || q.Get("prorate") != "false" {
			t.Errorf("a cancel asked for an invoice or proration: %v", q)
		}
	}
	if f.listGets < 3 {
		t.Errorf("listed %d pages, want the three the data needs", f.listGets)
	}
	// An unknown customer (already deleted at Stripe) is nothing to cancel, not an error.
	if n, err := c.CancelCustomerSubscriptions(context.Background(), "cus_TEST_gone"); err != nil || n != 0 {
		t.Errorf("an unknown customer: %d, %v", n, err)
	}
	// Stripe failing is an error (the caller queues a retry).
	f.fail = true
	if _, err := c.CancelCustomerSubscriptions(context.Background(), "cus_TEST_A"); err == nil {
		t.Error("a failing Stripe was reported as success")
	}
}

// A Stripe refusal reads as its status and error code, never its free-text message (which can echo an email, a name or a key).
func TestStripeErrorTextCarriesNoMessage(t *testing.T) {
	e := &Error{Status: 500, Code: "internal", Message: "stripe says: sk_test_unit_key and someone@example.com"}
	if got := e.Error(); got != "stripe: 500 internal" {
		t.Errorf("Error() = %q", got)
	}
	for _, code := range []string{"", "has space", "Has-Caps", strings.Repeat("a", 100), "x\ny"} {
		if got := (&Error{Status: 400, Code: code, Message: "m"}).Error(); got != "stripe: 400 error" {
			t.Errorf("code %q -> %q, want the generic one", code, got)
		}
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{e, "stripe: 500 internal"},
		{ErrNotConfigured, ErrNotConfigured.Error()},
		{context.DeadlineExceeded, "stripe: timed out"},
		{errors.New("Post https://x: dial tcp someone@example.com"), "stripe: request failed"},
		{nil, ""},
	} {
		if got := queueText(c.err); got != c.want {
			t.Errorf("queueText(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// The Stripe customer record (name, email, address) is deleted once nothing is open on it; an unpaid invoice or an unresolved dispute holds
// it back (ErrOpenItems, nothing deleted); a settled dispute does not; an unknown customer is already done.
func TestDeleteCustomerIfClear(t *testing.T) {
	ctx := context.Background()
	c := func(f *fakeStripe) *Client { return &Client{BaseURL: f.srv.URL, SecretKey: testKey} }

	f := newFakeStripe(t)
	f.subs = map[string][][2]string{"cus_clear": nil}
	if err := c(f).DeleteCustomerIfClear(ctx, "cus_clear"); err != nil || strings.Join(f.customersDeleted, ",") != "cus_clear" {
		t.Errorf("a customer with nothing open: %v, deleted %v", err, f.customersDeleted)
	}
	// Again: already gone (404) is success, not a second delete.
	if err := c(f).DeleteCustomerIfClear(ctx, "cus_clear"); err != nil || len(f.customersDeleted) != 1 {
		t.Errorf("a customer already deleted: %v, deleted %v", err, f.customersDeleted)
	}

	f = newFakeStripe(t)
	f.openInvoices = map[string]int{"cus_unpaid": 1}
	if err := c(f).DeleteCustomerIfClear(ctx, "cus_unpaid"); !errors.Is(err, ErrOpenItems) || len(f.customersDeleted) != 0 {
		t.Errorf("an unpaid invoice: %v, deleted %v; want ErrOpenItems and nothing deleted", err, f.customersDeleted)
	}

	f = newFakeStripe(t)
	f.charges = map[string][][2]string{"cus_dispute": {{"ch_1", ""}, {"ch_2", "needs_response"}}}
	if err := c(f).DeleteCustomerIfClear(ctx, "cus_dispute"); !errors.Is(err, ErrOpenItems) || len(f.customersDeleted) != 0 {
		t.Errorf("an unresolved dispute: %v, deleted %v; want ErrOpenItems", err, f.customersDeleted)
	}
	for _, status := range []string{"under_review", "warning_needs_response", "warning_under_review"} {
		f = newFakeStripe(t)
		f.charges = map[string][][2]string{"cus_d": {{"ch_9", status}}}
		if err := c(f).DeleteCustomerIfClear(ctx, "cus_d"); !errors.Is(err, ErrOpenItems) {
			t.Errorf("a dispute %s: %v, want ErrOpenItems", status, err)
		}
	}
	for _, status := range []string{"won", "lost", "warning_closed", "charge_refunded"} {
		f = newFakeStripe(t)
		f.charges = map[string][][2]string{"cus_d": {{"ch_9", status}}}
		if err := c(f).DeleteCustomerIfClear(ctx, "cus_d"); err != nil || len(f.customersDeleted) != 1 {
			t.Errorf("a settled dispute (%s): %v, deleted %v; want the customer deleted", status, err, f.customersDeleted)
		}
	}

	// CloseCustomer cancels first, then deletes.
	f = newFakeStripe(t)
	f.subs = map[string][][2]string{"cus_both": {{"sub_x", "active"}}}
	if err := c(f).CloseCustomer(ctx, "cus_both"); err != nil || strings.Join(f.deleted, ",") != "sub_x" || strings.Join(f.customersDeleted, ",") != "cus_both" {
		t.Errorf("CloseCustomer: %v, cancelled %v, deleted %v", err, f.deleted, f.customersDeleted)
	}
}

// The free-period explainer sits beside the pay button only when trial_end is set, names the same date as the consent line, and says
// "founding year" only for a founding account.
func TestCheckoutFreePeriodExplainer(t *testing.T) {
	end := launchDay.Add(10 * 24 * time.Hour)
	for source, what := range map[string]string{entitlements.SourceFounding: "(your founding year)", entitlements.SourceGrant: "(your free period)"} {
		f := newFakeStripe(t)
		svc := newService(f, fakePlans{billing: true, trial: end, state: entitlements.State{Pro: true, Source: source}}, launchDay)
		if _, err := svc.Checkout(context.Background(), uuid.New(), "year"); err != nil {
			t.Fatal(err)
		}
		form := f.posts[0]
		got := form.Get("custom_text[after_submit][message]")
		want := "Your Rivolt Pro is already free until December 11, 2026 " + what + ". Stripe calls this free period a trial: nothing is charged before December 11, 2026, then $34.99 renews every year until you cancel."
		if got != want {
			t.Errorf("%s: explainer = %q, want %q", source, got, want)
		}
		if len(got) > 1200 {
			t.Errorf("%s: explainer is %d characters, Stripe's limit is 1200", source, len(got))
		}
		if !strings.Contains(form.Get("custom_text[submit][message]"), "December 11, 2026") {
			t.Errorf("%s: the consent line names another date: %q", source, form.Get("custom_text[submit][message]"))
		}
	}
	// A free account has no trial_end and no explainer (TestBuildCheckoutForANewAccount pins the absence too).
	f := newFakeStripe(t)
	svc := newService(f, fakePlans{billing: true}, launchDay)
	if _, err := svc.Checkout(context.Background(), uuid.New(), "month"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"subscription_data[trial_end]", "custom_text[after_submit][message]"} {
		if _, ok := f.posts[0][k]; ok {
			t.Errorf("a free account's checkout sets %s", k)
		}
	}
}

// Every Checkout session names the payment method configuration (cards, Apple Pay, Google Pay), which is how it is held to those
// methods: Stripe no longer accepts payment_method_types on a Checkout Session (the preview rejected it with a 502). The form carries the
// configuration, the billing address requirement, and never a payment_method_types key.
func TestCheckoutUsesThePaymentMethodConfiguration(t *testing.T) {
	for name, plans := range map[string]fakePlans{
		"a new account":      {billing: true},
		"during a free year": {billing: true, trial: launchDay.Add(10 * 24 * time.Hour), state: entitlements.State{Pro: true, Source: entitlements.SourceFounding}},
	} {
		f := newFakeStripe(t)
		svc := newService(f, plans, launchDay)
		if _, err := svc.Checkout(context.Background(), uuid.New(), "year"); err != nil {
			t.Fatal(name, err)
		}
		form := f.posts[0]
		if got := form.Get("payment_method_configuration"); got != testPMC {
			t.Errorf("%s: payment_method_configuration = %q, want %q", name, got, testPMC)
		}
		if got := form.Get("billing_address_collection"); got != "required" {
			t.Errorf("%s: billing_address_collection = %q, want required (the webhook's US-only check needs the country)", name, got)
		}
		for k := range form {
			if strings.HasPrefix(k, "payment_method_types") {
				t.Errorf("%s: the form carries %q, which Stripe no longer accepts on a Checkout Session", name, k)
			}
		}
	}
}

// Without a configuration (billing on) Checkout is refused, not created on the Dashboard's default methods: nothing reaches Stripe.
func TestCheckoutRefusesWithoutAPaymentMethodConfiguration(t *testing.T) {
	for _, bad := range []string{"", " ", "card", "pmc_", "pmc_ab", "cs_test_123456", "pmc_1Valid; DROP"} {
		f := newFakeStripe(t)
		svc := newService(f, fakePlans{billing: true}, launchDay)
		svc.PaymentMethodConfig = bad
		if _, err := svc.Checkout(context.Background(), uuid.New(), "year"); !errors.Is(err, ErrNoPaymentConfig) {
			t.Errorf("configuration %q: err = %v, want ErrNoPaymentConfig", bad, err)
		}
		if len(f.posts) != 0 {
			t.Errorf("configuration %q: %d requests reached Stripe", bad, len(f.posts))
		}
	}
}

// The lesson of the preview 502: a form test passed while Stripe rejected the parameter. Nothing in this package may send payment_method_types
// again (a source scan, so a refactor that reintroduces it fails here even if its form test is rewritten).
func TestNoPaymentMethodTypesInTheSource(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(b), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if strings.Contains(code, "payment_method_types") {
				t.Errorf("%s:%d sends payment_method_types: %s", e.Name(), n+1, strings.TrimSpace(line))
			}
		}
	}
}
