package stripebill

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func factsServer(t *testing.T, portal, price string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s: the pre-flight reads only", r.Method, r.URL.Path)
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/billing_portal/configurations"):
			_, _ = w.Write([]byte(portal))
		case strings.HasPrefix(r.URL.Path, "/v1/customers/"):
			_, _ = w.Write([]byte(price))
		case r.URL.Path == "/v1/tax/settings":
			_, _ = w.Write([]byte(price))
		case strings.HasPrefix(r.URL.Path, "/v1/payment_method_configurations"):
			_, _ = w.Write([]byte(price))
		case strings.HasPrefix(r.URL.Path, "/v1/prices/"):
			_, _ = w.Write([]byte(price))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{SecretKey: testKey, BaseURL: srv.URL}
}

// The portal's Terms and Privacy URLs and its payment-method setting are read as booleans; no URL is kept.
func TestPortalFacts(t *testing.T) {
	c := factsServer(t, `{"data":[{"business_profile":{"terms_of_service_url":"https://x.test/t","privacy_policy_url":""},"features":{"payment_method_update":{"enabled":true}}}]}`, `{}`)
	got, err := c.PortalFacts(context.Background())
	if err != nil || !got.Found || !got.TermsSet || got.PrivacySet || !got.PaymentMethodUpdate {
		t.Errorf("PortalFacts = %+v, %v; want found, terms set, privacy unset, payment method update on", got, err)
	}
	none := factsServer(t, `{"data":[]}`, `{}`)
	if got, err := none.PortalFacts(context.Background()); err != nil || got.Found {
		t.Errorf("no configuration: %+v, %v", got, err)
	}
}

func TestPriceMode(t *testing.T) {
	c := factsServer(t, `{}`, `{"livemode":true,"active":false}`)
	live, active, err := c.PriceMode(context.Background(), "price_x")
	if err != nil || !live || active {
		t.Errorf("PriceMode = %v %v %v, want live and inactive", live, active, err)
	}
}

func TestEnabledCheckoutMethods(t *testing.T) {
	c := factsServer(t, `{}`, `{"data":[
	  {"id":"pmc_other","is_default":false,"card":{"available":true,"display_preference":{"value":"on"}},"klarna":{"available":true,"display_preference":{"value":"on"}}},
	  {"id":"pmc_default","object":"payment_method_configuration","is_default":true,"name":"Default","livemode":false,
	   "card":{"available":true,"display_preference":{"value":"on"}},
	   "apple_pay":{"available":true,"display_preference":{"value":"on"}},
	   "klarna":{"available":true,"display_preference":{"value":"off"}},
	   "cashapp":{"available":false,"display_preference":{"value":"on"}},
	   "us_bank_account":{"available":true,"display_preference":{"value":"on"}}}]}`)
	got, found, err := c.EnabledCheckoutMethods(context.Background())
	if err != nil || !found || strings.Join(got, ",") != "apple_pay,card,us_bank_account" {
		t.Errorf("EnabledCheckoutMethods = %v %v %v, want the default configuration's enabled types: apple_pay,card,us_bank_account", got, found, err)
	}
	none := factsServer(t, `{}`, `{"data":[{"id":"x","is_default":false,"card":{"available":true,"display_preference":{"value":"on"}}}]}`)
	if got, found, err := none.EnabledCheckoutMethods(context.Background()); err != nil || found || len(got) != 0 {
		t.Errorf("no default configuration: %v %v %v", got, found, err)
	}
}

func TestTaxActive(t *testing.T) {
	for body, want := range map[string]bool{`{"status":"active"}`: true, `{"status":"pending"}`: false, `{}`: false} {
		c := factsServer(t, `{}`, body)
		if got, err := c.TaxActive(context.Background()); err != nil || got != want {
			t.Errorf("TaxActive(%s) = %v %v, want %v", body, got, err, want)
		}
	}
}

func TestCustomerCountry(t *testing.T) {
	for body, want := range map[string]string{`{"address":{"country":"de"}}`: "DE", `{"address":null}`: "", `{}`: ""} {
		c := factsServer(t, `{}`, body)
		if got, err := c.CustomerCountry(context.Background(), "cus_x"); err != nil || got != want {
			t.Errorf("CustomerCountry(%s) = %q %v, want %q", body, got, err, want)
		}
	}
}
