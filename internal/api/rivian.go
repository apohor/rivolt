package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"

	"github.com/google/uuid"
)

// httpStatusForUpstream picks an HTTP status for an error that
// originated from the Rivian gateway. The mapping matters for two
// reasons:
//
//  1. Cloudflare (and most edges) replace 5xx response bodies with
//     a branded HTML error page. A bad-credentials response that
//     comes back as 502 reaches the browser as Cloudflare HTML
//     instead of our JSON, so the SPA can't render a useful
//     message. 4xx is passed through cleanly.
//  2. The class already encodes who the error belongs to. UserAction
//     means the user has to fix something on their side (bad
//     password, missing MFA, expired session). RateLimited means
//     the client should back off. Mapping these to 4xx gives the
//     SPA accurate semantics without inspecting our error strings.
//
// 5xx is reserved for genuine upstream gateway failures the user
// cannot fix.
func httpStatusForUpstream(err error) int {
	// Per-user re-auth sentinel: rivolt's classifier has already
	// flagged this user as needing to re-link their Rivian account
	// (token rotation / hard expiry / 401 on a prior call). The
	// gateway was never reached for this request. Map to 401 so the
	// SPA shows the re-auth banner instead of "Bad Gateway".
	if errors.Is(err, rivian.ErrNeedsReauth) {
		return http.StatusUnauthorized
	}
	// Operator kill switch — close to "service unavailable" on the
	// rivolt side, not "upstream wobble." 503 lets the SPA back off
	// instead of treating it as a retryable network blip.
	if errors.Is(err, rivian.ErrUpstreamPaused) {
		return http.StatusServiceUnavailable
	}
	var ue *rivian.UpstreamError
	if !errors.As(err, &ue) {
		return http.StatusBadGateway
	}
	switch ue.Class {
	case rivian.ClassUserAction:
		// 401 — the user's credentials / session need attention.
		// Distinct from a rivolt-side auth fail (also 401) by
		// the response body, which carries the upstream reason.
		return http.StatusUnauthorized
	case rivian.ClassRateLimited:
		return http.StatusTooManyRequests
	case rivian.ClassOutage:
		return http.StatusServiceUnavailable
	default:
		// ClassTransient + ClassUnknown: real upstream wobble,
		// retry-eligible. 502 is the right verb here.
		return http.StatusBadGateway
	}
}

// writeUpstreamError renders an UpstreamError (or any wrapped
// error from the rivian package) as JSON with the right status.
// Body shape is stable: {error, class, reason?}.
//
// The `class` field is load-bearing on the client: the SPA uses it to
// tell a *Rivian-upstream* 401 ("reconnect your Rivian") from a
// *Rivolt-session* 401 ("your login expired"), redirecting to /login
// only for the latter. ErrNeedsReauth is the persisted-flag path — a
// bare sentinel, not an UpstreamError, so errors.As misses it. Without
// a class it looks like a session expiry and bounces the user to
// /login in a loop (surfaced by impersonating a needs_reauth user).
// It is a user_action by definition (the user must re-link Rivian), so
// stamp that class explicitly.
func writeUpstreamError(w http.ResponseWriter, err error) {
	status := httpStatusForUpstream(err)
	body := map[string]any{"error": err.Error()}
	var ue *rivian.UpstreamError
	switch {
	case errors.As(err, &ue):
		body["class"] = ue.Class.String()
		if ue.Reason != "" {
			body["reason"] = ue.Reason
		}
	case errors.Is(err, rivian.ErrNeedsReauth):
		body["class"] = rivian.ClassUserAction.String()
		body["reason"] = "session expired"
	}
	writeJSON(w, status, body)
}

// rivianStatusDTO is the public view of the Rivian account state.
// Email is returned as-is for the authenticated caller's own session.
type rivianStatusDTO struct {
	Enabled       bool   `json:"enabled"` // true iff a live client is wired
	Authenticated bool   `json:"authenticated"`
	MFAPending    bool   `json:"mfa_pending"`
	Email         string `json:"email,omitempty"`
	// NeedsReauth signals that a stored session is structurally
	// present (Authenticated=true) but a runtime classifier has
	// flagged it as no longer usable — typically because Rivian's
	// WS gateway is rejecting the userSessionToken even though the
	// REST cache still hides the rot. UI surfaces a banner so the
	// user knows to re-sign in instead of waiting for missing
	// drives to tip them off.
	NeedsReauth       bool   `json:"needs_reauth"`
	NeedsReauthReason string `json:"needs_reauth_reason,omitempty"`
}

