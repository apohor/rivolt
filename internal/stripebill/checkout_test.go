package stripebill

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
func (p fakePlans) TrialEnd(context.Context, uuid.UUID) (time.Time, bool, error) {
	return p.trial, !p.trial.IsZero(), nil
}

func newService(f *fakeStripe, plans fakePlans, now time.Time) *Service {
	return &Service{
		Client:  &Client{BaseURL: f.srv.URL, SecretKey: testKey},
		Plans:   plans,
		Prices:  map[string]string{"month": priceMonth, "year": priceYear},
		BaseURL: "https://rivolt.example",
		Now:     func() time.Time { return now },
	}
}

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
		"success_url":                          "https://rivolt.example/settings/plan?checkout=success",
		"cancel_url":                           "https://rivolt.example/settings/plan?checkout=cancelled",
		"consent_collection[terms_of_service]": "required",
		"custom_text[submit][message]":         "First charge today: $34.99, then $34.99 every year until you cancel. Cancel any time in Settings.",
	} {
		if form.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, form.Get(k), want)
		}
	}
	for _, absent := range []string{"subscription_data[trial_end]", "customer", "automatic_tax[enabled]", "customer_email", "customer_update[address]"} {
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
