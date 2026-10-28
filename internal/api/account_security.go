package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/passkeys"
	"github.com/apohor/rivolt/internal/sessions"
)

// SecureAccountLabel is the one name of the "secure the account" action wherever a person reads it: the Settings → Security button
// (web, ux) and the "passkey added" email. They must be the same string. The code calls the action "secure".
const SecureAccountLabel = "Reset password and remove all passkeys"

// accountSecurityAPI is the owner's way to take an account back: change the password, or "secure my account" (the password, every
// passkey, and every other sign-in). It needs no passkeys: it is how a password gets changed at all, since Kratos recovery is off.
//
//   - POST /api/auth/password {"current","new"}: set the new password; sign every other session out (browsers and the iPhone app);
//     forget the identity provider's remembered logins; keep the passkeys, and list them so the person can remove any they do
//     not recognise.
//   - POST /api/auth/secure {"current","new"}: the same, and delete all the account's passkeys.
//
// Both: a cookie session of the account's own (not the app's token, not an admin viewing as them), same-site JSON, the current
// password re-checked under the sign-in failure limiter per address and per account, and an email to the address on file every
// time (the defence against someone using this against the owner).
type accountSecurityAPI struct {
	Kratos     *kratos.Client
	Remembered RememberedRevoker
	Sessions   interface {
		RevokeOthers(ctx context.Context, userID, keepID uuid.UUID) (int64, error)
	}
	// Passkeys is the passkey tables whether or not WebAuthn is configured (Deps.passkeyStore); nil only without a database.
	Passkeys passkeys.Store
	// Account is the account's sign-in email.
	Account func(ctx context.Context, uid uuid.UUID) (string, error)
	Mail    interface {
		Send(context.Context, email.Message) error
	}
	Failures *failLimiter
	Logger   *slog.Logger
	// Observe counts each clearing of the identity provider's memory and its failures; nil is a no-op.
	Observe ForgetObserver
}

func (a *accountSecurityAPI) guard(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	uid, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return uuid.Nil, false
	}
	if auth.IsImpersonating(r.Context()) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "not_available_while_viewing_as_another_user"})
		return uuid.Nil, false
	}
	if ref, ok := auth.SessionFromContext(r.Context()); ok && ref.Kind == sessions.KindApp || auth.ViaBearer(r.Context()) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "not_available_from_the_app"})
		return uuid.Nil, false
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross_site_request"})
		return uuid.Nil, false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "json_required"})
		return uuid.Nil, false
	}
	return uid, true
}