// primeAttempts debounces lazy-prime kicks from handleRivianStatus.
// The SPA polls /api/settings/rivian every few seconds, and without
// this we'd hit Rivian's getUserInfo on every poll for any user
// whose vehicles row hasn't landed yet. One attempt per pod per
// user per 5 minutes is more than enough to self-heal accounts that
// connected before the eager-prime fix shipped.
var (
	primeAttempts   = make(map[uuid.UUID]time.Time)
	primeAttemptsMu sync.Mutex
)

const primeAttemptInterval = 5 * time.Minute

func shouldAttemptPrime(uid uuid.UUID) bool {
	primeAttemptsMu.Lock()
	defer primeAttemptsMu.Unlock()
	if last, ok := primeAttempts[uid]; ok && time.Since(last) < primeAttemptInterval {
		return false
	}
	primeAttempts[uid] = time.Now()
	return true
}

func handleRivianStatus(reg rivian.AccountRegistry, store *secrets.Store, sqlDB *sql.DB, m *metrics.Metrics, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil {
			writeJSON(w, http.StatusOK, rivianStatusDTO{Enabled: false})
			return
		}
		// Status is allowed without an authenticated context (e.g.
		// the SPA polling on first paint before a session resolves);
		// in that case we return the "live wired but no session yet"
		// shape rather than 401 so the UI render path stays simple.
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusOK, rivianStatusDTO{Enabled: true})
			return
		}
		lc := reg.For(uid)
		if lc == nil {
			writeJSON(w, http.StatusOK, rivianStatusDTO{Enabled: false})
			return
		}
		// The per-user client is cached per pod and only restores its
		// session at construction (buildLive). A client built on this
		// pod before the user finished signing in on a peer pod keeps
		// reporting "not connected" / "no MFA" from stale memory, so
		// the SPA's status poll disagrees pod-to-pod and the login
		// screens loop. The shared user_secrets store is authoritative:
		// a stored session wins even over a locally pending OTP
		// challenge (a peer pod may have completed the sign-in after
		// this pod cached the challenge - Restore drops it), and a
		// stored challenge refreshes a local one that a peer's second
		// password leg may have superseded.
		if store != nil && !lc.Authenticated() {
			if sess, err := secrets.LoadRivianSession(r.Context(), store, uid); err == nil && sess.UserSessionToken != "" {
				lc.Restore(sess)
			} else if pc, ok := lc.(pendingMFAClient); ok {
				if p, found := secrets.LoadPendingMFA(r.Context(), store, uid); found {
					pc.RestorePending(p)
				}
			}
		}
		needs, reason := lc.NeedsReauth()
		authd := lc.Authenticated()
		writeJSON(w, http.StatusOK, rivianStatusDTO{
			Enabled:           true,
			Authenticated:     authd,
			MFAPending:        lc.MFAPending(),
			Email:             lc.Email(),
			NeedsReauth:       needs,
			NeedsReauthReason: reason,
		})
		// Self-heal accounts that connected Rivian before the eager
		// prime shipped: when the user is authenticated but has no
		// vehicles row, kick off a one-shot prime in the background.
		// Fire-and-forget — the response has already gone out.
		if authd && !needs && sqlDB != nil && shouldAttemptPrime(uid) {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				var n int
				if err := sqlDB.QueryRowContext(ctx,
					`SELECT COUNT(*) FROM vehicles WHERE user_id = $1`, uid,
				).Scan(&n); err != nil {
					if logger != nil {
						logger.Warn("rivian lazy prime: vehicle count failed",
							"user_id", uid.String(), "err", err.Error())
					}
					return
				}
				if n > 0 {
					return
				}
				// A connected account with no vehicles is usually an
				// Authorized Driver whose invite hadn't been accepted
				// yet at sign-in. When one finally shows up, mark it:
				// that's the user leaving the stuck state on their own.
				if found, err := primeUserVehicles(ctx, lc, sqlDB, uid, logger); err == nil && found > 0 {
					recordFunnel(ctx, m, uid, funnelRivianVehiclesFound, "vehicles", found, "via", "lazy_prime")
				}
			}()
		}
	}
}

type rivianLoginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// pendingMFAClient is the subset of *LiveClient that can hand off an
// in-flight OTP challenge to a peer pod. Type-asserted (not on the
// Account interface) so the mock client, which has no cross-pod
// concern, stays unaffected and falls back to in-memory pending state.
type pendingMFAClient interface {
	PendingSnapshot() (rivian.PendingMFA, bool)
	RestorePending(rivian.PendingMFA)
}

func handleRivianLogin(reg rivian.AccountRegistry, store *secrets.Store, monitors *rivian.MonitorRegistry, mailer *email.Client, d *sql.DB, m *metrics.Metrics, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		lc := reg.For(uid)
		if lc == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		var req rivianLoginReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.Email = strings.TrimSpace(req.Email)
		if req.Email == "" || req.Password == "" {
			http.Error(w, "email and password required", http.StatusBadRequest)
			return
		}
		recordFunnel(r.Context(), m, uid, funnelRivianLoginAttempt)
		err := lc.Login(r.Context(), rivian.Credentials{Email: req.Email, Password: req.Password})
		switch {
		case errors.Is(err, rivian.ErrMFARequired):
			recordFunnel(r.Context(), m, uid, funnelRivianMFARequired)
			// Share the challenge so a peer pod can complete the OTP
			// leg even though this pod handled the password leg.
			if pc, ok := lc.(pendingMFAClient); ok {
				if snap, pending := pc.PendingSnapshot(); pending {
					if perr := secrets.SavePendingMFA(r.Context(), store, uid, snap); perr != nil {
						slog.WarnContext(r.Context(), "persist pending mfa failed", "err", perr.Error())
					}
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"authenticated": false,
				"mfa_pending":   true,
			})
			return
		case err != nil:
			// Log every login failure at WARN so the gate or upstream
			// class is visible without round-tripping the response body.
			fields := []any{"err", err.Error()}
			var ue *rivian.UpstreamError
			if errors.As(err, &ue) {
				fields = append(fields,
					"class", ue.Class.String(),
					"op", ue.Op,
					"http_status", ue.HTTPStatus,
					"ext_code", ue.ExtCode,
					"reason", ue.Reason,
				)
			}
			slog.WarnContext(r.Context(), "rivian login failed", fields...)
			recordFunnel(r.Context(), m, uid, funnelRivianLoginFailed, "class", upstreamClass(err))
			// A user_action class on the password leg means Rivian
			// rejected the email/password. Its gateway phrases this as
			// "session expired: User is unauthenticated", which reads as
			// a Rivolt bug to someone connecting for the first time.
			// Replace it with honest, actionable copy - the raw chain is
			// in the WARN line above.
			if ue != nil && ue.Class == rivian.ClassUserAction {
				writeJSON(w, httpStatusForUpstream(err), map[string]any{
					"error": "Rivian didn't accept that email or password. Double-check both and try again - this is your Rivian account login, the same one you use in the Rivian app.",
					"class": ue.Class.String(),
				})
				return
			}
			writeUpstreamError(w, err)
			return
		}
		// First-connect detection BEFORE the persist: if the user has
		// no stored session yet, this login is their initial Rivian
		// connection — notify the admin once it lands. Re-logins (token
		// rotation, password change) skip the notification.
		hadSession := hasStoredRivianSession(r.Context(), store, uid)
		// Fully authenticated — persist. A password-only success also
		// supersedes any in-flight OTP challenge; drop the shared
		// pending row so peers stop offering the dead token.
		if store != nil {
			_ = secrets.ClearPendingMFA(r.Context(), store, uid)
		}
		if perr := secrets.SaveRivianSession(r.Context(), store, uid, lc.Snapshot()); perr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": perr.Error()})
			return
		}
		n := finishRivianConnect(r.Context(), lc, monitors, mailer, d, m, uid, !hadSession, logger)
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"email":         lc.Email(),
			"vehicle_count": n,
		})
	}
}

