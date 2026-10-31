package applebill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// MaxBodyBytes caps a notification body: Apple's are a few KB.
const MaxBodyBytes = 1 << 20

// EventRetention is how long notification ids are kept. Apple retries a failed delivery for days, not weeks.
const EventRetention = 30 * 24 * time.Hour

// PurgeEvents deletes notification ids older than the retention (the retention job calls it daily).
func PurgeEvents(ctx context.Context, d *sql.DB) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM apple_events WHERE received_at < now() - make_interval(secs => $1)`, EventRetention.Seconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ErrUnmapped is a notification for a subscription no account has linked yet. It is answered 500, so Apple retries: the app's own report of
// the transaction (transaction.go) may simply not have arrived yet.
var ErrUnmapped = errors.New("applebill: no account for the transaction yet")

// Handler is POST /api/billing/apple/notifications. It verifies the signed payload and the transaction in it, then applies the
// notification in ONE transaction that also records its UUID, so a replay changes nothing and a delivery that fails halfway rolls back whole
// (claim included) and is answered 500 so Apple retries it. Entitlements are written only here, by the client report, the Stripe
// webhook, the founding grant, the trial and admin grants.
type Handler struct {
	DB       *sql.DB // the system pool: Apple has no signed-in user
	Verifier *Verifier
	Config   *Config
	// Invalidate drops the account's cached plan on this pod (the others see it within the plan cache's TTL). Called on EVERY change.
	Invalidate func(uuid.UUID)
	Now        func() time.Time
	Log        *slog.Logger
	// Observe is told each notification's outcome (Outcomes); a fixed vocabulary, no user data.
	Observe func(outcome string)
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) observe(outcome string) {
	if h.Observe != nil {
		h.Observe(outcome)
	}
}

func (h *Handler) warn(msg string, args ...any) {
	if h.Log != nil {
		h.Log.Warn(msg, args...)
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var nb NotificationBody
	if json.Unmarshal(body, &nb) != nil || nb.SignedPayload == "" {
		h.observe(OutcomeRejected)
		http.Error(w, "bad notification", http.StatusBadRequest)
		return
	}
	var p NotificationPayload
	if err := h.Verifier.Verify(nb.SignedPayload, &p); err != nil {
		h.observe(OutcomeRejected)
		h.warn("apple notification: signature rejected", "err", err.Error())
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	if p.NotificationUUID == "" || len(p.NotificationUUID) > 255 || p.NotificationType == "" {
		h.observe(OutcomeRejected)
		http.Error(w, "bad notification", http.StatusBadRequest)
		return
	}
	var ti TransactionInfo
	if p.Data != nil {
		if err := h.Config.Check(p.Data.BundleID, p.Data.Environment, ""); err != nil {
			h.observe(OutcomeRejected)
			h.warn("apple notification: not for this deployment", "type", p.NotificationType, "err", err.Error())
			http.Error(w, "rejected", http.StatusBadRequest)
			return
		}
		if p.Data.SignedTransactionInfo != "" {
			if err := h.Verifier.Verify(p.Data.SignedTransactionInfo, &ti); err != nil {
				h.observe(OutcomeRejected)
				h.warn("apple notification: transaction signature rejected", "type", p.NotificationType, "err", err.Error())
				http.Error(w, "invalid signature", http.StatusBadRequest)
				return
			}
			if err := h.Config.Check(ti.BundleID, ti.Environment, ti.ProductID); err != nil || !ti.Subscription() {
				h.observe(OutcomeRejected)
				h.warn("apple notification: transaction not for this deployment", "type", p.NotificationType)
				http.Error(w, "rejected", http.StatusBadRequest)
				return
			}
		}
	}
	outcome, uid, err := h.apply(r.Context(), p, ti)
	if err != nil {
		if errors.Is(err, ErrUnmapped) {
			h.observe(OutcomeUnmapped)
			h.warn("apple notification: no account linked to the transaction yet; Apple will retry", "type", p.NotificationType)
		} else {
			h.warn("apple notification: could not apply it; Apple will retry", "type", p.NotificationType, "err", err.Error())
		}
		http.Error(w, "retry", http.StatusInternalServerError)
		return
	}
	if uid != uuid.Nil && h.Invalidate != nil {
		h.Invalidate(uid)
	}
	h.observe(outcome)
	w.WriteHeader(http.StatusOK)
}

// apply records the notification and applies it, in one transaction. It returns the outcome and the account whose plan changed.
func (h *Handler) apply(ctx context.Context, p NotificationPayload, ti TransactionInfo) (string, uuid.UUID, error) {
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", uuid.Nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx, `INSERT INTO apple_events (notification_uuid, type) VALUES ($1, $2) ON CONFLICT (notification_uuid) DO NOTHING`, p.NotificationUUID, p.NotificationType)
	if err != nil {
		return "", uuid.Nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return OutcomeDuplicate, uuid.Nil, nil
	}
	d := Decide(p.NotificationType, p.Subtype, ti, h.now())
	if !d.Change || ti.OriginalTransactionID == "" {
		return OutcomeIgnored, uuid.Nil, tx.Commit()
	}
	var uid uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM billing_apple_subs WHERE original_transaction_id = $1 FOR UPDATE`, ti.OriginalTransactionID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", uuid.Nil, ErrUnmapped // rolls back, the claim with it
	}
	if err != nil {
		return "", uuid.Nil, err
	}
	signed := p.SignedDate
	if signed == 0 {
		signed = ti.SignedDate
	}
	applied, err := applyDecision(ctx, tx, uid, ti, d, signed, p.NotificationType == "REFUND_REVERSED")
	if err != nil {
		return "", uuid.Nil, err
	}
	if !applied {
		return OutcomeStale, uuid.Nil, tx.Commit()
	}
	return OutcomeApplied, uid, tx.Commit()
}