// accountSecurityAPI builds the owner's password routes from the Deps, or nil when they cannot work: they need Kratos (the password
// lives there), the sessions store (to sign the others out) and a database for the account's email.
func (d Deps) accountSecurityAPI() *accountSecurityAPI {
	if d.Kratos == nil || !d.Kratos.Enabled() || d.Sessions == nil || d.Sessions.Store == nil || d.DB == nil || d.AdminOnly {
		return nil
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	a := &accountSecurityAPI{
		Kratos:     d.Kratos,
		Remembered: d.Sessions.Remembered,
		Sessions:   d.Sessions.Store,
		Account: func(ctx context.Context, uid uuid.UUID) (string, error) {
			var m sql.NullString
			err := d.DB.QueryRowContext(ctx, `SELECT email FROM users WHERE id = $1`, uid).Scan(&m) // request pool: the caller's own row
			return strings.TrimSpace(m.String), err
		},
		// 10 wrong current-passwords per address per 15 minutes, and per client address.
		Failures: newFailLimiter(10, 15*time.Minute).withRedis(d.Redis, d.RedisPrefix, "account", d.FailLimiterKey, d.failLimiterFallback()),
		Logger:   logger,
		Observe:  NewForgetObserver(d.Metrics),
	}
	if d.Email != nil {
		a.Mail = d.Email
	}
	a.Passkeys = d.passkeyStore()
	return a
}

// change serves POST /api/auth/password, secure POST /api/auth/secure.
func (a *accountSecurityAPI) change(w http.ResponseWriter, r *http.Request) { a.serve(w, r, false) }
func (a *accountSecurityAPI) secure(w http.ResponseWriter, r *http.Request) { a.serve(w, r, true) }

// maxPasswordBytes bounds what is read and sent on; Kratos applies its own policy on top.
const maxPasswordBytes = 256

func (a *accountSecurityAPI) serve(w http.ResponseWriter, r *http.Request, secure bool) {
	uid, ok := a.guard(w, r)
	if !ok {
		return
	}
	if secure && a.Passkeys == nil {
		// Before anything is changed: "Secure my account" that cannot see the passkeys would report success with them still there.
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
		return
	}
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || in.Current == "" || in.New == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "current_and_new_required"})
		return
	}
	if len(in.New) > maxPasswordBytes || len(in.Current) > maxPasswordBytes {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "password_too_long"})
		return
	}
	if in.New == in.Current {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "new_password_must_differ"})
		return
	}
	ref, hasSession := auth.SessionFromContext(r.Context())
	if !hasSession || ref.ID == uuid.Nil {
		// Without the current session there is nothing to keep, and everything else would be signed out from under the caller.
		writeJSON(w, http.StatusConflict, map[string]any{"error": "no_session_to_keep"})
		return
	}
	mail, err := a.Account(r.Context(), uid)
	if err != nil || mail == "" || a.Kratos == nil || !a.Kratos.Enabled() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "needs_an_email_account"})
		return
	}
	identityID, err := a.Kratos.IdentityIDByEmail(r.Context(), mail)
	if err != nil {
		a.Logger.Error("account security: find the identity", "err", redactIdentifier(err.Error(), mail))
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	if identityID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "needs_an_email_account"})
		return
	}

	// The current password is the re-authentication. Failures count against the address (a stolen session cannot be used to
	// brute-force it from anywhere) and against the client.
	// The account counter is per SESSION: the threat here is someone holding a session on the account, and a counter shared by every
	// session of the account would let them spend its budget on wrong guesses and lock the owner out of the one recovery action for
	// fifteen minutes, again and again. Per session, a stolen one still gets only ten guesses (it cannot mint more sessions without
	// the password), and the owner's own session has its own budget. The client counter covers a spread across sessions.
	acctKey := "password-change:" + emailLimitKey(mail) + ":" + ref.ID.String()
	ipKey := "password-change-ip:" + demoClientKey(r)
	if a.Failures != nil && (a.Failures.blocked(acctKey) > 0 || a.Failures.blocked(ipKey) > 0) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many attempts, try again later"})
		return
	}
	if _, err := a.Kratos.LoginByPassword(r.Context(), mail, in.Current); err != nil {
		if errors.Is(err, kratos.ErrInvalidCredentials) {
			if a.Failures != nil {
				a.Failures.fail(acctKey)
				a.Failures.fail(ipKey)
			}
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "password_incorrect"})
			return
		}
		a.Logger.Error("account security: re-authentication", "err", redactIdentifier(err.Error(), mail))
		http.Error(w, "auth failed", http.StatusBadGateway)
		return
	}
	if a.Failures != nil {
		a.Failures.reset(acctKey)
	}

	if err := a.Kratos.SetPassword(r.Context(), identityID, in.New); err != nil {
		var rej *kratos.RejectedError
		if errors.As(err, &rej) {
			// reason_code is what a screen should use; reason is Kratos' redacted text, for logs and support, never for display.
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "password_rejected", "reason_code": rej.Code, "reason": rej.Reason})
			return
		}
		a.Logger.Error("account security: set password", "err", redactIdentifier(err.Error(), mail))
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}

	// The password is changed. Everything below is cleanup that must not be skipped silently, and it runs in this order so a retry
	// never needs the old password: forget the identity provider's remembered logins, sign the other sessions out, then (Secure
	// only) delete the passkeys. Each part is tried even when an earlier one failed; the answer names what is still to do, so the
	// screen can offer each as one tap: "Sign out other devices" finishes sign_out, "Remove all passkeys" finishes passkeys.
	var revoked int64
	var removed int
	var incomplete []string
	signOutOK := true
	if a.Remembered != nil {
		err := a.Remembered.RevokeRemembered(r.Context(), uid)
		entry := "password"
		if secure {
			entry = "secure"
		}
		a.Observe.observe(entry, err)
		if err != nil {
			a.Logger.Error("account security: could not clear the remembered sign-in", "secure", secure, "err", redactIdentifier(err.Error(), mail))
			signOutOK = false
		}
	}
	if n, err := a.Sessions.RevokeOthers(r.Context(), uid, ref.ID); err != nil {
		a.Logger.Error("account security: could not sign the other sessions out", "secure", secure, "err", redactIdentifier(err.Error(), mail))
		signOutOK = false
	} else {
		revoked = n
	}
	if !signOutOK {
		incomplete = append(incomplete, "sign_out")
	}
	if secure {
		if a.Passkeys == nil {
			// Never report "secured" when the passkeys could not be looked at.
			a.Logger.Error("account security: no passkey store, cannot remove the passkeys")
			incomplete = append(incomplete, "passkeys")
		} else if err := a.deleteAllPasskeys(r.Context(), uid, &removed); err != nil {
			a.Logger.Error("account security: could not remove the passkeys", "err", redactIdentifier(err.Error(), mail))
			incomplete = append(incomplete, "passkeys")
		}
	}
	// The owner is told whether or not everything finished.
	a.notify(r.Context(), mail, secure, uid)
	if len(incomplete) > 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "password_changed_but_sign_out_incomplete", "password_changed": true, "incomplete": incomplete,
			"sessions_revoked": revoked, "passkeys_removed": removed,
		})
		return
	}

	out := map[string]any{"ok": true, "sessions_revoked": revoked}
	if secure {
		out["passkeys_removed"] = removed
	} else if a.Passkeys != nil {
		// "Change password" keeps the passkeys: list them, so the person can remove any they do not recognise.
		if cs, err := a.Passkeys.Credentials(r.Context(), uid); err == nil {
			views := make([]passkeyView, 0, len(cs))
			for _, c := range cs {
				views = append(views, viewOf(c))
			}
			out["passkeys"] = views
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// deleteAllPasskeys removes every passkey of the account, counting each one as it goes (so a partial failure still reports them).
func (a *accountSecurityAPI) deleteAllPasskeys(ctx context.Context, uid uuid.UUID, removed *int) error {
	cs, err := a.Passkeys.Credentials(ctx, uid)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if _, err := a.Passkeys.Delete(ctx, uid, c.ID); err != nil {
			return err
		}
		*removed++
	}
	return nil
}

// notify emails the address on file, every time: a change made against the owner is only ever caught by this message and support.
func (a *accountSecurityAPI) notify(ctx context.Context, to string, secure bool, uid uuid.UUID) {
	if a.Mail == nil || to == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	subject, text := "Your Rivolt password was changed", "The password for your Rivolt account was just changed, and every other device was signed out.\n\n"+
		"If this was you, there is nothing to do. If it wasn't, write to support@rivolt.dev right away.\n"
	if secure {
		subject = "Your Rivolt account was secured"
		text = "You chose \"" + SecureAccountLabel + "\": the password was changed, every passkey was removed, and every other device was signed out.\n\n" +
			"If this wasn't you, write to support@rivolt.dev right away.\n"
	}
	if err := a.Mail.Send(ctx, email.Message{To: to, Subject: subject, Text: text, IdempotencyKey: "account-security-" + uuid.NewString()}); err != nil {
		a.Logger.Warn("account security: notice not sent")
	}
}
