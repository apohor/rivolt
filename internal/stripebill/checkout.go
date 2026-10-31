package stripebill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/entitlements"
)

// Errors the handlers turn into answers.
var (
	ErrBadInterval       = errors.New("stripe: interval must be month or year")
	ErrBillingOff        = errors.New("stripe: billing is not enabled on this server")
	ErrAlreadySubscribed = errors.New("stripe: the account already has a paid subscription")
	ErrNoCustomer        = errors.New("stripe: the account has no Stripe customer")
)

// MinTrialLead is Stripe's floor for a Checkout subscription's trial_end: at least 48 hours ahead.
const MinTrialLead = 48 * time.Hour

// Plans reads an account's plan (internal/entitlements.Service).
type Plans interface {
	Billing() bool
	State(ctx context.Context, uid uuid.UUID) (entitlements.State, error)
	FreeUntil(ctx context.Context, uid uuid.UUID) (time.Time, bool, error)
}

// Service creates Checkout and Customer Portal sessions. The account is always the signed-in one, passed by the handler from the session:
// nothing a request carries names an account. Card data never touches Rivolt: Stripe's hosted pages take it.
type Service struct {
	Client  *Client
	DB      *sql.DB
	Plans   Plans
	Prices  map[string]string // interval ("month", "year") -> Stripe price id
	BaseURL string            // the public site, for the return URLs
	// AutomaticTax turns on Stripe Tax for the session. Off until the Stripe account has Tax set up (an owner step): Checkout refuses a
	// session asking for it otherwise.
	AutomaticTax bool
	Now          func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ConsentLine is the sentence shown beside the pay button, built from the session's own inputs, so it is true by construction: what will be
// charged, when the first charge is, and how it renews. firstCharge is the trial's end when the account is mid-trial (Stripe starts
// billing then), otherwise now.
func ConsentLine(p Price, firstCharge, now time.Time) string {
	amount := fmt.Sprintf("%s%d.%02d", currencySymbol(p.Currency), p.UnitAmount/100, p.UnitAmount%100)
	every := map[string]string{"month": "month", "year": "year"}[p.Interval]
	when := "today"
	if firstCharge.UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		when = "on " + firstCharge.UTC().Format("January 2, 2006")
	}
	return fmt.Sprintf("First charge %s: %s, then %s every %s until you cancel. Cancel any time in Settings.", when, amount, amount, every)
}

func currencySymbol(c string) string {
	if sym, ok := map[string]string{"usd": "$", "eur": "€", "gbp": "£"}[c]; ok {
		return sym
	}
	return strings.ToUpper(c) + " "
}

// CheckoutParams are the inputs of one session, exposed so a test can see exactly what is sent to Stripe.
type CheckoutParams struct {
	Form        url.Values
	FirstCharge time.Time
	Price       Price
}

// BuildCheckout builds the Checkout Session for uid and interval. It refuses an account that already has a paid subscription (a second one
// would charge twice), and starts billing when a running trial ends (never earlier than Stripe's 48 hours).
func (s *Service) BuildCheckout(ctx context.Context, uid uuid.UUID, interval string) (CheckoutParams, error) {
	var out CheckoutParams
	priceID := s.Prices[interval]
	if (interval != "month" && interval != "year") || priceID == "" {
		return out, ErrBadInterval
	}
	if s.Plans == nil || !s.Plans.Billing() {
		return out, ErrBillingOff
	}
	st, err := s.Plans.State(ctx, uid)
	if err != nil {
		return out, err
	}
	if st.Pro && (st.Source == entitlements.SourceStripe || st.Source == entitlements.SourceApple) {
		return out, ErrAlreadySubscribed
	}
	price, err := s.Client.Price(ctx, priceID)
	if err != nil {
		return out, err
	}
	now := s.now()
	first := now
	var trialEnd time.Time
	// Billing starts when the account's free Pro (a running trial, or the founding year) ends, never earlier: buying early must not bill
	// twice for the same months.
	if end, ok, err := s.Plans.FreeUntil(ctx, uid); err != nil {
		return out, err
	} else if ok {
		trialEnd = end
		if min := now.Add(MinTrialLead + time.Minute); trialEnd.Before(min) {
			trialEnd = min
		}
		first = trialEnd
	}
	uidStr := uid.String()
	form := url.Values{
		"mode":                    {"subscription"},
		"line_items[0][price]":    {priceID},
		"line_items[0][quantity]": {"1"},
		"client_reference_id":     {uidStr},
		"subscription_data[metadata][" + MetadataUserID + "]": {uidStr},
		"success_url":                          {s.BaseURL + "/settings?checkout=success#plan"},
		"cancel_url":                           {s.BaseURL + "/settings?checkout=cancelled#plan"},
		"consent_collection[terms_of_service]": {"required"},
		"custom_text[submit][message]":         {ConsentLine(price, first, now)},
	}
	if !trialEnd.IsZero() {
		form.Set("subscription_data[trial_end]", strconv.FormatInt(trialEnd.Unix(), 10))
	}
	if s.AutomaticTax {
		form.Set("automatic_tax[enabled]", "true")
	}
	// A returning customer keeps their Stripe customer; a new one is created by Checkout and linked by the checkout.session.completed event.
	if cust, err := s.customerOf(ctx, uid); err != nil {
		return out, err
	} else if cust != "" {
		form.Set("customer", cust)
		if s.AutomaticTax {
			form.Set("customer_update[address]", "auto")
		}
	}
	return CheckoutParams{Form: form, FirstCharge: first, Price: price}, nil
}

// Checkout creates the session and returns the hosted page's URL.
func (s *Service) Checkout(ctx context.Context, uid uuid.UUID, interval string) (string, error) {
	p, err := s.BuildCheckout(ctx, uid, interval)
	if err != nil {
		return "", err
	}
	// One session per account, interval and minute: a double click gets the same page, not two.
	idem := fmt.Sprintf("rivolt-checkout-%s-%s-%d", uid, interval, s.now().Unix()/60)
	return s.Client.CheckoutSessionURL(ctx, p.Form, idem)
}

// Portal creates a Customer Portal session for the account's Stripe customer.
func (s *Service) Portal(ctx context.Context, uid uuid.UUID) (string, error) {
	if s.Plans == nil || !s.Plans.Billing() {
		return "", ErrBillingOff
	}
	cust, err := s.customerOf(ctx, uid)
	if err != nil {
		return "", err
	}
	if cust == "" {
		return "", ErrNoCustomer
	}
	return s.Client.PortalSessionURL(ctx, cust, s.BaseURL+"/settings#plan")
}

func (s *Service) customerOf(ctx context.Context, uid uuid.UUID) (string, error) {
	if s.DB == nil {
		return "", nil
	}
	var c string
	err := s.DB.QueryRowContext(ctx, `SELECT stripe_customer_id FROM billing_customers WHERE user_id = $1`, uid).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return c, err
}
