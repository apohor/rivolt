package stripebill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultAPIBase is Stripe's REST API. Tests point Client.BaseURL at a local fake.
const DefaultAPIBase = "https://api.stripe.com"

// Client is the little of Stripe's REST API Rivolt calls: Checkout Sessions, Customer Portal sessions and Prices. Plain net/http with
// form-encoded bodies, the way Stripe documents it, so the whole thing is testable against a fake server. The secret key is sent only to
// BaseURL, as a Bearer token, and is never logged or returned in an error.
type Client struct {
	BaseURL   string // default DefaultAPIBase
	SecretKey string
	HTTP      *http.Client // default: 10 s timeout

	mu     sync.Mutex
	prices map[string]Price
}

// Price is the part of a Stripe Price Rivolt shows in the consent line.
type Price struct {
	ID         string
	UnitAmount int64  // minor units (cents)
	Currency   string // lower case
	Interval   string // month or year
}

// Error is a refusal from Stripe. Message is Stripe's own text for the operator's log; it is not shown to the user.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("stripe: %d %s: %s", e.Status, e.Code, e.Message) }

// ErrNotConfigured is a client with no secret key.
var ErrNotConfigured = errors.New("stripe: no secret key configured")

func (c *Client) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultAPIBase
}

// do sends one request. form is the url-encoded body of a POST (nil for GET); idem, when set, is the Idempotency-Key.
func (c *Client) do(ctx context.Context, method, path string, form url.Values, idem string, out any) error {
	if c == nil || c.SecretKey == "" {
		return ErrNotConfigured
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.SecretKey)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	resp, err := c.httpc().Do(req)
	if err != nil {
		// The request URL is not part of the message (a *url.Error carries it); the key never is.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("stripe: request failed: %w", ue.Err)
		}
		return errors.New("stripe: request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		code := e.Error.Code
		if code == "" {
			code = e.Error.Type
		}
		return &Error{Status: resp.StatusCode, Code: code, Message: e.Error.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// CheckoutSessionURL creates a Checkout Session and returns the hosted page's URL.
func (c *Client) CheckoutSessionURL(ctx context.Context, form url.Values, idem string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/checkout/sessions", form, idem, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("stripe: the Checkout Session has no url")
	}
	return out.URL, nil
}

// PortalSessionURL creates a Customer Portal session for a customer and returns its URL.
func (c *Client) PortalSessionURL(ctx context.Context, customer, returnURL string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	form := url.Values{"customer": {customer}, "return_url": {returnURL}}
	if err := c.do(ctx, http.MethodPost, "/v1/billing_portal/sessions", form, "", &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("stripe: the portal session has no url")
	}
	return out.URL, nil
}

// Price reads a Price (cached for the life of the process: a price's amount and interval do not change; a new price is a new id).
func (c *Client) Price(ctx context.Context, id string) (Price, error) {
	c.mu.Lock()
	if p, ok := c.prices[id]; ok {
		c.mu.Unlock()
		return p, nil
	}
	c.mu.Unlock()
	var out struct {
		ID         string `json:"id"`
		UnitAmount int64  `json:"unit_amount"`
		Currency   string `json:"currency"`
		Recurring  struct {
			Interval string `json:"interval"`
		} `json:"recurring"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/prices/"+url.PathEscape(id), nil, "", &out); err != nil {
		return Price{}, err
	}
	p := Price{ID: out.ID, UnitAmount: out.UnitAmount, Currency: strings.ToLower(out.Currency), Interval: out.Recurring.Interval}
	if p.ID == "" || p.UnitAmount <= 0 || p.Interval == "" {
		return Price{}, fmt.Errorf("stripe: price %s is not a recurring price with an amount", id)
	}
	c.mu.Lock()
	if c.prices == nil {
		c.prices = map[string]Price{}
	}
	c.prices[id] = p
	c.mu.Unlock()
	return p, nil
}