// hasStoredRivianSession reports whether the user already had a
// Rivian session persisted - i.e. whether a successful sign-in is a
// re-login rather than their first connect.
func hasStoredRivianSession(ctx context.Context, store *secrets.Store, uid uuid.UUID) bool {
	existing, err := secrets.LoadRivianSession(ctx, store, uid)
	return err == nil && existing.UserSessionToken != ""
}

// upstreamClass is the bounded error class for funnel/log labels:
// the Rivian classification when there is one, else "unknown".
func upstreamClass(err error) string {
	var ue *rivian.UpstreamError
	if errors.As(err, &ue) {
		return ue.Class.String()
	}
	return "unknown"
}

// finishRivianConnect is the shared tail of a successful Rivian
// sign-in, reached from either the password leg or the MFA leg once
// the session is persisted: start the user's StateMonitor, seed the
// vehicles table, record the funnel and, on a first connect, tell the
// admin. Returns the number of vehicles found (-1 when the lookup
// itself failed) so the SPA can react to an empty account.
//
// An empty account is the case worth shouting about. The sign-in
// succeeded, so the UI says "connected", yet nothing will ever record:
// typically an Authorized Driver account whose invite was never
// accepted in the Rivian app. Before this was surfaced, those users
// sat in the no-vehicle bucket with nothing telling them why.
func finishRivianConnect(
	ctx context.Context,
	lc rivian.Account,
	monitors *rivian.MonitorRegistry,
	mailer *email.Client,
	d *sql.DB,
	m *metrics.Metrics,
	uid uuid.UUID,
	firstConnect bool,
	logger *slog.Logger,
) int {
	// Start (or no-op resume of) this user's StateMonitor so
	// the recorder + WS subscription run under their identity.
	if monitors != nil {
		monitors.Start(ctx, uid)
	}
	// Seed the local vehicles table from the Rivian account so
	// /api/vehicles/owned, ownership middleware, and the import
	// picker all see the user's cars immediately — without this
	// the table only fills lazily on the first Live-tab visit.
	n, err := primeUserVehicles(ctx, lc, d, uid, logger)
	if err != nil {
		n = -1
	}
	recordFunnel(ctx, m, uid, funnelRivianConnected, "first_connect", firstConnect, "vehicles", n)
	if n == 0 {
		recordFunnel(ctx, m, uid, funnelRivianNoVehicles, "first_connect", firstConnect)
	}
	if !firstConnect {
		return n
	}
	username := ""
	if d != nil {
		if u, derr := db.LookupUsername(ctx, d, uid); derr == nil {
			username = u
		}
	}
	subject := "Rivolt user connected Rivian account"
	var outcome string
	switch {
	case n > 0:
		outcome = "Vehicles, drives, and charges should start appearing\n" +
			"on the admin page within a few seconds.\n"
	case n == 0:
		subject = "Rivolt user connected Rivian - no vehicles found"
		outcome = "Rivian returned NO vehicles for this account, so nothing\n" +
			"will record. Most likely an Authorized Driver account whose\n" +
			"invite hasn't been accepted (the new account must accept it\n" +
			"and sign in to the Rivian app once). The user sees a warning\n" +
			"with a \"Check again\" button.\n"
	default:
		outcome = "The vehicle lookup failed right after sign-in (see the\n" +
			"\"rivian vehicles prime failed\" log line); it retries when\n" +
			"the user next opens the app.\n"
	}
	go notifyAdmin(context.Background(), mailer, logger, subject,
		"A user finished the Rivian sign-in step:\n\n"+
			"  Rivolt user: "+username+" ("+uid.String()+")\n"+
			"  Rivian email: "+lc.Email()+"\n\n"+
			outcome,
	)
	return n
}

