package applebill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/entitlements"
)

// MaxTransactionBytes caps the app's report: one signed transaction is under 4 KB.
const MaxTransactionBytes = 16 << 10

// Transaction is POST /api/billing/apple/transaction: the signed-in account reports the transaction its purchase produced. The app's claim
// is worth nothing until the signed value verifies against Apple's root; then the transaction must be for this deployment's bundle,
// environment and product, and it must carry appAccountToken equal to the caller's account id. A signed transaction is a blob anyone can
// copy, so without that check someone handed another person's transaction could post it first and link that subscription to their own
// account. The app always sets the token when it buys.
type Transaction struct {
	DB         *sql.DB // the system pool: it writes the link and the entitlement table
	Verifier   *Verifier
	Config     *Config
	Invalidate func(uuid.UUID)
	Now        func() time.Time
	Log        *slog.Logger
}

func (t *Transaction) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

type transactionRequest struct {
	SignedTransaction string `json:"signedTransaction"`
}

type transactionResponse struct {
	Pro       bool       `json:"pro"`
	Status    string     `json:"status,omitempty"`
	PeriodEnd *time.Time `json:"current_period_end,omitempty"`
}

// Serve handles the request for the signed-in uid.
func (t *Transaction) Serve(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	var req transactionRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxTransactionBytes)).Decode(&req) != nil || req.SignedTransaction == "" {
		writeErr(w, http.StatusBadRequest, "invalid")
		return
	}
	var ti TransactionInfo
	if err := t.Verifier.Verify(req.SignedTransaction, &ti); err != nil {
		t.warn("apple transaction: signature rejected", "err", err.Error())
		writeErr(w, http.StatusBadRequest, "invalid")
		return
	}
	if err := t.Config.Check(ti.BundleID, ti.Environment, ti.ProductID); err != nil || !ti.Subscription() || ti.OriginalTransactionID == "" {
		t.warn("apple transaction: not for this deployment")
		writeErr(w, http.StatusBadRequest, "invalid")
		return
	}
	// The token binds the purchase to the account that made it. No token, or another account's, is refused; the answer does not say which.
	if tok, err := uuid.Parse(ti.AppAccountToken); err != nil || tok != uid {
		t.warn("apple transaction: appAccountToken is missing or not the caller's")
		writeErr(w, http.StatusBadRequest, "invalid")
		return
	}
	resp, status, err := t.link(r.Context(), uid, ti)
	if err != nil {
		t.warn("apple transaction: could not record it", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "unavailable")
		return
	}
	if status != http.StatusOK {
		writeErr(w, status, "conflict")
		return
	}
	if t.Invalidate != nil {
		t.Invalidate(uid)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, http.StatusOK, resp)
}

// link records the subscription's link to the account and grants Pro for a live, unrevoked transaction, in one transaction.
func (t *Transaction) link(ctx context.Context, uid uuid.UUID, ti TransactionInfo) (transactionResponse, int, error) {
	tx, err := t.DB.BeginTx(ctx, nil)
	if err != nil {
		return transactionResponse{}, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT true FROM users WHERE id = $1`, uid).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return transactionResponse{}, 0, errNoUser
	} else if err != nil {
		return transactionResponse{}, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO billing_apple_subs (original_transaction_id, user_id, last_signed_date) VALUES ($1, $2, 0) ON CONFLICT (original_transaction_id) DO NOTHING`, ti.OriginalTransactionID, uid); err != nil {
		return transactionResponse{}, 0, err
	}
	var owner uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM billing_apple_subs WHERE original_transaction_id = $1 FOR UPDATE`, ti.OriginalTransactionID).Scan(&owner); err != nil {
		return transactionResponse{}, 0, err
	}
	if owner != uid {
		return transactionResponse{}, http.StatusConflict, nil // linked to another account already
	}
	now := t.now()
	d := Decide("SUBSCRIBED", "", ti, now)
	if ti.Revoked() {
		d = Decision{} // a refunded transaction grants nothing; the notification path records the revoke
	}
	resp := transactionResponse{}
	if d.Change {
		signed := ti.SignedDate
		if _, err := applyDecision(ctx, tx, uid, ti, d, signed, false); err != nil {
			return transactionResponse{}, 0, err
		}
	}
	var status string
	var end sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT status, current_period_end FROM entitlements WHERE user_id = $1 AND source = 'apple'`, uid).Scan(&status, &end); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return transactionResponse{}, 0, err
	}
	resp.Status = status
	if end.Valid {
		e := end.Time.UTC()
		resp.PeriodEnd = &e
		resp.Pro = status == entitlements.StatusActive && e.After(now)
	}
	return resp, http.StatusOK, tx.Commit()
}

func (t *Transaction) warn(msg string, args ...any) {
	if t.Log != nil {
		t.Log.Warn(msg, args...)
	}
}

// ServeHTTP adapts Serve to a handler that reads the signed-in user from the request.
func (t *Transaction) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	t.Serve(uid, w, r)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSONStatus(w, code, map[string]string{"error": msg})
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
