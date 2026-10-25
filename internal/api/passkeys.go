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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/passkeys"
	"github.com/apohor/rivolt/internal/sessions"
)

// Passkeys, the HTTP side (Sprint 10; design DA-approved). Two surfaces:
//
//   - managing them, for a signed-in browser session: list, add (after the password is typed again), rename, delete;
//   - signing in on the Hydra login page: begin and finish, bound to the Hydra login challenge, ending in the same
//     AcceptLoginRequest the password path makes.
//
// A passkey is a way into an email account (one Rivolt keeps in Kratos); an account that signs in only with Google, GitHub or
// Apple has no password to type and no Kratos identity, and is told so. Nothing here logs a credential id, key, challenge or
// user handle.

// passkeyAPI carries what the handlers use.
type passkeyAPI struct {
	Svc     *passkeys.Service
	Kratos  *kratos.Client
	Hydra   *hydra.Client
	DB      *sql.DB
	Account func(ctx context.Context, uid uuid.UUID) (mail, display string, err error)
	Mail    interface {
		Send(ctx context.Context, m email.Message) error
	}
	Logger    *slog.Logger
	Failures  *failLimiter // the password path's: wrong passwords per address; sign-in failures per client address
	OnRefused func(reason string)
	Remember  int64 // Hydra remember-for seconds, as the password path
	BaseURL   string
}

// accountOf is the signed-in account's email and display name.
func (p *passkeyAPI) accountOf(ctx context.Context, uid uuid.UUID) (mail, display string, err error) {
	if p.Account != nil {
		return p.Account(ctx, uid)
	}
	var m, d sql.NullString
	err = p.DB.QueryRowContext(ctx, `SELECT email, display_name FROM users WHERE id = $1`, uid).Scan(&m, &d)
	return strings.TrimSpace(m.String), strings.TrimSpace(d.String), err
}

// guard is what every management route checks first: configured, a signed-in user, a browser session of their own (not an
// admin viewing as them, not the iPhone app's token), not a cross-site request, and JSON when there is a body.
func (p *passkeyAPI) guard(w http.ResponseWriter, r *http.Request, body bool) (uuid.UUID, bool) {
	if p == nil || p.Svc == nil {
		http.NotFound(w, r)
		return uuid.Nil, false
	}
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
	if r.Method != http.MethodGet {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross_site_request"})
			return uuid.Nil, false
		}
		if body {
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "json_required"})
				return uuid.Nil, false
			}
		}
	}
	return uid, true
}

type passkeyView struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Synced     bool       `json:"synced"` // can be backed up (iCloud Keychain, a password manager)
}

func viewOf(c passkeys.Credential) passkeyView {
	return passkeyView{ID: c.ID, Label: c.Label, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt, Synced: c.BackupEligible}
}

// list serves GET /api/auth/passkeys.
func (p *passkeyAPI) list(w http.ResponseWriter, r *http.Request) {
	uid, ok := p.guard(w, r, false)
	if !ok {
		return
	}
	cs, err := p.Svc.Store().Credentials(r.Context(), uid)
	if err != nil {
		p.Logger.Error("passkeys: list", "err", err)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	out := make([]passkeyView, 0, len(cs))
	for _, c := range cs {
		out = append(out, viewOf(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": out, "max": passkeys.MaxPerAccount})
}

// registerBegin serves POST /api/auth/passkeys/register/begin {"password": "..."}: the password is asked again, so a hijacked
// session alone cannot add a way in.
func (p *passkeyAPI) registerBegin(w http.ResponseWriter, r *http.Request) {
	uid, ok := p.guard(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || in.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "password_required"})
		return
	}
	mail, display, err := p.accountOf(r.Context(), uid)
	if err != nil || mail == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "passkeys_need_an_email_account"})
		return
	}
	if p.Kratos == nil || !p.Kratos.Enabled() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "passkeys_need_an_email_account"})
		return
	}
	key := "passkey-reauth:" + emailLimitKey(mail)
	if p.Failures != nil {
		if wait := p.Failures.blocked(key); wait > 0 {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many attempts, try again later"})
			return
		}
	}
	if id, err := p.Kratos.IdentityIDByEmail(r.Context(), mail); err == nil && id == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "passkeys_need_an_email_account"})
		return
	}
	if _, err := p.Kratos.LoginByPassword(r.Context(), mail, in.Password); err != nil {
		if errors.Is(err, kratos.ErrInvalidCredentials) {
			if p.Failures != nil {
				p.Failures.fail(key)
			}
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "password_incorrect"})
			return
		}
		p.Logger.Error("passkeys: re-authentication", "err", err)
		http.Error(w, "auth failed", http.StatusBadGateway)
		return
	}
	if p.Failures != nil {
		p.Failures.reset(key)
	}
	cid, opts, err := p.Svc.BeginRegistration(r.Context(), uid, mail, orFallback(display, mail))
	if errors.Is(err, passkeys.ErrTooMany) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "too_many_passkeys", "max": passkeys.MaxPerAccount})
		return
	}
	if err != nil {
		p.Logger.Error("passkeys: begin registration", "err", err)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony_id": cid, "options": opts})
}