// primeUserVehicles fetches the user's vehicles from Rivian and
// upserts them into the local vehicles table, returning how many rows
// it wrote. The error is the Rivian lookup failing; callers treat it
// as best-effort since the same upsert path runs lazily from
// /api/vehicles on next Live-tab visit. A nil error with a zero count
// is the important case: Rivian answered, and the account has no
// vehicles. Idempotent on the (user_id, rivian_vehicle_id) unique
// constraint.
func primeUserVehicles(
	ctx context.Context,
	lc rivian.Account,
	sqlDB *sql.DB,
	uid uuid.UUID,
	logger *slog.Logger,
) (int, error) {
	if sqlDB == nil || lc == nil {
		return 0, errors.New("prime: no database or client")
	}
	// rivian.Account doesn't expose Vehicles() — that lives on the
	// fuller Client interface that *LiveClient and *MockClient both
	// satisfy. Type-assert so we can reuse the same prime helper from
	// both login and MFA paths without coupling them to the concrete
	// LiveClient type.
	c, ok := lc.(rivian.Client)
	if !ok {
		return 0, errors.New("prime: client cannot list vehicles")
	}
	vs, err := c.Vehicles(ctx)
	if err != nil {
		if logger != nil {
			logger.Warn("rivian vehicles prime failed",
				"user_id", uid.String(), "err", err.Error())
		}
		return 0, err
	}
	primed := 0
	for i := range vs {
		if vs[i].ID == "" {
			continue
		}
		_, uerr := sqlDB.ExecContext(ctx, `
			INSERT INTO vehicles (user_id, rivian_vehicle_id, vin, display_name, model, model_year, pack_kwh)
			VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, 0)::int, NULLIF($7, 0)::double precision)
			ON CONFLICT (user_id, rivian_vehicle_id) DO UPDATE SET
				vin          = COALESCE(EXCLUDED.vin,          vehicles.vin),
				display_name = COALESCE(EXCLUDED.display_name, vehicles.display_name),
				model        = COALESCE(EXCLUDED.model,        vehicles.model),
				model_year   = COALESCE(EXCLUDED.model_year,   vehicles.model_year),
				-- Keep an existing pack_kwh: it may be the vehicle-reported
				-- capacity (write-through from observeBatteryCapacity), which
				-- must not be clobbered by this sync's InferPackKWh guess.
				-- Only seed when currently null.
				pack_kwh     = COALESCE(vehicles.pack_kwh,     EXCLUDED.pack_kwh),
				updated_at   = NOW()
		`, uid, vs[i].ID, vs[i].VIN, vs[i].Name, vs[i].Model, vs[i].ModelYear, vs[i].PackKWh)
		if uerr != nil {
			if logger != nil {
				logger.Warn("rivian vehicles prime upsert failed",
					"user_id", uid.String(),
					"rivian_vehicle_id", vs[i].ID,
					"err", uerr.Error())
			}
			continue
		}
		primed++
	}
	if logger != nil {
		logger.Info("rivian vehicles primed",
			"user_id", uid.String(), "returned", len(vs), "primed", primed)
	}
	// Rivian had vehicles but none landed: that's a database failure,
	// not an empty account, and must not read as "no vehicles found".
	if primed == 0 && len(vs) > 0 {
		return 0, errors.New("prime: every vehicle upsert failed")
	}
	return primed, nil
}

type rivianMFAReq struct {
	OTP string `json:"otp"`
}

