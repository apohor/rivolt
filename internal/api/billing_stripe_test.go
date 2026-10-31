package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/stripebill"
)

type billingPlans struct {
	on    bool
	state entitlements.State
}

func (p billingPlans) Billing() bool { return p.on }
func (p billingPlans) State(context.Context, uuid.UUID) (entitlements.State, error) {
	return p.state, nil
}
func (p billingPlans) FreeUntil(context.Context, uuid.UUID) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

type stripeRecorder struct {
	mu    sync.Mutex
	forms []map[string]string
	fail  bool
}

func newStripeRecorder(t *testing.T) (*stripeRecorder, *httptest.Server) {
	rec := &stripeRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if rec.fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"secret internal detail"}}`))
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/prices/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "price_x", "unit_amount": 3499, "currency": "usd", "recurring": map[string]any{"interval": "year"}})
		case r.URL.Path == "/v1/checkout/sessions":
			_ = r.ParseForm()
			f := map[string]string{}
			for k := range r.PostForm {
				f[k] = r.PostForm.Get(k)
			}
			rec.forms = append(rec.forms, f)
			_ = json.NewEncoder(w).Encode(map[string]any{"url": "https://checkout.stripe.test/pay"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return rec, srv
}

func billingSvc(srv *httptest.Server, plans billingPlans) *stripebill.Service {
	return &stripebill.Service{
		Client: &stripebill.Client{BaseURL: srv.URL, SecretKey: "sk_test_x"}, Plans: plans,
		Prices: map[string]string{"month": "price_x", "year": "price_x"}, BaseURL: "https://rivolt.example",
	}
}

func postBilling(h func(uuid.UUID, http.ResponseWriter, *http.Request), uid uuid.UUID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/billing/checkout", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(uid, rec, req)
	return rec
}

// The account is the signed-in one. A body that names another account (or carries any field but the interval) changes nothing: Stripe is
// asked for a session for the caller.
func TestCheckoutAccountComesFromTheSessionOnly(t *testing.T) {
	stripe, srv := newStripeRecorder(t)
	h := handleBillingCheckout(billingSvc(srv, billingPlans{on: true}))
	me, other := uuid.New(), uuid.New()
	rec := postBilling(h, me, `{"interval":"year","user_id":"`+other.String()+`","client_reference_id":"`+other.String()+`","customer":"cus_victim","rivolt_user_id":"`+other.String()+`"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "checkout.stripe.test") {
		t.Fatalf("checkout: %d %s", rec.Code, rec.Body)
	}
	f := stripe.forms[0]
	if f["client_reference_id"] != me.String() || f["subscription_data[metadata][rivolt_user_id]"] != me.String() {
		t.Errorf("Stripe was asked for %q / %q, want the signed-in %s", f["client_reference_id"], f["subscription_data[metadata][rivolt_user_id]"], me)
	}
	if _, ok := f["customer"]; ok {
		t.Error("a customer named in the request body reached Stripe")
	}
	for k, v := range f {
		if strings.Contains(v, other.String()) || strings.Contains(v, "cus_victim") {
			t.Errorf("another account's id reached Stripe in %s=%q", k, v)
		}
	}
}

func TestCheckoutAnswers(t *testing.T) {
	stripe, srv := newStripeRecorder(t)
	_ = stripe
	uid := uuid.New()
	for _, c := range []struct {
		name  string
		plans billingPlans
		body  string
		fail  bool
		code  int
		err   string
	}{
		{"a bad interval", billingPlans{on: true}, `{"interval":"week"}`, false, 400, "bad_interval"},
		{"a body that is not JSON", billingPlans{on: true}, `nope`, false, 400, "bad_request"},
		{"billing off", billingPlans{}, `{"interval":"year"}`, false, 409, "billing_disabled"},
		{"already subscribed", billingPlans{on: true, state: entitlements.State{Pro: true, Source: entitlements.SourceStripe}}, `{"interval":"year"}`, false, 409, "already_subscribed"},
		{"Stripe down", billingPlans{on: true}, `{"interval":"year"}`, true, 502, "billing_unavailable"},
	} {
		stripe.mu.Lock()
		stripe.fail = c.fail
		stripe.mu.Unlock()
		// A fresh service each time: the price cache would otherwise hide the failing price read.
		rec := postBilling(handleBillingCheckout(billingSvc(srv, c.plans)), uid, c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"`+c.err+`"`) {
			t.Errorf("%s: %d %s, want %d %s", c.name, rec.Code, rec.Body, c.code, c.err)
		}
		if strings.Contains(rec.Body.String(), "secret internal detail") {
			t.Errorf("%s: Stripe's message reached the user: %s", c.name, rec.Body)
		}
	}
}

func TestPortalWithoutACustomerIsRefused(t *testing.T) {
	_, srv := newStripeRecorder(t)
	rec := httptest.NewRecorder()
	handleBillingPortal(billingSvc(srv, billingPlans{on: true}))(uuid.New(), rec, httptest.NewRequest(http.MethodPost, "/api/billing/portal", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_billing_account") {
		t.Errorf("portal with no customer: %d %s", rec.Code, rec.Body)
	}
}

// The iPhone app sells through StoreKit and may not open a web checkout or portal: its sessions cannot write to these routes.
func TestAppSessionsCannotUseWebBilling(t *testing.T) {
	for _, p := range []string{"/api/billing/checkout", "/api/billing/portal", "/api/billing/stripe/webhook"} {
		if appWriteAllowed(http.MethodPost, p) {
			t.Errorf("an app session may POST %s", p)
		}
	}
}

type stepStub struct {
	err   error
	calls *[]string
	name  string
}

func (s stepStub) BeforeDelete(context.Context, uuid.UUID) error {
	*s.calls = append(*s.calls, s.name)
	return s.err
}

// The pre-deletion steps (Apple token, Stripe subscriptions) run in order, a nil one is skipped, and the first failure stops the rest and
// is returned: the caller then does not delete the account.
func TestPreDeletersRunInOrderAndStopAtTheFirstError(t *testing.T) {
	var calls []string
	boom := errors.New("stripe down and the queue too")
	steps := preDeleters{nil, stepStub{nil, &calls, "apple"}, stepStub{boom, &calls, "stripe"}, stepStub{nil, &calls, "after"}}
	if err := steps.BeforeDelete(context.Background(), uuid.New()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the stripe step's error", err)
	}
	if strings.Join(calls, ",") != "apple,stripe" {
		t.Errorf("ran %v, want apple then stripe and nothing after the failure", calls)
	}
	if err := (preDeleters{}).BeforeDelete(context.Background(), uuid.New()); err != nil {
		t.Errorf("no steps: %v", err)
	}
	// A server with neither configured has no steps.
	if got := (Deps{}).preDeletes(); len(got) != 0 {
		t.Errorf("an empty Deps has %d pre-deletion steps", len(got))
	}
	if got := (Deps{StripeClosure: &stripebill.AccountClosure{}}).preDeletes(); len(got) != 1 {
		t.Errorf("Deps with a Stripe closure has %d steps, want 1", len(got))
	}
}
