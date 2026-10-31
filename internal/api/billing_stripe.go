package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/stripebill"
)

// handleBillingCheckout is POST /api/billing/checkout {"interval":"month"|"year"} -> {"url": the Stripe-hosted Checkout page}. The account
// is the signed-in one and nothing in the request names another: the body carries the interval and nothing else is read from it.
func handleBillingCheckout(svc *stripebill.Service) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		var in struct {
			Interval string `json:"interval"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
			return
		}
		url, err := svc.Checkout(r.Context(), uid, in.Interval)
		if !billingAnswer(w, r, err, "checkout") {
			return
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

// billingAnswer writes the answer for an error and reports whether the call succeeded. The codes are the contract with the clients;
// Stripe's own message goes to the log, never to the user.
func billingAnswer(w http.ResponseWriter, r *http.Request, err error, what string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, stripebill.ErrBadInterval):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_interval"})
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