func handleRivianMFA(reg rivian.AccountRegistry, store *secrets.Store, monitors *rivian.MonitorRegistry, mailer *email.Client, d *sql.DB, m *metrics.Metrics, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		lc := reg.For(uid)
		if lc == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		// The shared store is authoritative for both halves of the
		// dance. A peer pod may have completed the sign-in already
		// (e.g. a second password leg that no longer demanded MFA) -
		// short-circuit success instead of replaying a dead challenge,
		// which Rivian's gateway answers with a 500. A stored session
		// wins even over a locally pending challenge.
		if store != nil && !lc.Authenticated() {
			if sess, err := secrets.LoadRivianSession(r.Context(), store, uid); err == nil && sess.UserSessionToken != "" {
				lc.Restore(sess)
			}
		}
		if lc.Authenticated() {
			if store != nil {
				_ = secrets.ClearPendingMFA(r.Context(), store, uid)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"authenticated": true,
				"email":         lc.Email(),
			})
			return
		}
		// Prefer the stored challenge even when this pod has one in
		// memory: a peer's re-run password leg mints a new otpToken
		// and invalidates ours.
		if pc, ok := lc.(pendingMFAClient); ok {
			if p, found := secrets.LoadPendingMFA(r.Context(), store, uid); found {
				pc.RestorePending(p)
			}
		}
		if !lc.MFAPending() {
			http.Error(w, "no MFA challenge in flight; start with /login", http.StatusConflict)
			return
		}
		var req rivianMFAReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.OTP = strings.TrimSpace(req.OTP)
		if req.OTP == "" {
			http.Error(w, "otp required", http.StatusBadRequest)
			return
		}
		// Second leg of the MFA dance. Email is read from the
		// pending-state cached inside the client.
		if err := lc.Login(r.Context(), rivian.Credentials{OTP: req.OTP}); err != nil {
			fields := []any{"err", err.Error()}
			var ue *rivian.UpstreamError
			if errors.As(err, &ue) {
				fields = append(fields,
					"class", ue.Class.String(),
					"op", ue.Op,
					"http_status", ue.HTTPStatus,
					"ext_code", ue.ExtCode,
					"reason", ue.Reason,
				)
			}
			slog.WarnContext(r.Context(), "rivian mfa failed", fields...)
			recordFunnel(r.Context(), m, uid, funnelRivianMFAFailed, "class", upstreamClass(err))
			// Rivian answers a stale or superseded otpToken with a
			// bare INTERNAL_SERVER_ERROR; surfacing that chain reads
			// like a Rivolt outage. Translate the outage class into
			// something the user can act on - the raw error is in the
			// log line above.
			if ue != nil && ue.Class == rivian.ClassOutage {
				writeJSON(w, httpStatusForUpstream(err), map[string]any{
					"error": "Rivian didn't accept this code - it may have expired or been superseded. Cancel and sign in again to request a fresh one.",
					"class": ue.Class.String(),
				})
				return
			}
			writeUpstreamError(w, err)
			return
		}
		// Challenge consumed — drop the shared pending row so a stray
		// resubmit can't replay it. First-connect detection runs before
		// the persist, same as the password leg: most Rivian accounts
		// have MFA on, so this is where most first connects land.
		_ = secrets.ClearPendingMFA(r.Context(), store, uid)
		hadSession := hasStoredRivianSession(r.Context(), store, uid)
		if perr := secrets.SaveRivianSession(r.Context(), store, uid, lc.Snapshot()); perr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": perr.Error()})
			return
		}
		n := finishRivianConnect(r.Context(), lc, monitors, mailer, d, m, uid, !hadSession, logger)
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"email":         lc.Email(),
			"vehicle_count": n,
		})
	}
}

func handleRivianLogout(reg rivian.AccountRegistry, store *secrets.Store, monitors *rivian.MonitorRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		lc := reg.For(uid)
		if lc == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		lc.Logout()
		if monitors != nil {
			monitors.Stop(uid)
		}
		_ = secrets.ClearPendingMFA(r.Context(), store, uid)
		if perr := secrets.SaveRivianSession(r.Context(), store, uid, rivian.Session{}); perr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": perr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
	}
}

// handleRivianRefreshVehicles — POST /api/settings/rivian/refresh-vehicles
//
// Re-reads the vehicle list from Rivian for an already-connected
// account and seeds any new ones. Backs the "Check again" button the
// SPA shows when a sign-in found no vehicles: the usual fix happens in
// the Rivian app (accepting an Authorized Driver invite), and the user
// shouldn't have to sign out and back in to pick it up.
func handleRivianRefreshVehicles(reg rivian.AccountRegistry, store *secrets.Store, d *sql.DB, m *metrics.Metrics, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		lc := reg.For(uid)
		if lc == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		// Same cross-pod rehydrate as the status handler: the session
		// may have been minted on a peer pod.
		if store != nil && !lc.Authenticated() {
			if sess, err := secrets.LoadRivianSession(r.Context(), store, uid); err == nil && sess.UserSessionToken != "" {
				lc.Restore(sess)
			}
		}
		if !lc.Authenticated() {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "Rivian account is not connected"})
			return
		}
		n, err := primeUserVehicles(r.Context(), lc, d, uid, logger)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		recordFunnel(r.Context(), m, uid, funnelRivianVehicleCheck, "vehicles", n)
		if n > 0 {
			recordFunnel(r.Context(), m, uid, funnelRivianVehiclesFound, "vehicles", n, "via", "recheck")
		}
		writeJSON(w, http.StatusOK, map[string]any{"vehicle_count": n})
	}
}
