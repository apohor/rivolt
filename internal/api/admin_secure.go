package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/passkeys"
)

// adminSecure is support's "secure this account" (POST /api/admin/users/{id}/secure), for an account someone else has hold of
// and whose owner cannot do it themselves (docs/COMPROMISED_ACCOUNT.md). It replaces raw SQL on prod. One audited action that:
//
//  1. sets a new random password through Kratos (crypto/rand, 128 bits, 22 characters);
//  2. forgets the identity provider's remembered logins and Kratos sessions (#1110), so a remembered browser is not skipped;
//  3. revokes every Rivolt session and app token;
//  4. deletes every passkey;
//  5. emails the account "support secured your account".
//
// The new password is returned ONCE in the response (no-store) and nowhere else: not logged, not in the audit row (which holds the
// action and the target, never a body), not in the email. Support gives it to the owner only at the address on file (the runbook).
// Every step is idempotent, and on any failure no password is returned: run it again.
type adminSecure struct {
	// Account is the target's sign-in email (sql.ErrNoRows: no such user), read on the system pool: another user's account.
	Account    func(ctx context.Context, uid uuid.UUID) (string, error)
	Kratos     *kratos.Client
	Remembered RememberedRevoker
	Sessions   interface {
		RevokeAll(ctx context.Context, userID uuid.UUID) (int64, error)
	}
	// Passkeys is the passkey tables, required: a reset that cannot delete credentials must not run, and never reports them removed.
	Passkeys passkeys.Store
	Mail     interface {
		Send(context.Context, email.Message) error
	}
	Logger *slog.Logger
	// Observe counts each clearing of the identity provider's memory and its failures; nil is a no-op.
	Observe ForgetObserver
}

// adminSecure builds support's reset from the deployment's dependencies. The passkey store comes from d.passkeyStore(), which does
// not depend on WebAuthn being configured: the admin site runs with passkeys off, and an account can still hold credentials.
func (d Deps) adminSecure() *adminSecure {
	sec := &adminSecure{
		Account: func(ctx context.Context, uid uuid.UUID) (string, error) {
			var m sql.NullString
			err := d.SystemDB.QueryRowContext(ctx, `SELECT email FROM users WHERE id = $1`, uid).Scan(&m) // system pool: another user's account
			return m.String, err
		},
		Kratos: d.Kratos, Remembered: d.RememberedRevoker, Sessions: d.SessionRevoker, Logger: d.Logger, Observe: NewForgetObserver(d.Metrics),
		Passkeys: d.passkeyStore(),
	}
	if d.Email != nil {
		sec.Mail = d.Email
	}
	return sec
}

func (a *adminSecure) handle(w http.ResponseWriter, r *http.Request) {
	target, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid user id"})
		return
	}
	if a.Account == nil || a.Kratos == nil || !a.Kratos.Enabled() || a.Sessions == nil || a.Passkeys == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
		return
	}
	mail, err := a.Account(r.Context(), target)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such user"})
		return
	case err != nil:
		a.Logger.Error("admin secure: look up the account", "err", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
		return
	}
	mail = strings.TrimSpace(mail)
	if mail == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "no_password_account"})
		return
	}
	identityID, err := a.Kratos.IdentityIDByEmail(r.Context(), mail)
	if err != nil {
		a.Logger.Error("admin secure: find the identity", "err", redactIdentifier(err.Error(), mail))
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "unavailable"})
		return
	}
	if identityID == "" {
		// Google, GitHub or Apple only: there is no password here to reset. Disabling the account is the tool for that.
		writeJSON(w, http.StatusConflict, map[string]any{"error": "no_password_account"})
		return
	}
	password, err := kratos.GeneratePassword()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
		return
	}
	fail := func(step string, err error) {
		a.Logger.Error("admin secure: a step failed; nothing was returned, run it again", "step", step, "err", redactIdentifier(err.Error(), mail))
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "incomplete", "step": step})
	}
	// The password first: it kills the one the other person knows. It is only revealed if every step below succeeds.
	if err := a.Kratos.SetPassword(r.Context(), identityID, password); err != nil {
		fail("set_password", err)
		return
	}
	if a.Remembered != nil {
		err := a.Remembered.RevokeRemembered(r.Context(), target)
		a.Observe.observe("admin_secure", err)
		if err != nil {
			fail("forget_remembered_logins", err)
			return
		}
	}
	sessionsRevoked, err := a.Sessions.RevokeAll(r.Context(), target)
	if err != nil {
		fail("revoke_sessions", err)
		return
	}
	removed := 0
	cs, err := a.Passkeys.Credentials(r.Context(), target)
	if err != nil {
		fail("list_passkeys", err)
		return
	}
	for _, c := range cs {
		if _, err := a.Passkeys.Delete(r.Context(), target, c.ID); err != nil {
			fail("delete_passkeys", err)
			return
		}
		removed++
	}
	a.notify(r.Context(), mail)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "temporary_password": password, "sessions_revoked": sessionsRevoked, "passkeys_removed": removed})
}

func (a *adminSecure) notify(ctx context.Context, to string) {
	if a.Mail == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	err := a.Mail.Send(ctx, email.Message{
		To:             to,
		Subject:        "Rivolt support secured your account",
		IdempotencyKey: "support-secure-" + uuid.NewString(),
		Text: "Rivolt support secured your account: the password was reset, every passkey was removed, and every device was signed out.\n\n" +
			"Support will send you a temporary password at this address only. When you have it, sign in and change it straight away in Settings → Security.\n" +
			"If you did not expect this, write to support@rivolt.dev.\n",
	})
	if err != nil {
		a.Logger.Warn("admin secure: notice not sent")
	}
}
