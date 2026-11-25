package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/stripebill"
)

// handleBillingCheckout is POST /api/billing/checkout {"interval":"month"|"year"} -> {"url": the Stripe-hosted Checkout page}. The account
// is the signed-in one and nothing in the request names another: the body carries the interval and "from" (the entry point, for a count only) and nothing else is read from it.
func handleBillingCheckout(svc *stripebill.Service, m *metrics.Metrics) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		var in struct {
			Interval string `json:"interval"`
			From     string `json:"from"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
			return
		}
		url, err := svc.Checkout(r.Context(), uid, in.Interval)
		if !billingAnswer(w, r, err, "checkout") {
			return
		}
		if m != nil {
			// Counted when a URL is returned; an unknown "from" is "other", so the label set stays closed.
			m.BillingCheckoutsStarted.WithLabelValues(in.Interval, metrics.ProFrom(in.From)).Inc()
		}
		writeJSON(w, http.StatusOK, map[string]any{"url": url})
	}
}

// handleBillingPortal is POST /api/billing/portal -> {"url": the Stripe-hosted Customer Portal}.
func handleBillingPortal(svc *stripebill.Service) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		url, err := svc.Portal(r.Context(), uid)
		if !billingAnswer(w, r, err, "portal") {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"url": url})
	}
}

// handleBillingRefundOffer is GET /api/billing/refund -> whether the signed-in account can refund itself now:
// {"eligible":true,"until":"<RFC 3339>","amount_cents":N,"currency":"usd"}, or {"eligible":false,"reason":"no_billing_account|no_payment|window_closed|used"}.
func handleBillingRefundOffer(svc *stripebill.Service) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		o, err := svc.RefundOffer(r.Context(), uid)
		if !billingAnswer(w, r, err, "refund offer") {
			return
		}
		if !o.Eligible {
			writeJSON(w, http.StatusOK, map[string]any{"eligible": false, "reason": o.Reason})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"eligible": true, "until": o.Until.Format(time.RFC3339), "amount_cents": o.AmountCents, "currency": o.Currency})
	}
}

// handleBillingRefund is POST /api/billing/refund: refunds the account's newest payment in full (within 14 days of it, once per account),
// ends Pro at once. The account is the signed-in one; the request carries nothing. 200 {"refunded":true,"amount_cents":N,"currency":"usd"}, or
// 409 {"error":"refund_unavailable","reason":...} when none is on offer.
func handleBillingRefund(svc *stripebill.Service) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		o, err := svc.SelfRefund(r.Context(), uid)
		if errors.Is(err, stripebill.ErrRefundUnavailable) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "refund_unavailable", "reason": o.Reason})
			return
		}
		if !billingAnswer(w, r, err, "refund") {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"refunded": true, "amount_cents": o.AmountCents, "currency": o.Currency})
	}
}

// billingAnswer writes the answer for an error and reports whether the call succeeded. The codes are the contract with the clients;
// Stripe's own message goes to the log, never to the user.
func billingAnswer(w http.ResponseWriter, r *http.Request, err error, what string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, stripebill.ErrBadInterval):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_interval"})
	case errors.Is(err, stripebill.ErrNoPaymentConfig):
		// A deployment problem, not the user's: logged with its cause, answered with a code the clients read as "try later".
		slog.Error("stripe checkout refused: no payment method configuration", "err", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "payment_methods_not_configured"})
	case errors.Is(err, stripebill.ErrBillingOff):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "billing_disabled"})
	case errors.Is(err, stripebill.ErrAlreadySubscribed):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "already_subscribed"})
	case errors.Is(err, stripebill.ErrNoCustomer):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "no_billing_account"})
	default:
		serverErrorJSON(w, r, "stripe "+what, err)
	}
	return false
}

// serverErrorJSON logs the error and answers {"error":"billing_unavailable"} with 502: Stripe (or the network to it) failed.
func serverErrorJSON(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), what, "err", err.Error(), "path", r.URL.Path)
	writeJSON(w, http.StatusBadGateway, map[string]any{"error": "billing_unavailable"})
}
