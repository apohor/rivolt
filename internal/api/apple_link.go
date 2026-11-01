package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/oidc"
)

// Link Apple to my account, the start (design: _shared/apple-link-design.md, DA-approved Nov 1). A signed-in browser session asks to
// attach an Apple identity to its own account; the answer is the URL to send the browser to. Apple's reply comes back to the OIDC
// callback (internal/oidc), which finds who is linking from the request recorded here, never from a cookie.
//
// Linking adds a way into the account, so it needs a recent sign-in: a session created within ten minutes, or else the account's
// password typed again (the same bar as adding a passkey). An account with neither (it signs in only with Google or GitHub, and its
// session is older) is told to sign in again first.

// appleLinkFresh is how recent a sign-in a link needs.
const appleLinkFresh = 10 * time.Minute

// appleLinkAPI carries what the start uses.
type appleLinkAPI struct {
	OIDC interface {
		BeginAppleLink(w http.ResponseWriter, r *http.Request, uid, sessionID uuid.UUID) (string, error)
	}
	Sessions interface {
		Live(ctx context.Context, userID, id uuid.UUID) (created time.Time, ok bool, err error)
	}
	Kratos *kratos.Client
	// Account is the account's sign-in email ("" when it has none).
	Account  func(ctx context.Context, uid uuid.UUID) (string, error)
	Failures *failLimiter // wrong passwords per address, as the passkey path counts them
	Logger   *slog.Logger
	Observe  func(outcome string) // rivolt_apple_link_total
	Now      func() time.Time
}

func (a *appleLinkAPI) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *appleLinkAPI) observe(outcome string) {
	if a.Observe != nil {
		a.Observe(outcome)
	}
}

// start serves POST /api/auth/oidc/apple/link/start {"password": "..."} (the password only when the sign-in is older than ten
// minutes) with {"url": "<where to send the browser>"}. The Apple state cookie comes with the response.
func (a *appleLinkAPI) start(w http.ResponseWriter, r *http.Request) {
	uid, ok := browserSessionGuard(w, r)
	if !ok {
		return
	}
	ref, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "needs_a_browser_session"})
		return
	}
	created, live, err := a.Sessions.Live(r.Context(), uid, ref.ID)
	if err != nil {
		a.Logger.Error("apple link: session check", "err", logging.ScrubErr(err))
		a.observe("provider_error")
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !live {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if a.now().Sub(created) > appleLinkFresh {
		if code, status := a.reauthenticate(w, r, uid); code != "" {
			a.observe("reauth_refused")
			writeJSON(w, status, map[string]any{"error": code})
			return
		}
	}
	authURL, err := a.OIDC.BeginAppleLink(w, r, uid, ref.ID)
	if errors.Is(err, oidc.ErrAppleNotConfigured) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "apple_not_available"})
		return
	}
	if err != nil {
		a.Logger.Error("apple link: begin", "err", logging.ScrubErr(err))
		a.observe("provider_error")
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	a.observe("started")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"url": authURL})
}

// reauthenticate checks the password in the request for a sign-in older than appleLinkFresh. It returns an error code and status when
// the link must not go on, "" when it may.
func (a *appleLinkAPI) reauthenticate(w http.ResponseWriter, r *http.Request, uid uuid.UUID) (string, int) {
	mail, err := "", error(nil)
	if a.Account != nil {
		mail, err = a.Account(r.Context(), uid)
	}
	if err != nil {
		a.Logger.Error("apple link: account", "err", logging.ScrubErr(err))
		return "unavailable", http.StatusInternalServerError
	}
	if mail == "" || a.Kratos == nil || !a.Kratos.Enabled() {
		return "recent_sign_in_required", http.StatusForbidden // no password to ask for: sign in again
	}
	if id, err := a.Kratos.IdentityIDByEmail(r.Context(), mail); err == nil && id == "" {
		return "recent_sign_in_required", http.StatusForbidden
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || in.Password == "" {
		return "password_required", http.StatusForbidden
	}
	key := "apple-link-reauth:" + emailLimitKey(mail)
	if a.Failures != nil && a.Failures.blocked(key) > 0 {
		return "too many attempts, try again later", http.StatusTooManyRequests
	}
	if _, err := a.Kratos.LoginByPassword(r.Context(), mail, in.Password); err != nil {
		if errors.Is(err, kratos.ErrInvalidCredentials) {
			if a.Failures != nil {
				a.Failures.fail(key)
			}
			return "password_incorrect", http.StatusForbidden
		}
		a.Logger.Error("apple link: re-authentication", "err", logging.ScrubErr(err))
		return "auth_failed", http.StatusBadGateway
	}
	if a.Failures != nil {
		a.Failures.reset(key)
	}
	return "", 0
}

// appleLinkAPI builds the start from the Deps, or nil when linking cannot work here: it needs the OIDC service (with an Apple provider
// and the link store behind it), the sessions store and a database for the account's email, and is not on the admin deployment.
func (d Deps) appleLinkAPI() *appleLinkAPI {
	if d.OIDC == nil || d.Sessions == nil || d.Sessions.Store == nil || d.DB == nil || d.AdminOnly {
		return nil
	}
	live, ok := d.Sessions.Store.(interface {
		Live(ctx context.Context, userID, id uuid.UUID) (time.Time, bool, error)
	})
	if !ok { // the real store has it; a test double of SessionsStore need not
		return nil
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	a := &appleLinkAPI{
		OIDC:     d.OIDC,
		Sessions: live,
		Kratos:   d.Kratos,
		Account: func(ctx context.Context, uid uuid.UUID) (string, error) {
			var m sql.NullString
			err := d.DB.QueryRowContext(ctx, `SELECT email FROM users WHERE id = $1`, uid).Scan(&m) // request pool: the caller's own row
			return strings.TrimSpace(m.String), err
		},
		Failures: newFailLimiter(10, 15*time.Minute).withRedis(d.Redis, d.RedisPrefix, "apple-link", d.FailLimiterKey, d.failLimiterFallback()),
		Logger:   logger,
	}
	if d.Metrics != nil {
		a.Observe = func(outcome string) { d.Metrics.AppleLinkTotal.WithLabelValues(outcome).Inc() }
	}
	return a
}
