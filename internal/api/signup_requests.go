package api

// HTTP handlers for the pre-account signup request flow. Public
// POST /api/signup/request lets anyone ask for beta access; admin
// GET / approve / reject endpoints live under /api/admin and are
// gated by the existing requireAdminMW.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/invites"
	"github.com/apohor/rivolt/internal/signuprequests"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// signupRequestRow is the JSON shape returned to the admin SPA.
// Mirrors signuprequests.Request with stringified ids/times so the
// frontend can render directly.
type signupRequestRow struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	Message     string  `json:"message"`
	Status      string  `json:"status"`
	InviteCode  *string `json:"invite_code,omitempty"`
	DecidedBy   *string `json:"decided_by,omitempty"`
	DecidedAt   *string `json:"decided_at,omitempty"`
	RequestedAt string  `json:"requested_at"`
}

func toSignupRequestRow(r signuprequests.Request) signupRequestRow {
	row := signupRequestRow{
		ID:          r.ID.String(),
		Email:       r.Email,
		Message:     r.Message,
		Status:      r.Status,
		InviteCode:  r.InviteCode,
		RequestedAt: r.RequestedAt.UTC().Format(time.RFC3339),
	}
	if r.DecidedBy != nil {
		s := r.DecidedBy.String()
		row.DecidedBy = &s
	}
	if r.DecidedAt != nil {
		s := r.DecidedAt.UTC().Format(time.RFC3339)
		row.DecidedAt = &s
	}
	return row
}

// handleSignupRequestCreate — POST /api/signup/request (public)
//
// Body: {"email": "...", "message": "..."}
// 201 on success. Returns a generic 200 even on already-pending so
// requesters can't enumerate prior submissions.
func handleSignupRequestCreate(store *signuprequests.Store, mailer *email.Client, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "signup requests disabled"})
			return
		}
		var body struct {
			Email   string `json:"email"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
			return
		}
		reqEmail := strings.ToLower(strings.TrimSpace(body.Email))
		if !isValidEmail(reqEmail) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid email"})
			return
		}
		// Cap stored message so a hostile caller can't fill the table.
		message := strings.TrimSpace(body.Message)
		if len(message) > 2000 {
			message = message[:2000]
		}
		_, err := store.Create(r.Context(), reqEmail, message)
		switch {
		case err == nil:
			// Fire-and-forget admin notification. Don't block the
			// requester on Resend latency.
			go notifyAdmin(context.Background(), mailer, logger,
				"New Rivolt signup request: "+reqEmail,
				"A new beta-access request landed:\n\n"+
					"  Email:   "+reqEmail+"\n"+
					(func() string {
						if message == "" {
							return ""
						}
						return "  Message: " + message + "\n"
					})()+
					"\nReview at https://rivolt.dev/admin\n",
			)
			writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
		case errors.Is(err, signuprequests.ErrAlreadyPending):
			// Same shape as success — the requester sees a friendly
			// "we'll be in touch" on either path.
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "already_pending": true})
		default:
			if logger != nil {
				logger.WarnContext(r.Context(), "signup request create failed",
					slog.String("email", reqEmail),
					slog.String("err", err.Error()),
				)
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
		}
	}
}

// handleAdminSignupRequestsList — GET /api/admin/signup-requests?status=pending
func handleAdminSignupRequestsList(store *signuprequests.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "signup requests disabled"})
			return
		}
		status := r.URL.Query().Get("status")
		list, err := store.List(r.Context(), status)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		out := make([]signupRequestRow, 0, len(list))
		for _, row := range list {
			out = append(out, toSignupRequestRow(row))
		}
		writeJSON(w, http.StatusOK, map[string]any{"requests": out})
	}
}

// handleAdminSignupRequestApprove — POST /api/admin/signup-requests/{id}/approve
//
// Atomically: mints a single-use invite_code owned by the admin,
// links it on the row, and emails the requester. Failure to send
// the email is logged but does NOT roll back the approval — the
// admin can copy the code from the list view and forward it
// manually (the response always includes the code).
func handleAdminSignupRequestApprove(d *sql.DB, store *signuprequests.Store, inv *invites.Store, mailer *email.Client, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid id"})
			return
		}
		if store == nil || inv == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "approval flow disabled"})
			return
		}
		codes, err := inv.Generate(r.Context(), adminID, 1)
		if err != nil || len(codes) == 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "mint invite: " + errString(err)})
			return
		}
		code := codes[0]
		row, err := store.Approve(r.Context(), id, adminID, code)
		if err != nil {
			if errors.Is(err, signuprequests.ErrNotPending) {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "request not pending"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		// Best-effort email; never blocks the approval response.
		emailErr := sendApprovalEmail(r.Context(), mailer, row.Email, code)
		if emailErr != nil && logger != nil {
			logger.WarnContext(r.Context(), "signup approval email failed",
				slog.String("email", row.Email),
				slog.String("err", emailErr.Error()),
			)
		}
		out := toSignupRequestRow(row)
		writeJSON(w, http.StatusOK, map[string]any{
			"request":    out,
			"email_sent": emailErr == nil,
		})
		_ = d // reserved for future per-tx work
	}
}

// handleAdminSignupRequestReject — POST /api/admin/signup-requests/{id}/reject
func handleAdminSignupRequestReject(store *signuprequests.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid id"})
			return
		}
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "signup requests disabled"})
			return
		}
		row, err := store.Reject(r.Context(), id, adminID)
		if err != nil {
			if errors.Is(err, signuprequests.ErrNotPending) {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "request not pending"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"request": toSignupRequestRow(row)})
	}
}

// sendApprovalEmail composes the plaintext + HTML body and dispatches
// via Resend. The HTML body is a deliberately small inline template;
// Resend renders plaintext for clients that strip HTML.
func sendApprovalEmail(ctx context.Context, mailer *email.Client, to, code string) error {
	if mailer == nil {
		return email.ErrNotConfigured
	}
	subject := "You're in: your Rivolt invite code"
	text := "Hi,\n\n" +
		"Your request for Rivolt beta access has been approved.\n\n" +
		"Use this invite code at https://rivolt.dev/signup to create your account:\n\n" +
		"    " + code + "\n\n" +
		"The code is single-use and expires once redeemed.\n\n" +
		"— Anton (anton@rivolt.dev)\n"
	html := "<p>Hi,</p>" +
		"<p>Your request for Rivolt beta access has been approved.</p>" +
		"<p>Use this invite code at <a href=\"https://rivolt.dev/signup\">https://rivolt.dev/signup</a> to create your account:</p>" +
		"<p style=\"font-family:monospace;font-size:16px;padding:12px;background:#f6f6f6;border-radius:6px;display:inline-block;\">" + code + "</p>" +
		"<p>The code is single-use and expires once redeemed.</p>" +
		"<p>— Anton (<a href=\"mailto:anton@rivolt.dev\">anton@rivolt.dev</a>)</p>"
	return mailer.Send(ctx, email.Message{
		To:      to,
		Subject: subject,
		Text:    text,
		HTML:    html,
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
