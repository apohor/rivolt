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
	"github.com/apohor/rivolt/internal/signuprequests"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// signupRequestRow is the JSON shape returned to the admin SPA.
// Mirrors signuprequests.Request with stringified ids/times so the
// frontend can render directly.
type signupRequestRow struct {
	ID             string  `json:"id"`
	Email          string  `json:"email"`
	Message        string  `json:"message"`
	Status         string  `json:"status"`
	SignupToken    *string `json:"signup_token,omitempty"`
	TokenExpiresAt *string `json:"token_expires_at,omitempty"`
	TokenUsedAt    *string `json:"token_used_at,omitempty"`
	DecidedBy      *string `json:"decided_by,omitempty"`
	DecidedAt      *string `json:"decided_at,omitempty"`
	RequestedAt    string  `json:"requested_at"`
}

func toSignupRequestRow(r signuprequests.Request) signupRequestRow {
	row := signupRequestRow{
		ID:          r.ID.String(),
		Email:       r.Email,
		Message:     r.Message,
		Status:      r.Status,
		SignupToken: r.SignupToken,
		RequestedAt: r.RequestedAt.UTC().Format(time.RFC3339),
	}
	if r.TokenExpiresAt != nil {
		s := r.TokenExpiresAt.UTC().Format(time.RFC3339)
		row.TokenExpiresAt = &s
	}
	if r.TokenUsedAt != nil {
		s := r.TokenUsedAt.UTC().Format(time.RFC3339)
		row.TokenUsedAt = &s
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
// Mints a single-use signup token + expiry on the row, stamps the
// admin as decided_by, and emails the requester a magic link
//    https://rivolt.dev/signup?token=<token>
//
// Email is best-effort: the response carries the token + link
// regardless so the admin can copy/paste a manual forward when
// Resend has a hiccup. We no longer mint invite_codes on approval;
// the legacy code-redeem path stays only for codes already
// distributed before this flow shipped.
func handleAdminSignupRequestApprove(d *sql.DB, store *signuprequests.Store, mailer *email.Client, baseURL string, logger *slog.Logger) http.HandlerFunc {
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
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "approval flow disabled"})
			return
		}
		row, err := store.ApproveWithToken(r.Context(), id, adminID, 0)
		if err != nil {
			if errors.Is(err, signuprequests.ErrNotPending) {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "request not pending"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		token := ""
		if row.SignupToken != nil {
			token = *row.SignupToken
		}
		link := signupLink(baseURL, token)
		// Best-effort email; never blocks the approval response.
		emailErr := sendApprovalEmail(r.Context(), mailer, row.Email, link)
		if emailErr != nil && logger != nil {
			logger.WarnContext(r.Context(), "signup approval email failed",
				slog.String("email", row.Email),
				slog.String("err", emailErr.Error()),
			)
		}
		out := toSignupRequestRow(row)
		writeJSON(w, http.StatusOK, map[string]any{
			"request":    out,
			"link":       link,
			"email_sent": emailErr == nil,
		})
		_ = d // reserved for future per-tx work
	}
}

// signupLink composes the magic-link URL the requester clicks to
// finish signup. baseURL is the install's public origin (e.g.
// https://rivolt.dev); when empty we fall back to a relative path so
// at least copy-paste-into-the-same-tab works.
func signupLink(baseURL, token string) string {
	if token == "" {
		return ""
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		return "/signup?token=" + token
	}
	return base + "/signup?token=" + token
}

// handleSignupTokenLookup — GET /api/signup/token/{token} (public)
//
// Returns {email, expires_at} when the token is valid. 410 Gone when
// it is missing / used / expired so the SPA can branch to a friendly
// "this link is no longer valid, please request a new invite" view
// without leaking which condition failed.
func handleSignupTokenLookup(store *signuprequests.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "signup tokens disabled"})
			return
		}
		token := chi.URLParam(r, "token")
		req, err := store.LookupToken(r.Context(), token)
		if err != nil {
			writeJSON(w, http.StatusGone, map[string]any{"error": "token invalid or expired"})
			return
		}
		out := map[string]any{
			"email": req.Email,
		}
		if req.TokenExpiresAt != nil {
			out["expires_at"] = req.TokenExpiresAt.UTC().Format(time.RFC3339)
		}
		writeJSON(w, http.StatusOK, out)
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
// via Resend. Magic-link rather than invite-code so the requester
// can click straight through to a signup form with their email
// already filled in.
func sendApprovalEmail(ctx context.Context, mailer *email.Client, to, link string) error {
	if mailer == nil {
		return email.ErrNotConfigured
	}
	subject := "You're in: finish your Rivolt signup"
	// Same-domain redirect (served by GET /discord) so the email's links align with the sender domain.
	const discordURL = "https://rivolt.dev/discord"
	text := "Hi,\n\n" +
		"Your request for Rivolt beta access has been approved.\n\n" +
		"Click this link to finish signing up (it'll prefill your email\n" +
		"so you only need to pick a password):\n\n" +
		"    " + link + "\n\n" +
		"The link is single-use and expires in 14 days.\n\n" +
		"Join the Rivolt community on Discord for help, feedback, and\n" +
		"early access to new features:\n\n" +
		"    " + discordURL + "\n\n" +
		"— Anton (anton@rivolt.dev)\n"
	html := "<p>Hi,</p>" +
		"<p>Your request for Rivolt beta access has been approved.</p>" +
		"<p>Click below to finish signing up — your email is already filled in, " +
		"you'll just need to pick a password.</p>" +
		"<p><a href=\"" + link + "\" " +
		"style=\"display:inline-block;padding:10px 18px;background:#10b981;color:#fff;" +
		"font-weight:600;border-radius:6px;text-decoration:none;\">Finish signup</a></p>" +
		"<p style=\"color:#666;font-size:13px;\">Or paste this URL into your browser:<br>" +
		"<a href=\"" + link + "\">" + link + "</a></p>" +
		"<p>The link is single-use and expires in 14 days.</p>" +
		"<p>Join the community on " +
		"<a href=\"" + discordURL + "\" style=\"color:#5865f2;font-weight:600;\">Discord</a> " +
		"for help, feedback, and early access to new features.</p>" +
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