// registerFinish serves POST /api/auth/passkeys/register/finish {"ceremony_id", "label", "credential"}.
func (p *passkeyAPI) registerFinish(w http.ResponseWriter, r *http.Request) {
	uid, ok := p.guard(w, r, true)
	if !ok {
		return
	}
	var in struct {
		CeremonyID uuid.UUID       `json:"ceremony_id"`
		Label      string          `json:"label"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || len(in.Credential) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
		return
	}
	mail, display, err := p.accountOf(r.Context(), uid)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	c, err := p.Svc.FinishRegistration(r.Context(), uid, in.CeremonyID, in.Credential, in.Label, mail, orFallback(display, mail))
	var refused passkeys.Refused
	if errors.As(err, &refused) {
		p.refused(refused.Reason)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "registration_failed"})
		return
	}
	if err != nil {
		p.Logger.Error("passkeys: finish registration", "err", err)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	p.notifyAdded(r.Context(), mail, c)
	writeJSON(w, http.StatusOK, map[string]any{"passkey": viewOf(c)})
}

// notifyAdded emails the account that a passkey was added: it catches a hijacked session with a known password. Best effort.
func (p *passkeyAPI) notifyAdded(ctx context.Context, to string, c passkeys.Credential) {
	if p.Mail == nil || to == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := p.Mail.Send(ctx, email.Message{
		To:             to,
		Subject:        "A passkey was added to your Rivolt account",
		IdempotencyKey: "passkey-added-" + c.ID.String(),
		Text: "A passkey named \"" + c.Label + "\" was added to your Rivolt account, so you can now sign in with it.\n\n" +
			"If this was you, there is nothing to do. If it wasn't, sign in, remove the passkey in Settings, change your password,\n" +
			"and write to support@rivolt.dev.\n",
	}); err != nil {
		p.Logger.Warn("passkeys: added notice not sent")
	}
}

func (p *passkeyAPI) refused(reason string) {
	if p.OnRefused != nil {
		p.OnRefused(reason)
	}
}

// rename serves PATCH /api/auth/passkeys/{id} {"label": "..."}.
func (p *passkeyAPI) rename(w http.ResponseWriter, r *http.Request) {
	uid, ok := p.guard(w, r, true)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	var in struct {
		Label string `json:"label"`
	}
	label := ""
	if err == nil && json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in) == nil {
		label = strings.TrimSpace(in.Label)
	}
	if err != nil || label == "" || len([]rune(label)) > 60 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
		return
	}
	found, err := p.Svc.Store().Rename(r.Context(), uid, id, label)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// remove serves DELETE /api/auth/passkeys/{id}. Removing one needs no password: it only takes a way in away.
func (p *passkeyAPI) remove(w http.ResponseWriter, r *http.Request) {
	uid, ok := p.guard(w, r, false)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	found, err := p.Svc.Store().Delete(r.Context(), uid, id)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hydraBegin serves POST /api/auth/hydra/passkey/begin {"challenge": "..."}: the Hydra login challenge is checked first, as the
// password path does, so this is no oracle with no login in progress.
func (p *passkeyAPI) hydraBegin(w http.ResponseWriter, r *http.Request) {
	if p == nil || p.Svc == nil || p.Hydra == nil || !p.Hydra.Enabled() {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Challenge string `json:"challenge"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || strings.TrimSpace(in.Challenge) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := p.Hydra.GetLoginRequest(r.Context(), in.Challenge); err != nil {
		if errors.Is(err, hydra.ErrBadChallenge) {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		p.Logger.Error("passkeys: hydra challenge", "err", err)
		http.Error(w, "hydra fetch failed", http.StatusBadGateway)
		return
	}
	cid, opts, err := p.Svc.BeginLogin(r.Context(), in.Challenge)
	if err != nil {
		p.Logger.Error("passkeys: begin sign-in", "err", err)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony_id": cid, "options": opts})
}

// hydraFinish serves POST /api/auth/hydra/passkey/finish {"challenge", "ceremony_id", "credential"}. Every way it can fail
// short of a server fault answers alike: 401 "invalid credentials".
func (p *passkeyAPI) hydraFinish(w http.ResponseWriter, r *http.Request) {
	if p == nil || p.Svc == nil || p.Hydra == nil || !p.Hydra.Enabled() || p.Kratos == nil || !p.Kratos.Enabled() {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Challenge  string          `json:"challenge"`
		CeremonyID uuid.UUID       `json:"ceremony_id"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || in.Challenge == "" || len(in.Credential) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The same failure limiter as the password path, keyed by client address (a passkey sign-in names no email).
	key := "passkey-signin:" + clientIP(r)
	if p.Failures != nil {
		if wait := p.Failures.blocked(key); wait > 0 {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many attempts, try again later"})
			return
		}
	}
	invalid := func(reason string) {
		p.refused(reason)
		if p.Failures != nil {
			p.Failures.fail(key)
		}
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
	}
	if _, err := p.Hydra.GetLoginRequest(r.Context(), in.Challenge); err != nil {
		if errors.Is(err, hydra.ErrBadChallenge) {
			invalid(passkeys.ReasonCeremony)
			return
		}
		p.Logger.Error("passkeys: hydra challenge", "err", err)
		http.Error(w, "hydra fetch failed", http.StatusBadGateway)
		return
	}
	uid, cred, err := p.Svc.FinishLogin(r.Context(), in.CeremonyID, in.Challenge, in.Credential)
	var refused passkeys.Refused
	if errors.As(err, &refused) {
		invalid(refused.Reason)
		return
	}
	if err != nil {
		p.Logger.Error("passkeys: finish sign-in", "err", err)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	mail, _, err := p.accountOf(r.Context(), uid)
	if err != nil || mail == "" {
		invalid(passkeys.ReasonUnknown)
		return
	}
	subject, err := p.Kratos.IdentityIDByEmail(r.Context(), mail)
	if err != nil {
		p.Logger.Error("passkeys: identity lookup", "err", err)
		http.Error(w, "auth failed", http.StatusBadGateway)
		return
	}
	if subject == "" {
		invalid(passkeys.ReasonUnknown)
		return
	}
	if p.Failures != nil {
		p.Failures.reset(key)
	}
	amr := []string{"hwk", "user"}
	if cred.BackupEligible {
		amr = []string{"swk", "user"} // a synced passkey: a software-held key
	}
	redirect, err := p.Hydra.AcceptLoginRequest(r.Context(), in.Challenge, hydra.AcceptLoginRequest{
		Subject: subject, Remember: true, RememberFor: p.Remember, ACR: "0", AMR: amr,
	})
	if err != nil {
		p.Logger.Error("passkeys: hydra accept", "err", err)
		http.Error(w, "hydra accept failed", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"redirect_to": redirect.RedirectTo})
}

// passkeyAPI builds the handlers' dependencies from the Deps, or nil when passkeys are not configured (or there is no database
// to keep them in).
func (d Deps) passkeyAPI() *passkeyAPI {
	if d.Passkeys == nil || (d.SystemDB == nil && d.PasskeyAccount == nil) {
		return nil
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	p := &passkeyAPI{Svc: d.Passkeys, Kratos: d.Kratos, Hydra: d.Hydra, DB: d.SystemDB /* system pool: the sign-in ceremony looks the account up before a user is known */, Account: d.PasskeyAccount, Logger: logger, BaseURL: d.BaseURL}
	switch {
	case d.PasskeyMailer != nil:
		p.Mail = d.PasskeyMailer
	case d.Email != nil:
		p.Mail = d.Email
	}
	if d.Metrics != nil {
		p.OnRefused = func(reason string) { d.Metrics.PasskeyRefusedTotal.WithLabelValues(reason).Inc() }
	}
	return p
}
