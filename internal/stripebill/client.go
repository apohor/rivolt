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

// Error is a refusal from Stripe. Message is Stripe's own text; it is kept off every log line and queue row (see Error) and never shown to the user.
type Error struct {
	Status  int
	Code    string
	Message string
}

// Error is the status and Stripe's error code only. Stripe's free-text message can echo what was sent (an email, a name, a key), so it is not
// part of the text that goes to logs or into the retry queue; it stays in the Message field for a debugger.
func (e *Error) Error() string { return fmt.Sprintf("stripe: %d %s", e.Status, safeCode(e.Code)) }

// safeCode keeps a Stripe error code to the characters Stripe uses (lower-case letters, digits, underscore), so nothing else can ride in it.
func safeCode(c string) string {
	if c == "" || len(c) > 64 {
		return "error"
	}
	for _, r := range c {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return "error"
		}
	}
	return c
}

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

// CancelCustomerSubscriptions cancels, at once and without proration or an invoice, every subscription of a customer that is not already
// over, and returns how many it cancelled. A customer Stripe does not know (404) has nothing to cancel. Idempotent: a subscription
// cancelled by an earlier attempt is no longer listed as live. Used when an account is deleted: a deleted account is never charged again.
func (c *Client) CancelCustomerSubscriptions(ctx context.Context, customer string) (int, error) {
	cancelled := 0
	after := ""
	for page := 0; page < 20; page++ {
		path := "/v1/subscriptions?limit=100&status=all&customer=" + url.QueryEscape(customer)
		if after != "" {
			path += "&starting_after=" + url.QueryEscape(after)
		}
		var list struct {
			Data []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"data"`
			HasMore bool `json:"has_more"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, "", &list); err != nil {
			var se *Error
			if errors.As(err, &se) && se.Status == http.StatusNotFound {
				return cancelled, nil
			}
			return cancelled, err
		}
		for _, s := range list.Data {
			after = s.ID
			if s.Status == "canceled" || s.Status == "incomplete_expired" {
				continue
			}
			form := url.Values{"invoice_now": {"false"}, "prorate": {"false"}}
			if err := c.do(ctx, http.MethodDelete, "/v1/subscriptions/"+url.PathEscape(s.ID)+"?"+form.Encode(), nil, "", nil); err != nil {
				var se *Error
				if errors.As(err, &se) && se.Status == http.StatusNotFound {
					continue // gone between the list and the cancel
				}
				return cancelled, err
			}
			cancelled++
		}
		if !list.HasMore {
			return cancelled, nil
		}
	}
	return cancelled, errors.New("stripe: more than 2000 subscriptions on one customer")
}
