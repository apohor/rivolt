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

func TestCardInCheckout(t *testing.T) {
	for body, want := range map[string]bool{
		`{"data":[{"is_default":false,"card":{"available":false}},{"is_default":true,"card":{"available":true,"display_preference":{"value":"on"}}}]}`: true,
		`{"data":[{"is_default":true,"card":{"available":true,"display_preference":{"value":"off"}}}]}`:                                                false,
		`{"data":[{"is_default":true,"card":{"available":false,"display_preference":{"value":"on"}}}]}`:                                                false,
		`{"data":[{"is_default":false,"card":{"available":true}}]}`:                                                                                    false,
	} {
		c := factsServer(t, `{}`, body)
		if got, err := c.CardInCheckout(context.Background()); err != nil || got != want {
			t.Errorf("CardInCheckout(%s) = %v %v, want %v", body, got, err, want)
		}
	}
}
