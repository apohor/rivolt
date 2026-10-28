// Package api wires the HTTP surface for Rivolt. It assembles routes,
// middleware, and handler dependencies into a single chi Mux.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/trace"

	"github.com/apohor/rivolt/internal/adminaudit"
	"github.com/apohor/rivolt/internal/adminhealth"
	"github.com/apohor/rivolt/internal/aibudget"
	"github.com/apohor/rivolt/internal/appleid"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/cfaccess"
	"github.com/apohor/rivolt/internal/changelog"
	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/clientip"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/idp"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/milestones"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/packhealth"
	"github.com/apohor/rivolt/internal/passkeys"
	"github.com/apohor/rivolt/internal/planleg"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/signupgate"
	"github.com/apohor/rivolt/internal/signuprequests"
	"github.com/apohor/rivolt/internal/statement"
	"github.com/apohor/rivolt/internal/trips"
	"github.com/apohor/rivolt/internal/weather"
)

// Pools (row-level security, stage 2). DB, Drives, Charges, Samples, Settings, Push, Trips, Secrets, PackHealth, AIBudget and
// ArchiveRows (the backup's archive reader; a request with no user fails the backup instead of dropping months) are built in
// main on the request pool when RIVOLT_REQUEST_DATABASE_URL is set (otherwise on the system pool, as before): they serve per-user
// routes and every table they touch carries the tenant policy. SystemDB, SystemSettings and SystemSecrets are the owner-role
// counterparts for the few places that must span users or run before a user is known. Every other store below is built on the
// system pool, each for a reason (DA asked for this list in the S2-2 review):
//
//   - Flags: install-wide switches, no tenant data. Request handlers read an in-memory snapshot; admin writes go through SystemDB.
//   - SettingsMgr: app_settings, which the request role cannot read (provider keys).
//   - SignupRequests: signup_requests, denied to the request role; signup and admin only.
//   - Users: the identity provider's admin API, not a database.
//   - Sessions, AppAuth, OIDC, Passkeys, AppleNotifications, AppleRevocation: sign-in, token and session paths. They run before a
//     user is known, or manage the session store the auth middleware itself reads; each statement names the user it acts for.
//   - Accounts, Monitors: the per-user Rivian clients and recorders (the monitor and recorder are system by policy).
//   - ArchiveRestorer: the admin restore of one user's archived month; admin, so system.
//   - Changelog, ChargersArchive, WeatherCache: not tenant data (a feed URL, the charger archive, an in-memory weather cache keyed by
//     place and time).
//
// Deps is the bag of dependencies the API router needs. Keep this
// small; avoid accumulating a "dependency soup" pattern.
//
// All per-user state (data-plane stores, Rivian client, monitor)
// is reached through factories/registries keyed by the request
// user's uid — there is no singleton "current user" anywhere in
// the API surface.
type Deps struct {
	// SampleRetentionMonths is RIVOLT_SAMPLE_RETENTION_MONTHS (0 keeps raw
	// samples forever). A restore skips samples older than the cutoff, which
	// the next retention sweep would delete anyway.
	SampleRetentionMonths int
	// ArchiveRows reads the telemetry archive (N19) so the backup download
	// carries full detail for months thinning has reduced; nil when the
	// archive is off, and the backup is the database alone.
	ArchiveRows samples.ArchiveRows
	// ArchiveRestorer puts one archived vehicle-month back in full detail
	// (POST /api/admin/archive/restore); nil when the archive is off.
	ArchiveRestorer ArchiveRestorer

	Rivian rivian.Client
	// Accounts hands out per-user *rivian.LiveClient instances. The
	// /api/settings/rivian sign-in surface resolves the request user
	// to its private client via Accounts.For(uid), so concurrent
	// Login/Restore from different sessions can no longer corrupt
	// each other's tokens. nil when the stub client is in use
	// (nothing to sign into).
	Accounts rivian.AccountRegistry
	// Monitors hands out per-user *rivian.StateMonitor instances
	// that own the websocket subscription + recorder for that user's
	// vehicles. nil in mock/stub modes (no recorder needed there).
	Monitors    *rivian.MonitorRegistry
	PushService *push.Service
	// Per-user data-plane factories. The router resolves req.user →
	// uid → factory.For(uid) once per request, so handlers always
	// see stores scoped to the caller. nil-safe: handlers gate on
	// the resolved *Store being non-nil before reading.
	Drives   *drives.Factory
	Charges  *charges.Factory
	Samples  *samples.Factory
	Settings *settings.Factory
	Push     *push.Factory
	Trips    *trips.Factory
	// PackHealth is the per-vehicle derived-pack-capacity store.
	// Not a factory because samples are scoped by vehicle_id, not
	// user_id; the handler authorizes via the vehicleScoped
	// ownership middleware before reading. Nil-safe: the endpoint
	// returns 503 when unset.
	PackHealth *packhealth.Store
	// SettingsMgr exposes install-wide AI provider config (keys,
	// default models). Install-wide because the deployer pays the
	// LLM bill for every user. May be nil; the admin track
	// repopulates it from app_settings and gates the handlers
	// behind requireAdmin.
	SettingsMgr *settings.Manager
	// Auth, when non-nil, gates /api/* behind a session cookie
	// or trusted-proxy header. Whether unauthenticated requests
	// are 401'd is governed by AuthEnforced below; with Auth
	// non-nil but AuthEnforced false, sessions still resolve into
	// request context but the API stays open (legacy single-tenant
	// UX so docker-compose upgrades don't lock anyone out).
	Auth *auth.Service
	// Sessions, when set, mounts the signed-in user's device list under
	// /api/auth/sessions (list, revoke one, sign out everywhere else).
	Sessions *SessionsAPI
	// AuthEnforced flips on the requireUser middleware for /api/*.
	// True when at least one real issuer is configured (OIDC,
	// trusted-proxy, or the debug bypass).
	AuthEnforced bool
	// TermsLookup replaces the database read of a user's accepted Terms in the server-side Terms gate (tests).
	TermsLookup func(ctx context.Context, uid uuid.UUID) (db.Terms, error)
	// ImpersonationDisabled mirrors $RIVOLT_IMPERSONATION_DISABLED.
	// true makes the impersonation middleware ignore
	// X-Rivolt-Impersonate outright (regardless of caller role) and
	// is surfaced via /api/config so the SPA hides the admin
	// "View as" control instead of offering a dead button.
	ImpersonationDisabled bool
	// AdminDisabled mirrors $RIVOLT_ADMIN_DISABLED: the public site
	// serves no admin surface. /admin* and /api/admin* answer 404 (even
	// for an admin session), /api/auth/me reports an admin as "user" so
	// the SPA hides the link, and impersonation is off. Off by default;
	// for the split to admin.rivolt.dev (scratchpad admin-split.md).
	AdminDisabled bool
	// AdminOnly is RIVOLT_ADMIN_ONLY: the admin deployment. Every request
	// must carry a valid Cloudflare Access JWT (Access; nil denies all),
	// and only the admin surface exists under /api.
	AdminOnly bool
	// AdminHealth feeds GET /api/admin/health-summary (nil: every section reads unavailable).
	AdminHealth *adminhealth.Service
	// PlanLegs caches live planner calls and caps them per user (nil: neither).
	PlanLegs *planleg.Cache
	Access   *cfaccess.Verifier
	// AdminDebugProbes mirrors $RIVOLT_ADMIN_DEBUG_PROBES: mounts the raw
	// Rivian probes under /api/admin (trips/plan/raw, trips/plan/diag,
	// gql/type, gql/enum, gql/raw), which send arbitrary queries to Rivian
	// as the admin. Off by default; an operator turns it on for a session.
	AdminDebugProbes bool
	// OIDC, when non-nil, mounts /api/auth/oidc/* — the third
	// auth issuer alongside static creds and trusted-proxy
	// header. nil disables the social-login button row in the
	// SPA but doesn't affect any other code path.
	OIDC *oidc.Service
	// AppleNotifications, when non-nil, is POST /api/auth/apple/notifications: Apple's
	// server-to-server notifications (consent revoked, Apple account deleted, relay email
	// off or on). Public (Apple calls it, no cookie), behind a body cap and a per-IP limit.
	AppleNotifications http.Handler
	// Passkeys, when non-nil, adds WebAuthn sign-in to Rivolt's email accounts: the management routes under /api/auth/passkeys
	// and, with Hydra and Kratos wired, the passkey sign-in on the Hydra login page. nil: no passkey route exists.
	Passkeys *passkeys.Service
	// PasskeyStore is the passkey tables on the system pool, present whenever there is a database, whether or not WebAuthn is
	// configured. "Secure my account" and support's reset delete credentials through it: a deployment with passkeys off (the admin
	// site, or prod before the enable) can still hold rows, and must not skip the deletion because the sign-in service is not built.
	PasskeyStore passkeys.Store
	// PasskeyAccount replaces the database read of the signed-in account's email and name for the passkey routes (tests).
	PasskeyAccount func(ctx context.Context, uid uuid.UUID) (mail, display string, err error)
	// PasskeyMailer replaces the mailer for the "a passkey was added" notice (tests).
	PasskeyMailer interface {
		Send(ctx context.Context, m email.Message) error
	}
	// IOSAppIDs ("TEAMID.bundle.id", RIVOLT_IOS_APP_IDS) publishes the
	// iPhone app's associated domain: the AASA file and /app/callback. Empty:
	// neither is served.
	IOSAppIDs []string
	// AppAuth, when non-nil (with OIDC), mounts the iPhone app's sign-in
	// under /api/auth/app. nil: /api/app/config reports app_login=false.
	AppAuth *AppAuth
	// Hydra, when non-nil along with Kratos, mounts the custom
	// login + consent handlers under /api/auth/hydra. This makes
	// Rivolt the bring-your-own-UI for an Ory Hydra OAuth2 / OIDC
	// provider — downstream apps (ArgoCD, Grafana, etc.) federate
	// against Hydra and Hydra calls back into us for the user-
	// facing login prompt.
	Hydra  *hydra.Client
	Kratos *kratos.Client
	// Redis, when set, makes the failed-login limiters count across replicas (RedisPrefix is
	// the install's key prefix). Nil leaves them per pod.
	Redis       *redis.Client
	RedisPrefix string
	// RivianExpiryEmails is true when the sender of the Rivian connection expiry reminders runs
	// (RIVOLT_RIVIAN_EXPIRY_EMAILS=true with a mailer and engagement email not off). The
	// Settings toggle for the reminders is mounted only then, so it never shows as switched on
	// where nothing is sent. The signed unsubscribe links are always mounted.
	RivianExpiryEmails bool
	// FailLimiterKey is the HMAC key for the limiter's Redis key names (FailLimiterKey of the
	// server's secret); without it the limiters stay per pod.
	FailLimiterKey []byte
	// PurgeUserCaches deletes what is held of a user outside Postgres (the Redis live-state
	// snapshots) once their account is deleted. Best effort, nil: nothing to purge.
	PurgeUserCaches func(ctx context.Context, uid uuid.UUID)
	// HydraRememberFor controls how long Hydra remembers the
	// user's login + consent before forcing a fresh prompt. Zero
	// defers to Hydra's login_session lifespan. Sourced from the
	// RIVOLT_OIDC_HYDRA_REMEMBER_FOR env var in main.go.
	HydraRememberFor time.Duration
	WebFS            fs.FS
	Version          string
	// Changelog serves the release notes (GET /api/changelog and
	// /changelog.atom); nil leaves both unmounted.
	Changelog *changelog.Source
	// DB is the shared Postgres pool. Used by request middleware
	// that needs to answer "does this session user own this
	// vehicle?" without round-tripping through a per-user store.
	// Safe to be nil in legacy code paths that predate the
	// ownership middleware; ownership enforcement is only wired
	// when DB is non-nil.
	// DB is what request handling reads and writes: with RIVOLT_REQUEST_DATABASE_URL set it is the request pool (row-level security
	// applies, app.user_id comes from the request's context), otherwise the same pool as SystemDB. A site that forgot to say
	// otherwise therefore fails closed (an empty answer), never open.
	DB *sql.DB
	// SystemDB is the owner-role pool for paths that legitimately span users or run before a user is known: login, session and
	// token lookups, signup, account deletion, admin, signed links, pre-auth GETs. Using it is a decision with a comment at the
	// site (DA's RLS ruling). nil means DB (tests, and every deployment without the request pool).
	SystemDB *sql.DB
	// RequestPoolOn is set by main when DB is a separate request pool (RIVOLT_REQUEST_DATABASE_URL). With it set, New refuses to build
	// unless every system-side store is set too: the fallbacks (sysDB, systemSettings, systemSecrets) would otherwise run the system
	// sites on the request pool, where, for example, account deletion's last-admin count sees only the caller.
	RequestPoolOn bool
	// LoginForgetter forgets Hydra's remembered login for a user at plain sign-out (an IdentityRevoker). nil: nothing to forget.
	LoginForgetter LoginForgetter
	// SessionRevoker revokes every session and app token of a user (sessions.Store.RevokeAll), for support's "secure this account".
	SessionRevoker interface {
		RevokeAll(ctx context.Context, userID uuid.UUID) (int64, error)
	}
	// RememberedRevoker forgets the identity provider's remembered logins for a user (an IdentityRevoker), for the same.
	RememberedRevoker RememberedRevoker
	// SystemSettings is the settings factory on the system pool, for the sweeps that run without a request (the statement and weekly
	// recap emails). nil means Settings (no request pool).
	SystemSettings *settings.Factory
	// AuditKey keys the salted hash of a user id in the "account deleted" log line (derived from the cookie secret; empty
	// means a random key for the process).
	AuditKey []byte
	// Logger is the structured logger used by middleware for
	// infrastructure-class warnings (DB errors on ownership check,
	// etc.). nil is fine; events are dropped.
	Logger *slog.Logger
	// Flags is the operational-flag store (kill switch, future
	// pause_digest / pause_push rows). When nil the /api/admin/*
	// routes return 503 but the server still boots — the flag
	// surface is non-critical to rendering the app.
	Flags *flags.Store
	// AIBudget enforces the per-user daily cap on LLM-backed
	// endpoints (trip advice, drive efficiency). nil disables the
	// gate — the cost backstop fails open so no-DB and test paths
	// keep working.
	AIBudget *aibudget.Store
	// Secrets is the envelope-encrypted per-user blob store
	// (see internal/crypto, internal/secrets). Holds the
	// sealed rivian.Session and, later, AI provider keys and
	// per-user VAPID private keys. nil is tolerated so tests
	// and the mock/stub client don't have to stand up a
	// sealer; the Rivian sign-in surface becomes read-only
	// when it's absent.
	Secrets *secrets.Store
	// SystemSecrets is the secret store on the system pool, for the admin route that refreshes another user's Rivian session.
	// nil means Secrets (no request pool).
	SystemSecrets *secrets.Store
	// Metrics is the Prometheus instrumentation surface. When non-nil
	// the router wires per-handler latency + count tracking and
	// mounts /metrics. nil disables both — useful for tests that
	// don't want the global default registry pollution.
	Metrics *metrics.Metrics
	// Users, when non-nil, provisions user identities in the
	// configured IdP on POST /api/admin/users and DELETE.
	// nil (or a disabled provider) skips IdP provisioning —
	// the rivolt DB row is still created, callers must create
	// the OIDC identity out-of-band. See internal/idp.
	Users idp.UserProvider
	// AppleRevocation revokes (or queues the revoke of) an account's
	// Sign in with Apple token before any account deletion. nil-safe.
	AppleRevocation *AppleRevocation
	// SignupRequests, when non-nil, enables the public-facing
	// "request beta access" form at POST /api/signup/request and
	// the admin review surface at /api/admin/signup-requests/*.
	// Approve mints a magic-link signup token on the request row
	// and (if Email is wired) emails the requester. POST /api/signup
	// redeems that token to create the user.
	SignupRequests *signuprequests.Store
	// Email, when non-nil, sends transactional mail (currently just
	// signup approvals) via the Resend HTTP API. nil disables the
	// approval-email send; the admin still gets the code in the
	// approval response so it can be forwarded manually.
	Email *email.Client
	// StatementSigner signs the one-click unsubscribe links of the
	// monthly statement email (nil disables the unsubscribe routes).
	StatementSigner *statement.Signer
	// WeeklySigner signs the weekly recap's unsubscribe links (a scope of its
	// own, so they never verify as statement links). nil disables the recap.
	WeeklySigner *statement.Signer
	// BaseURL is the install's public origin (e.g. https://rivolt.dev),
	// sourced from $RIVOLT_PUBLIC_BASE_URL, else $RIVOLT_BASE_URL (the
	// admin deployment sets the former: its own host is behind Cloudflare
	// Access, so links it emails must point at the public site). Used
	// for user-facing links in emails: the magic-link signup URL
	// (admin approval and auto-approve) and the monthly statement's
	// links. Safe to leave empty in tests — the signup link falls back
	// to a relative path so copy-paste-into-the-same-tab still works.
	BaseURL string
	// ValhallaProxy, when non-nil, mounts a same-origin reverse
	// proxy at /api/maps/valhalla/* that forwards to a self-hosted
	// Valhalla. /api/config advertises whether the proxy is
	// mounted so the SPA picks the right base URL at boot. nil
	// leaves the route unmounted; the SPA renders raw GPS chords
	// without snapping.
	ValhallaProxy http.Handler
	// Valhalla is the self-hosted router the server itself calls (the overnight search puts a sparse path on the road with it); nil: none.
	Valhalla *maps.Valhalla
	// TilesProxy, when non-nil, mounts a same-origin reverse
	// proxy at /api/maps/tiles/* that forwards to a self-hosted
	// PMTiles file server (nginx serving the .pmtiles bundle
	// with byte-range support). nil leaves the route unmounted;
	// the SPA falls back to CARTO's public dark raster basemap.
	TilesProxy http.Handler
	// ChargersArchive, when non-nil, exposes a server-side
	// chargers-along-corridor query at /api/maps/chargers-along.
	// Backed by an in-memory copy of chargers.pmtiles so the SPA
	// can replace its per-tile HTTP fan-out with one POST.
	ChargersArchive *chargers.Archive
	// Photon is the self-hosted geocoder client. Empty BaseURL on
	// the client disables; /api/geocode falls through to
	// Open-Meteo's city-level service.
	Photon *geocoding.PhotonClient
	// WeatherClient is the shared Open-Meteo HTTP client used by
	// trip-planner weather adjustment. Distinct from the per-callsite
	// weather.NewClient() instances used elsewhere because the
	// planner reuses this in tight loops with MemCache. nil disables
	// weather adjustment (the plan response just omits the field).
	WeatherClient *weather.Client
	// WeatherCache memoises FetchHour results across overlapping plan
	// requests. Keyed on coarsened (lat, lon, hour). nil disables
	// caching; FetchHourCached falls through to the client.
	WeatherCache *weather.MemCache
}

// sysDB is SystemDB, or DB where New has not defaulted it (code that takes a Deps without going through New).
func (d Deps) sysDB() *sql.DB {
	if d.SystemDB != nil {
		return d.SystemDB
	}
	return d.DB
}

// passkeyStore is the passkey tables for the security actions: PasskeyStore, else the sign-in service's own, else nil (no database).
func (d Deps) passkeyStore() passkeys.Store {
	if d.PasskeyStore != nil {
		return d.PasskeyStore
	}
	if d.Passkeys != nil {
		return d.Passkeys.Store()
	}
	return nil
}

// lodgingSource is Photon as the interface the overnight search takes; nil (not a typed nil) when none is configured.
func (d Deps) lodgingSource() lodgingSource {
	if d.Photon == nil || !d.Photon.Enabled() {
		return nil
	}
	return d.Photon
}

// pathRouter is Valhalla as the interface the overnight search takes; nil (not a typed nil) when none is configured.
func (d Deps) pathRouter() pathRouter {
	if d.Valhalla == nil {
		return nil
	}
	return d.Valhalla
}

// systemSettings is the settings factory background sweeps use: the system-pool one when there is a separate request pool.
func (d Deps) systemSettings() *settings.Factory {
	if d.SystemSettings != nil {
		return d.SystemSettings
	}
	return d.Settings
}

// systemSecrets is the secret store admin routes use: the system-pool one when there is a separate request pool.
func (d Deps) systemSecrets() *secrets.Store {
	if d.SystemSecrets != nil {
		return d.SystemSecrets
	}
	return d.Secrets
}

// checkSystemSide refuses a Deps that has the request pool on but is missing a system-side store (see RequestPoolOn).
func (d Deps) checkSystemSide() error {
	if !d.RequestPoolOn {
		return nil
	}
	var missing []string
	if d.SystemDB == nil {
		missing = append(missing, "SystemDB")
	}
	if d.Settings != nil && d.SystemSettings == nil {
		missing = append(missing, "SystemSettings")
	}
	if d.Secrets != nil && d.SystemSecrets == nil {
		missing = append(missing, "SystemSecrets")
	}
	if len(missing) > 0 {
		return fmt.Errorf("api.New: the request pool is on but %s is not set; the system sites would run on the request pool", strings.Join(missing, ", "))
	}
	// Present but wrong: the same object under both names is one pool, so the "system" sites would still run on the request pool.
	var same []string
	if d.SystemDB == d.DB {
		same = append(same, "SystemDB is DB")
	}
	if d.Settings != nil && d.SystemSettings == d.Settings {
		same = append(same, "SystemSettings is Settings")
	}
	if d.Secrets != nil && d.SystemSecrets == d.Secrets {
		same = append(same, "SystemSecrets is Secrets")
	}
	if len(same) > 0 {
		return fmt.Errorf("api.New: the request pool is on but %s; a system-side store must be a different one", strings.Join(same, ", "))
	}
	return nil
}

// logoutHandler is POST /api/auth/logout: Rivolt's own sign-out (revoke the session row, clear the cookie), preceded by forgetting
// what Hydra remembers about the user's login. Without that, "Sign out" leaves Hydra's remembered login in place and the next
// "Continue with Email" on a shared computer is signed straight back in with no password. The cost: the user's other browsers lose
// their remembered login too (they type the password next time). The iPhone app's bearer sign-out ends only the app token and
// leaves the browser's login alone; a viewing-as admin signs out of nothing of the target's.
//
// The Rivolt sign-out always happens: a Hydra failure is logged and not allowed to keep someone signed in (when Hydra is down the
// skip path that makes the remembered login dangerous is down with it).
func (d Deps) logoutHandler() http.HandlerFunc {
	observe := NewForgetObserver(d.Metrics)
	return func(w http.ResponseWriter, r *http.Request) {
		if uid, ok := auth.UserFromContext(r.Context()); ok && d.LoginForgetter != nil && !auth.ViaBearer(r.Context()) && !auth.IsImpersonating(r.Context()) {
			err := d.LoginForgetter.ForgetLogin(r.Context(), uid)
			observe.observe("logout", err)
			if err != nil {
				slog.WarnContext(r.Context(), "logout: could not clear the identity provider's remembered login", "err", logging.ScrubErr(err))
			}
		}
		d.Auth.ClearKratosCookie(w, r) // a browser may hold one (see ForgetLogin)
		d.Auth.Logout(w, r)
	}
}

// New builds the root mux with all routes mounted. It panics if the request pool is on but a system-side store is missing (a startup
// error: main builds the router once, before it listens).
func New(d Deps) http.Handler {
	if err := d.checkSystemSide(); err != nil {
		panic(err.Error())
	}
	if d.SystemDB == nil {
		d.SystemDB = d.DB
	}
	r := chi.NewRouter()

	// The real client address, from CF-Connecting-IP and only when a trusted
	// proxy sent it (chi's RealIP trusted forwarding headers from anyone, and
	// left every visitor behind the load balancer sharing one address).
	r.Use(clientip.Middleware)
	r.Use(middleware.RequestID)
	// HTTPMiddleware (a) copies chi's RequestID into our context so
	// the slog ContextHandler stamps it on every log line, and (b)
	// emits a single structured access-log entry per request. Must
	// run after RequestID, before Recoverer so panics still log.
	r.Use(logging.HTTPMiddleware)
	// otelTraceRoute updates the active span name (set by
	// otelhttp.NewHandler at the outer wrap) from "HTTP <method>"
	// to "HTTP <method> <chi-route-pattern>" once chi has resolved
	// the pattern. Cardinality stays bounded the same way the
	// metrics labels do — pattern, not raw URL.
	r.Use(otelTraceRoute)
	// Metrics middleware piggybacks on chi's RouteContext to record
	// per-pattern latency without exploding cardinality. Mounted
	// after logging so a panic logs first, then increments the 5xx
	// counter via Recoverer's recovered response.
	if d.Metrics != nil {
		r.Use(d.Metrics.HTTPMiddleware)
	}
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	if d.AdminDisabled {
		r.Use(adminDisabledMW)
	}
	// Carry DNT/GPC in the context for server-side product events.
	r.Use(dntContextMW)
	// NOTE: the global request timeout is applied per-group below,
	// not here. CSV imports, backups, and restores can legitimately
	// run for minutes on large exports, and a 30s ceiling cancels
	// the context mid-write — producing a 400/500 plus a
	// "superfluous WriteHeader" warning when the handler then tries
	// to write its own error. Carving those routes out of the
	// timeout is the minimal fix.
	r.Use(cors.Handler(cors.Options{
		// Self-hosted: we don't know the LAN hostname in advance, and
		// tightening CORS here doesn't add real security because this
		// server isn't exposed to the public internet by default. The
		// operator can put a reverse proxy in front for stricter rules.
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type", "Authorization"},
		// Credentials are intentionally NOT allowed here: the CORS
		// spec forbids combining Access-Control-Allow-Credentials:
		// true with Allow-Origin: *. The SPA is served same-origin
		// with the API in every supported deployment (docker
		// compose, DSM, k8s behind ingress), so the browser attaches
		// the session cookie on its own — CORS isn't involved.
		MaxAge: 300,
	}))

	// Admin deployment: Cloudflare Access first (403 without a valid JWT,
	// before any route), then nothing but the admin surface under /api.
	if d.AdminOnly {
		r.Use(cfaccess.Middleware(d.Access, accessExempt, d.Logger))
		r.Use(adminOnlyGateMW)
	}

	// Standalone public demo: nothing but the demo allowlist exists under
	// /api, whoever asks. Runs before authentication.
	if demo.OnlyEnabled() {
		r.Use(demoOnlyGateMW(demo.VehicleID()))
	}

	// Authentication middleware runs on every request so handlers
	// below can read auth.UserFromContext regardless of whether
	// auth is enforced. With auth unconfigured (nil Service) the
	// middleware is a no-op — the single-tenant legacy UX stays.
	if d.Auth != nil {
		r.Use(d.Auth.Middleware)
		// An iPhone app session may write only to a short list (app_write_allowlist.go);
		// it must run once the session is known and before any route.
		r.Use(appWriteAllowlistMW)
		// Public demo (preview only): allowlisted anonymous GETs are
		// served as the demo user. Not installed unless RIVOLT_DEMO.
		if demo.Enabled() {
			if d.Metrics != nil {
				demoClientKeyObserver = func(source string) { d.Metrics.DemoClientKeyTotal.WithLabelValues(source).Inc() }
				demoRateLimitedObserver = func(routeClass, scope string) {
					d.Metrics.DemoRateLimitedTotal.WithLabelValues(routeClass, scope).Inc()
				}
			}
			r.Use(demoAccessMW(demo.UserID(), demo.VehicleID(), newDemoLimiter(), newDemoGlobalLimiter()))
		}
		// impersonationMW must run immediately after auth resolves
		// the real identity and before any route reads
		// auth.UserFromContext, so the swap it performs is
		// invisible to every downstream handler/store/RLS check.
		// System pool (RLS policy): the caller's role is read before the request's user is settled (the view-as swap happens here), and
		// the audit row is an admin table.
		r.Use(impersonationMW(func(ctx context.Context, uid uuid.UUID) (string, error) {
			if d.SystemDB == nil {
				return "", nil
			}
			return db.RoleFor(ctx, d.SystemDB, uid) // system pool: identity lookup
		}, func() bool { return d.ImpersonationDisabled || d.AdminDisabled },
			func(ctx context.Context, admin, target uuid.UUID, ip string) error {
				if d.SystemDB == nil {
					return nil
				}
				return adminaudit.New(d.SystemDB).NoteImpersonationStart(ctx, admin, target, ip, 30*time.Minute) // system pool: admin audit
			}))
		if d.AdminOnly {
			r.Use(adminOnlyViewAsMW)
		}
	} else if d.AdminOnly {
		r.Use(adminOnlyViewAsMW) // no auth, so nothing can impersonate: user routes stay closed
	}

	// /metrics is deliberately NOT mounted on this router: the Ingress
	// routes every path here, so a route here is public. Prometheus reads
	// it from its own listener (RIVOLT_METRICS_ADDR, default :9090; see
	// cmd/rivolt), which the Ingress does not reach.

	// Same-domain redirect so email links stay aligned with the sender domain (DMARC/Resend warning).
	r.Get("/discord", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://discord.com/invite/kdKqbK3pz", http.StatusFound)
	})

	// Kubernetes-style probe endpoints at the root (matches kube-apiserver
	// convention; kubelet sends no auth headers so they're unauthenticated
	// by design). /healthz is the bare alive-check: no I/O, no deps —
	// only fails if the HTTP handler can't run. /readyz pings Postgres
	// with a tight timeout so a DB outage drops the pod from Service
	// endpoints without restart-looping the binary.
	r.Get("/healthz", handleHealthz())
	r.Get("/readyz", handleReadyz(d.SystemDB)) // system pool: a pre-auth probe of the owner connection

	// Release notes are public and cheap (a cached copy), but limited per IP
	// like the other unauthenticated reads.
	clLimiter := newIPLimiter(60, time.Minute, time.Hour)

	r.Route("/api", func(r chi.Router) {
		// Health + auth endpoints stay reachable without a session,
		// otherwise the browser has no way to log in. /api/health is
		// kept as a stable alias of /healthz for the preview-version
		// poller, external monitoring, and any docs that reference it.
		r.Get("/health", handleHealth(d.Version))
		// /api/config advertises optional runtime knobs to the SPA
		// (which same-origin proxies are mounted, feature flags,
		// admin-configurable GPS thresholds). Public so the SPA can
		// fetch it before login; reveals no user-scoped data.
		r.Get("/config", handleConfig(d.ValhallaProxy != nil, d.TilesProxy != nil, d.SettingsMgr != nil && d.SettingsMgr.Analyzer() != nil, d.ImpersonationDisabled || d.AdminDisabled, d.Flags, d.SettingsMgr))
		// The charging-networks comparison page's data: public reference data (plans as each
		// network states them, typical observed prices with their label and month).
		r.With(clLimiter.Middleware).Get("/charging-networks", handleChargingNetworks(time.Now))
		// Release notes for the /changelog page. The admin and demo-only
		// gates 404 it there, like every non-allowlisted /api path.
		if d.Changelog != nil {
			cl := handleChangelogJSON(d.Changelog)
			r.With(clLimiter.Middleware).Get("/changelog", cl)
			r.With(clLimiter.Middleware).Head("/changelog", cl)
		}
		// The iPhone app's server check (iOS design §2). Not on the admin
		// deployment: the app never signs in there.
		if !d.AdminOnly {
			r.Get("/app/config", handleAppConfig(d))
		}
		if d.Auth != nil {
			r.Route("/auth", func(r chi.Router) {
				r.Post("/logout", d.logoutHandler())
				r.Get("/me", handleMeEnrichedWith(d.Auth, d.DB, d.AdminDisabled, d.unitsFor(), d.passkeyAPI() != nil && !d.AdminOnly))
				if d.Sessions != nil && !d.AdminOnly {
					r.Get("/sessions", d.Sessions.List)
					r.Post("/sessions/revoke-others", d.Sessions.RevokeOthers)
					r.Delete("/sessions/{id}", d.Sessions.Revoke)
				}
				if as := d.accountSecurityAPI(); as != nil {
					// Change the password, or secure the account: the owner's way back in from a hijacked session. Not on the
					// admin deployment; the app token and view-as are refused in the handler.
					asLimiter := newIPLimiter(20, time.Minute, time.Hour)
					r.With(asLimiter.Middleware).Post("/password", as.change)
					r.With(asLimiter.Middleware).Post("/secure", as.secure)
				}
				if d.OIDC != nil {
					d.OIDC.Mount(r)
				}
				if pk := d.passkeyAPI(); pk != nil && !d.AdminOnly {
					pkLimiter := newIPLimiter(30, time.Minute, time.Hour)
					r.With(pkLimiter.Middleware).Route("/passkeys", func(r chi.Router) {
						r.Get("/", pk.list)
						r.Post("/register/begin", pk.registerBegin)
						r.Post("/register/finish", pk.registerFinish)
						r.Patch("/{id}", pk.rename)
						r.Delete("/{id}", pk.remove)
					})
				}
				if d.AppleNotifications != nil && !d.AdminOnly {
					// A real notification is ~1 KB and Apple sends few: 30 a minute per address is generous.
					appleLimiter := newIPLimiter(30, time.Minute, time.Hour)
					r.With(appleLimiter.Middleware, maxBodyBytes(appleid.MaxNotificationBody)).Post("/apple/notifications", d.AppleNotifications.ServeHTTP)
				}
				// The iPhone app's sign-in: start → the usual OIDC sign-in →
				// finish (one-time code to the app's fixed callback) → token.
				if d.AppAuth != nil && d.OIDC != nil && !d.AdminOnly {
					// Per IP, per pod (DA on #769): start writes a row for an
					// anonymous caller; token redeems codes.
					startLimiter := newIPLimiter(10, time.Minute, time.Hour)
					tokenLimiter := newIPLimiter(10, time.Minute, time.Hour)
					r.Route("/app", func(r chi.Router) {
						r.With(startLimiter.Middleware).Get("/start", d.AppAuth.handleAppStart(d))
						r.Get("/finish", d.AppAuth.handleAppFinishPage(d))
						r.Post("/finish", d.AppAuth.handleAppFinish())
						r.With(tokenLimiter.Middleware).Post("/token", d.AppAuth.handleAppToken(d))
					})
				}
				// Hydra OIDC bridge: Rivolt is the login + consent UI
				// for our Ory Hydra OAuth2 server. Both Hydra (admin
				// API) and Kratos (public auth API) must be wired
				// for this to be useful. Mounting just one would
				// leave a half-broken flow, so guard on both.
				if d.Hydra != nil && d.Hydra.Enabled() &&
					d.Kratos != nil && d.Kratos.Enabled() {
					failures := newFailLimiter(10, 15*time.Minute).withRedis(d.Redis, d.RedisPrefix, "login", d.FailLimiterKey, d.failLimiterFallback())
					if d.Metrics != nil {
						failures.onLockout = d.Metrics.LoginLockoutsTotal.Inc
					}
					hd := hydraDeps{
						Hydra:       d.Hydra,
						Kratos:      d.Kratos,
						Logger:      d.Logger,
						RememberFor: d.HydraRememberFor,
						// 10 wrong passwords per address per 15 minutes, from anywhere.
						Failures: failures,
					}
					if pk := d.passkeyAPI(); pk != nil {
						pk.Failures, pk.Remember = failures, hd.rememberForSeconds()
						hd.Passkeys = pk
					}
					mountHydraBridge(r, hd)
				}
			})
		}
		runStatement, runWeekly := emailSweepsAllowed(d)
		if runStatement {
			startStatementJob(d, d.StatementSigner)
		}
		// Weekly recap email (opt-in, internal/weekly): the sweep, and its signed
		// one-click unsubscribe, public like the statement's.
		if d.SystemDB != nil && d.WeeklySigner != nil && d.Email != nil && !demo.OnlyEnabled() {
			if runWeekly {
				startWeeklyJob(d)
			}
			wu := &weeklyUnsub{db: d.SystemDB, signer: d.WeeklySigner, now: time.Now} // system pool: a signed link, no session
			wl := newIPLimiter(30, time.Minute, time.Hour)
			wl.key = demoClientKey
			r.With(wl.Middleware).Get("/weekly-recap/unsubscribe", wu.get)
			r.With(wl.Middleware, maxBodyBytes(4096)).Post("/weekly-recap/unsubscribe", wu.post)
		}
		// Monthly statement email: signed one-click unsubscribe.
		// Public (no session), per-IP limited, GET shows a page and
		// POST performs it (RFC 8058). 404 without a signer or DB.
		if d.SystemDB != nil && d.StatementSigner != nil {
			stm := newStatementAPI(d.SystemDB, d, d.StatementSigner) // system pool: a signed link, no session
			unsubLimiter := newIPLimiter(30, time.Minute, time.Hour)
			unsubLimiter.key = demoClientKey // the trusted CF-Connecting-IP, not the ingress address
			r.With(unsubLimiter.Middleware).Get("/statement-email/unsubscribe", stm.unsubscribeGet)
			r.With(unsubLimiter.Middleware, maxBodyBytes(4096)).Post("/statement-email/unsubscribe", stm.unsubscribePost)
			// Welcome and setup emails: the same kind of signed one-click
			// link, scoped by its purpose so the two never cross.
			oe := &onboardingEmailAPI{db: d.SystemDB, signer: d.StatementSigner, now: time.Now} // system pool: a signed link, no session
			r.With(unsubLimiter.Middleware).Get("/onboarding-email/unsubscribe", oe.unsubscribeGet)
			r.With(unsubLimiter.Middleware, maxBodyBytes(4096)).Post("/onboarding-email/unsubscribe", oe.unsubscribePost)
			// The Rivian connection expiry reminders: the same, with their own purpose.
			re := &rivianExpiryEmailAPI{db: d.SystemDB, signer: d.StatementSigner, now: time.Now} // system pool: a signed link, no session
			r.With(unsubLimiter.Middleware).Get("/rivian-expiry-email/unsubscribe", re.unsubscribeGet)
			r.With(unsubLimiter.Middleware, maxBodyBytes(4096)).Post("/rivian-expiry-email/unsubscribe", re.unsubscribePost)
		}
		// Public demo discovery (preview only; 404 when off).
		if demo.Enabled() {
			r.Get("/demo", handleDemoInfo)
			// Preset trips for the demo planner: static GETs behind the
			// same per-client then install-wide demo limits.
			dl, dg := newDemoLimiter(), newDemoGlobalLimiter()
			r.With(dl.Middleware, dg.Middleware).Get("/demo/trips", handleDemoTrips)
			r.With(dl.Middleware, dg.Middleware).Get("/demo/trips/{id}/plan", handleDemoTripPlan)
		}
		// Public sign-up: validate invite code and create account.
		// Deliberately outside the requireUser group — the user
		// does not have a session yet.
		if d.SignupRequests != nil && !signupgate.Disabled() {
			r.With(maxBodyBytes(maxJSONBody)).Post("/signup", handleSignup(d.SystemDB /* system pool: signup, no user yet */, d.SignupRequests, d.Users, signupSessionIssuer(d.Auth), d.Metrics, d.Logger, signupCapOK(d)))
		}
		// Public "request access" form companion to /signup. Anyone
		// without an invite code can submit an email + short note;
		// admins decide the row in /admin/signup-requests.
		//
		// Per-visitor in-memory limiter: 5 requests/hour per real client
		// address (the clientip middleware), plus a global backstop of 200 an
		// hour per pod against floods. The real DDoS defense is the Cloudflare edge rule;
		// this is the in-pod fallback if CF ever lets traffic
		// through. Beta scale: 5/hr is generous for a human filling
		// out the form and trivial to enforce.
		if d.SignupRequests != nil && !signupgate.Disabled() {
			signupReqLimiter, signupReqGlobal := newSignupRequestLimiters(signupReqPerIPPerHour, signupReqGlobalPerHour)
			r.With(signupReqLimiter.Middleware, signupReqGlobal.Middleware, maxBodyBytes(maxJSONBody)).Post(
				"/signup/request",
				handleSignupRequestCreate(d.SignupRequests, d.Email, d.Logger, newAutoApprove(d)),
			)
			// Magic-link prefill — the SPA hits this on /signup?token=…
			// to resolve the token to its email before rendering the
			// password form.
			r.Get("/signup/token/{token}", handleSignupTokenLookup(d.SignupRequests))
		}

		// First-party product events (counts and enums only). Public with
		// an optional session; per-IP limited, 2 KB cap. Off unless
		// RIVOLT_EVENTS=true.
		if d.SystemDB != nil {
			startEventsPurge(d.SystemDB) // regardless of the flag: retention is a promise. System pool: a retention job across users
		}
		if d.SystemDB != nil && eventsEnabled() {
			evLimiter := newIPLimiter(eventsRatePerMin, time.Minute, time.Hour)
			r.With(evLimiter.Middleware, maxBodyBytes(eventsMaxBody)).Post("/events", handleEvents(newPGEventsSink(d.SystemDB))) // system pool: anonymous callers have no user, and the row's user is optional
		}

		// Everything else sits behind requireUser when auth is
		// enforced. Unenforced mode (no issuer wired) keeps the
		// API open — legacy single-tenant docker-compose UX.
		r.Group(func(r chi.Router) {
			if d.AuthEnforced {
				r.Use(requireUserMW)
				r.Use(requireTermsMW(termsLookupFor(d)))
			}
			// Per-user Rivian sessions are hydrated at boot via the
			// AccountRegistry sweep in main.go (see runServer's call
			// to rivian.NewLiveAccountRegistry + secrets.LoadRivianSession
			// loop). The legacy rivianHydrateMW that lazily restored on
			// the first authenticated request is gone — it shared a
			// single LiveClient across all callers and was the root
			// cause of the boot-restart-loses-drives bug + the
			// last-write-wins multi-user data corruption.
			//
			// 30s is plenty for regular JSON endpoints; it keeps
			// stuck Rivian calls from pinning a connection. Bulk
			// data routes (import / backup / restore) live in a
			// second group below without this timeout — large CSV
			// exports can take minutes. AI-bound routes (efficiency
			// POST in particular) also live in a sibling group below
			// with a 5-minute timeout because "thinking" LLMs like
			// Gemini 3.1 Pro Preview routinely take >30s to respond
			// on non-trivial prompts (502 when CF saw the chi
			// middleware kill the connection mid-call on
			// 2026-05-07).
			r.Use(middleware.Timeout(30 * time.Second))
			r.Use(maxBodyBytes(maxJSONBody))

			// Same-origin Valhalla proxy. Mounted only when the
			// operator configured RIVOLT_VALHALLA_BASE_URL —
			// otherwise the route is absent entirely and the SPA
			// renders raw GPS chords without snapping. chi's Mount
			// strips the prefix from r.URL.Path itself; the
			// proxy.Director then forwards as-is.
			if d.ValhallaProxy != nil {
				r.Mount("/maps/valhalla", valhallaMount(d.ValhallaProxy))
			}
			if d.TilesProxy != nil {
				r.Mount("/maps/tiles", tilesMount(d.TilesProxy))
			}
			// Server-side chargers-along-corridor query. Replaces the
			// SPA's PMTiles per-tile fan-out (hundreds of HTTP range
			// reads per planner re-render) with one POST that returns
			// the decoded POI list.
			if d.ChargersArchive != nil {
				r.With(mapsUserLimiter(mapsChargersAlongPerMin).Middleware).Post("/maps/chargers-along", handleChargersAlong(d.ChargersArchive))
			}
			// Onboarding — marks the first-run stepper as done for the
			// current user. Called by the frontend when the user
			// reaches the final step and clicks "Done".
			r.Post("/onboarding/complete", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleOnboardingComplete(d.DB, d.Metrics)(uid, w, r)
			}))

			// Terms acceptance: records the version the user agreed to.
			// Not on the demo allowlist (POST); nothing is blocked on it.
			r.Post("/terms/accept", withUser(handleTermsAccept(d.DB)))

			// Milestone cards (internal/milestones): read, and dismiss one.
			if d.DB != nil {
				ms := &milestonesAPI{store: milestones.NewStore(d.DB), now: time.Now}
				r.Get("/milestones", withUser(ms.list))
				r.Post("/milestones/dismiss", withUser(ms.dismiss))
			}

			r.Route("/push", func(r chi.Router) {
				r.Get("/vapid-key", handlePushVAPIDKey(d.PushService))
				r.Get("/status", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePushStatus(d.PushService, d.Push.For(uid))(w, r)
				}))
				r.Post("/subscribe", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePushSubscribe(d.Push.For(uid))(w, r)
				}))
				r.Post("/unsubscribe", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePushUnsubscribe(d.Push.For(uid))(w, r)
				}))
				r.Post("/test", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePushTest(d.PushService, d.Push.For(uid))(w, r)
				}))
				r.Post("/device", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePushDevice(d.Push.For(uid))(w, r)
				}))
				r.Post("/optin", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePushOptIn(d.Push.For(uid))(w, r)
				}))
				r.Post("/off", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePushOff(d.Push.For(uid))(w, r)
				}))
				// The iPhone app (bearer only; see push_apns.go).
				r.Post("/apns/register", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleAPNsRegister(d.PushService, d.Push.For(uid), func(reason string) {
						if d.Metrics != nil {
							d.Metrics.APNsRegisterRefusedTotal.WithLabelValues(reason).Inc()
						}
					})(w, r)
				}))
				r.Post("/apns/preferences", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleAPNsPreferences(d.PushService, d.Push.For(uid))(w, r)
				}))
				apnsActivityLimiter := mapsUserLimiter(30)
				r.With(apnsActivityLimiter.Middleware).Post("/apns/activity", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleAPNsActivity(d.PushService, d.Push.For(uid))(w, r)
				}))
				r.With(apnsActivityLimiter.Middleware).Post("/apns/activity/end", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleAPNsActivityEnd(d.PushService, d.Push.For(uid))(w, r)
				}))
				apnsActivityTestLimiter := mapsUserLimiter(3)
				r.With(apnsActivityTestLimiter.Middleware).Post("/apns/activity/test", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleAPNsActivityTest(d.PushService, d.Push.For(uid))(w, r)
				}))
				apnsTestLimiter := mapsUserLimiter(6)
				r.With(apnsTestLimiter.Middleware).Post("/apns/test", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleAPNsTest(d.PushService, d.Push.For(uid))(w, r)
				}))
				r.Post("/apns/unregister", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleAPNsUnregister(d.PushService, d.Push.For(uid))(w, r)
				}))
				r.Get("/notifications/{id}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePushNotification(d.Push.For(uid))(w, r)
				}))
			})

			// Rivian live endpoints. /api/vehicles returns [] when no real
			// client is configured (the stub returns ErrNotImplemented);
			// other errors surface as 502 so the UI can show them.
			//
			// Vehicle-scoped routes below sit behind
			// requireVehicleOwnershipMW when the shared DB pool is
			// wired: that check converts "unknown vehicle for this
			// user" into a 404 before any handler runs, so
			// /api/state/{someone-elses-id} can't read tenant data
			// even if Rivian upstream would honor the call.
			// The insight endpoints take ?units=imperial|metric; without one they follow the account's setting (settings_units.go).
			defaultUnits := defaultUnitsParam(d.accountUnitsLookup())
			var vehicleScoped func(http.Handler) http.Handler
			if d.DB != nil {
				vehicleScoped = requireVehicleOwnershipMW(d.DB, d.Logger)
			} else {
				vehicleScoped = func(next http.Handler) http.Handler { return next }
			}
			r.Get("/vehicles", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				hasSession := d.rivianSessionCheck(uid)
				handleVehicles(clientFor(d, uid), monitorFor(d, uid), d.DB, d.Logger, hasSession)(w, r)
			}))
			// /api/vehicles/owned reads straight from the local
			// vehicles table — used by the import picker so the
			// user can pick a target vehicle even when Rivian is
			// momentarily unreachable. Excludes the legacy
			// electrafi-* synthetic rows so they can't be picked
			// again as import targets.
			r.Get("/vehicles/owned", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleOwnedVehicles(d.DB)(uid, w, r)
			}))
			// Per-vehicle profile (tire type, wheel size, accessories,
			// default extra load, frequently_tows). Stored in
			// vehicles.metadata.profile JSONB; consumed by the
			// efficiency analyzer. Path param is the Rivian gateway
			// id; the handler resolves it to the internal UUID and
			// scopes the read/write by user.
			r.With(vehicleScoped).Get("/vehicles/{vehicleID}/profile", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleVehicleProfileGet(d.DB)(uid, w, r)
			}))
			r.With(vehicleScoped).Put("/vehicles/{vehicleID}/profile", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleVehicleProfilePut(d.DB)(uid, w, r)
			}))
			// A fresh link to a software release's notes: Rivian's own is a one-hour signed URL, so it is fetched when tapped
			// (see ota_notes.go) and never stored or served with the vehicle list.
			otaNotes := handleOTANotes(func(uid uuid.UUID) rivian.OTASource {
				src, _ := clientFor(d, uid).(rivian.OTASource)
				return src
			}, newOTANotesCache(), d.Logger)
			r.With(vehicleScoped).Get("/vehicles/{vehicleID}/ota/notes", withUser(otaNotes))
			// Lease tracker: terms CRUD and the computed standing. 404 when
			// no lease is set.
			r.With(vehicleScoped).Get("/vehicles/{vehicleID}/lease", withUser(handleLeaseGet(d.DB)))
			r.With(vehicleScoped).Put("/vehicles/{vehicleID}/lease", withUser(handleLeasePut(d.DB)))
			r.With(vehicleScoped).Delete("/vehicles/{vehicleID}/lease", withUser(handleLeaseDelete(d.DB)))
			r.With(vehicleScoped).Get("/vehicles/{vehicleID}/lease/status", withUser(handleLeaseStatus(d.DB, time.Now)))
			// Now-card state fields (location_fix_at, connection,
			// state_since, tire min). Read-only.
			if d.DB != nil {
				na := nowAPI{
					snapshot: newNowSnapshotter(d.DB),
					connection: func(uid uuid.UUID) string {
						paused := d.Flags != nil && d.Flags.KillSwitch().Paused
						needs := false
						if d.Accounts != nil {
							if st, ok := freshAccountState(auth.WithUser(context.Background(), uid), d.Accounts, uid); ok {
								// From the users row, not this pod's memory.
								needs = st.NeedsReauth
							} else if a := d.Accounts.For(uid); a != nil {
								needs, _ = a.NeedsReauth()
							}
						}
						return connectionState(envReplay(), paused, needs)
					},
					frameAt: func(ctx context.Context, uid uuid.UUID, rivianID string) *time.Time {
						return monitorFor(d, uid).VehicleStateFrameAt(ctx, rivianID)
					},
					ambient: ambientLookup(ambientOptIn(d.Settings), d.WeatherClient, newAmbientCache(), d.Logger, isDemoUser),
					now:     time.Now,
				}
				r.With(vehicleScoped).Get("/vehicles/{vehicleID}/now", withUser(na.handle))
			}
			// Parked-drain card (B8): per-vehicle drain, idle-awake and
			// sleep stats from vehicle_state. Read-only.
			if d.DB != nil {
				dr := newDrainAPI(newDrainStore(d.DB))
				r.With(vehicleScoped).Get("/vehicles/{vehicleID}/drain", withUser(dr.handle))
			}
			// This week's short sleeps against this truck's own last 30 days (display only).
			r.With(vehicleScoped).Get("/vehicles/{vehicleID}/sleep-health", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSleepHealth(uid, d.Samples.For(uid), time.Now)(w, r)
			}))
			r.With(vehicleScoped).Get("/vehicles/{vehicleID}/pack-health", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handlePackHealthGet(d.DB, d.PackHealth)(uid, w, r)
			}))
			// One truck's own efficiency and battery trend by month (the Truck page chart).
			r.With(vehicleScoped, defaultUnits).Get("/vehicles/{vehicleID}/trends", withUser(handleVehicleTrends(d.DB)))
			r.With(vehicleScoped, defaultUnits).Get("/vehicles/{vehicleID}/cold-range", withUser(handleColdRange(d.DB)))
			r.With(vehicleScoped).Get("/state/{vehicleID}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleVehicleState(clientFor(d, uid), monitorFor(d, uid), d.rivianSessionCheck(uid))(w, r)
			}))
			r.With(vehicleScoped).Get("/state/{vehicleID}/debug", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleVehicleStateDebug(clientFor(d, uid))(w, r)
			}))
			r.With(vehicleScoped).Get("/state/{vehicleID}/fresh", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleVehicleStateFresh(clientFor(d, uid), d.rivianSessionCheck(uid))(w, r)
			}))
			r.With(vehicleScoped).Get("/live-session/{vehicleID}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleLiveSession(clientFor(d, uid), monitorFor(d, uid), d.Settings.For(uid))(w, r)
			}))
			r.With(vehicleScoped).Get("/live-drive/{vehicleID}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleLiveDrive(monitorFor(d, uid))(w, r)
			}))
			r.Get("/charging-schema", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargingSchemaProbe(clientFor(d, uid))(w, r)
			}))
			// PoC: live battery pack cell temperatures via Parallax.
			r.With(vehicleScoped).Get("/battery-temp/{vehicleID}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleBatteryTempProbe(clientFor(d, uid))(w, r)
			}))
			// RE tool: capture raw Parallax frames for a topic (default
			// dynamics.vehicle.gnss) — ?rvms=a,b&max=N.
			r.With(vehicleScoped).Get("/parallax-raw/{vehicleID}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleParallaxRawProbe(clientFor(d, uid))(w, r)
			}))
			// Capability gate: SupportedFeatures per vehicle, incl.
			// PX_STATE_ALL (Parallax vehicle-state) availability.
			r.Get("/supported-features", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSupportedFeaturesProbe(clientFor(d, uid))(w, r)
			}))
			r.Get("/charging-field/{field}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargingFieldProbe(clientFor(d, uid))(w, r)
			}))
			r.Get("/charging-frames", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargingFrames(clientFor(d, uid))(w, r)
			}))

			// Rivian account management. Only wired when a live client is
			// present; with the stub/mock these return 404.
			r.Route("/settings/rivian", func(r chi.Router) {
				r.Get("/", handleRivianStatus(d.Accounts, d.Secrets, d.DB, d.Metrics, d.Logger, func() bool {
					return d.Flags != nil && d.Flags.KillSwitch().Paused
				}))
				r.Post("/login", handleRivianLogin(d.Accounts, d.Secrets, d.Monitors, d.Email, d.DB, d.Metrics, d.Logger))
				r.Post("/mfa", handleRivianMFA(d.Accounts, d.Secrets, d.Monitors, d.Email, d.DB, d.Metrics, d.Logger))
				r.Post("/refresh-vehicles", handleRivianRefreshVehicles(d.Accounts, d.Secrets, d.DB, d.Metrics, d.Logger))
				r.Post("/logout", handleRivianLogout(d.Accounts, d.Secrets, d.Monitors))
			})

			// Home electricity cost settings, applied locally to estimate
			// the price of sessions Rivian reports as free (home AC, L2,
			// non-RAN public chargers).
			settingsWriteLimiter := mapsUserLimiter(30)
			r.Route("/settings/charging", func(r chi.Router) {
				r.Get("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingSettingsGet(d.Settings.For(uid))(w, r)
				}))
				r.Put("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingSettingsPut(d.Settings.For(uid))(w, r)
				}))
				// The home rate only, merged field by field; the one settings write the
				// iPhone app may make (app_write_allowlist.go). 30 a minute per user.
				r.With(settingsWriteLimiter.Middleware).Patch("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingSettingsPatch(d.Settings.For(uid))(w, r)
				}))
				r.Get("/networks", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingNetworksGet(d.Settings.For(uid))(w, r)
				}))
				r.With(settingsWriteLimiter.Middleware).Put("/networks", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingNetworksPut(d.Settings.For(uid))(w, r)
				}))
				r.With(settingsWriteLimiter.Middleware).Post("/networks/reset", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingNetworksReset(d.Settings.For(uid))(w, r)
				}))
				// Networks' membership terms: sourced facts with their check date.
				r.Get("/membership-terms", handleMembershipTerms(time.Now))
				// The user's off-peak electricity schedule (times and rates).
				r.Get("/offpeak", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleOffpeakGet(d.Settings.For(uid))(w, r)
				}))
				r.With(settingsWriteLimiter.Middleware).Put("/offpeak", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleOffpeakPut(d.Settings.For(uid))(w, r)
				}))
				// "My electricity is one flat rate": stops the off-peak estimates. The iPhone app may write it too (30 a minute per user).
				r.Get("/flat-rate", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleFlatRateGet(d.Settings.For(uid))(w, r)
				}))
				r.With(settingsWriteLimiter.Middleware).Put("/flat-rate", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleFlatRatePut(d.Settings.For(uid))(w, r)
				}))
			})

			// The account's display units (mi/km, °C/°F), one setting for the web and the iPhone app (settings_units.go).
			r.Route("/settings/units", func(r chi.Router) {
				r.Get("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleUnitsGet(d.Settings.For(uid))(w, r)
				}))
				r.With(settingsWriteLimiter.Middleware).Patch("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleUnitsPatch(d.Settings.For(uid))(w, r)
				}))
			})

			// Welcome and setup reminder emails: on/off (Settings toggle).
			if d.DB != nil {
				oe := &onboardingEmailAPI{db: d.DB, now: time.Now}
				r.Get("/settings/onboarding-email", withUser(oe.getSettings))
				r.Put("/settings/onboarding-email", withUser(oe.putSettings))
				if d.RivianExpiryEmails {
					re := &rivianExpiryEmailAPI{db: d.DB, now: time.Now}
					r.Get("/settings/rivian-expiry-email", withUser(re.getSettings))
					r.Put("/settings/rivian-expiry-email", withUser(re.putSettings))
				}
			}

			// Plug-in reminder and weekly recap knobs (internal/engage).
			if d.DB != nil {
				eg := &engagementAPI{db: d.DB}
				r.Get("/settings/engagement", withUser(eg.get))
				r.Put("/settings/engagement", withUser(eg.put))
			}

			// User's saved home location, used by the trip planner
			// for one-click Origin/Destination presets.
			// Monthly statement email: opt-in + timezone, and a preview of
			// what would be sent. Off unless the operator flag covers
			// the user (preview 404s, `available` is false).
			if d.DB != nil && d.StatementSigner != nil {
				stm := newStatementAPI(d.DB, d, d.StatementSigner)
				r.Get("/settings/statement-email", withUser(stm.getSettings))
				r.Put("/settings/statement-email", withUser(stm.putSettings))
				r.Get("/statement-email/preview", withUser(stm.preview))
			}
			homeWriteLimiter, homeSearchLimiter := mapsUserLimiter(10), mapsUserLimiter(20)
			var homeNotice *homeChangeNotice
			if d.Email != nil {
				homeNotice = &homeChangeNotice{mail: d.Email, db: d.DB}
			}
			r.Route("/settings/home-location", func(r chi.Router) {
				r.Get("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleHomeLocationGet(d.Settings.For(uid), d.Photon)(w, r)
				}))
				// Writes: the web, and the iPhone app (app_write_allowlist.go). 10 a minute per user for set, clear and restore;
				// the address search is the costliest call and has its own per-user limit. The account is emailed when the app
				// changes the home (home_notice.go).
				r.With(homeWriteLimiter.Middleware).Put("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleHomeLocationPut(d.Settings.For(uid), d.Photon, homeNotice.afterHomeChange)(w, r)
				}))
				r.With(homeWriteLimiter.Middleware).Post("/restore", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleHomeLocationRestore(d.Settings.For(uid), d.Photon, homeNotice.afterHomeChange)(w, r)
				}))
				// A street address through the self-hosted Photon only (the address in the body, never logged).
				r.With(homeSearchLimiter.Middleware).Post("/search", withUser(func(_ uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleHomeAddressSearch(d.Photon)(w, r)
				}))
			})

			// Opt-in to reading Rivian charging receipts (off by default;
			// opting out deletes what was stored).
			r.Get("/settings/rivian-receipts", withUser(handleRivianReceiptsGet(d.Flags, d.Settings)))
			r.Put("/settings/rivian-receipts", withUser(handleRivianReceiptsPut(d.Flags, d.Settings, receipts.NewStore(d.DB))))

			// Per-user switch for sending trip/drive data to the AI provider.
			// Off by default; the advice endpoints refuse until it is on.
			// Opt-in to looking up the outside temperature (Open-Meteo, location
			// rounded to ~11 km). Off by default; see ambient_temp.go.
			r.Get("/settings/ambient-temp", withUser(handleAmbientTempGet(d.Settings)))
			r.Put("/settings/ambient-temp", withUser(handleAmbientTempPut(d.Settings)))
			r.Get("/settings/ai-consent", withUser(handleAIConsentGet(d.Settings)))
			r.Put("/settings/ai-consent", withUser(handleAIConsentPut(d.Settings)))

			// Trip planner defaults — drive mode + Tesla NACS
			// adapter. SPA pre-fills the per-trip form from these.
			// Gated on the trip-planner feature flag: when disabled
			// the route returns 404 so the SPA's surface is fully
			// gone, not just hidden.
			r.Route("/settings/planner", func(r chi.Router) {
				r.Use(requireTripPlannerEnabledMW(d.Flags))
				r.Get("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePlannerPrefsGet(d.Settings.For(uid))(w, r)
				}))
				r.With(settingsWriteLimiter.Middleware).Put("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePlannerPrefsPut(d.Settings.For(uid))(w, r)
				}))
				r.Get("/favorites", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePlannerFavoritesGet(d.Settings.For(uid))(w, r)
				}))
				r.Put("/favorites", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handlePlannerFavoritesPut(d.Settings.For(uid))(w, r)
				}))
			})

			// AI provider configuration moved to /api/admin/settings/ai.
			// AI keys are install-wide (operator pays the bill); only
			// admins can read or rotate them. The SPA's /admin page
			// is gated on me().role === "admin" before rendering the
			// AI card, so non-admin users never see the route.

			// Admin surface. Gated by requireAdminMW (role='admin' on
			// the users row). The role check is layered ON TOP of the
			// requireUserMW that already runs at the parent group, so
			// an unauthenticated request gets a 401 before we even
			// resolve the role; an authenticated non-admin gets a 403.
			//
			// Endpoints:
			//   - kill-switch: Rivian upstream pause (decision 6)
			//   - users:       list / promote / delete users
			//   - settings/ai: install-wide AI provider config
			r.Route("/admin", func(r chi.Router) {
				// System pool for everything under /admin (RLS policy): admin routes span users, read and write non-tenant tables
				// (admin_audit, flags, signup_requests, idp_delete_failures) and sit behind Access on the admin host.
				r.Use(requireAdminMW(func(ctx context.Context, uid uuid.UUID) (string, error) {
					if d.SystemDB == nil {
						return "", nil
					}
					return db.RoleFor(ctx, d.SystemDB, uid)
				}))
				// Every admin write below is audited (admin_audit): the row is
				// written first and the change is refused if that fails.
				var auditStore *adminaudit.Store
				if d.SystemDB != nil {
					auditStore = adminaudit.New(d.SystemDB)
				}
				audit := adminAudit(auditStore)
				flagBefore := func(f func(*flags.Store) map[string]any) func() map[string]any {
					return func() map[string]any {
						if d.Flags == nil {
							return nil
						}
						return f(d.Flags)
					}
				}
				usersCount := func(b map[string]any) map[string]any {
					n := 0
					if us, ok := b["users"].([]any); ok {
						n = len(us)
					}
					return map[string]any{"users_count": n}
				}
				r.Get("/audit", handleAdminAuditList(auditStore))
				r.Get("/health-summary", handleAdminHealthSummary(d.AdminHealth))
				r.With(audit(auditSpec{action: "impersonation.stop", targetType: "user", bodyUser: "target_id"})).
					Post("/impersonation/stop", handleImpersonationStop())
				r.With(audit(auditSpec{action: "archive.restore", targetType: "archive_month", bodyUser: "user_id", userTarget: true,
					keys: []string{"vehicle_id", "month", "dry_run"}})).
					Post("/archive/restore", handleAdminArchiveRestore(d.ArchiveRestorer))
				r.Get("/kill-switch", handleFlagsGet(d.Flags))
				r.With(audit(auditSpec{action: "flag.kill_switch", targetType: "flag", targetID: "kill_switch", keys: []string{"paused"},
					before: flagBefore(func(f *flags.Store) map[string]any { return map[string]any{"paused": f.KillSwitch().Paused} })})).
					Put("/kill-switch", handleFlagsKillPut(d.Flags))
				r.With(audit(auditSpec{action: "flag.trip_planner", targetType: "flag", targetID: "trip_planner", keys: []string{"enabled"},
					before: flagBefore(func(f *flags.Store) map[string]any { return map[string]any{"enabled": f.TripPlanner().Enabled} })})).
					Put("/flags/trip-planner", handleFlagsTripPlannerPut(d.Flags))
				r.With(audit(auditSpec{action: "flag.statement_email", targetType: "flag", targetID: "statement_email", keys: []string{"all"}, custom: usersCount,
					before: flagBefore(func(f *flags.Store) map[string]any {
						return map[string]any{"all": f.StatementEmail().All, "users_count": len(f.StatementEmail().Users)}
					})})).
					Put("/flags/statement-email", handleFlagsStatementEmailPut(d.Flags))
				r.Get("/signup-cap", handleSignupCapGet(d.Flags, d.SystemDB))
				r.With(audit(auditSpec{action: "flag.signup_cap", targetType: "flag", targetID: "signup_cap", keys: []string{"limit"},
					before: flagBefore(func(f *flags.Store) map[string]any { return map[string]any{"limit": f.SignupCap().Limit} })})).
					Put("/signup-cap", handleSignupCapPut(d.Flags, d.SystemDB))
				r.With(audit(auditSpec{action: "flag.ai_call_cap", targetType: "flag", targetID: "ai_call_cap", keys: []string{"daily_limit"},
					before: flagBefore(func(f *flags.Store) map[string]any { return map[string]any{"daily_limit": f.AICallCap().DailyLimit} })})).
					Put("/flags/ai-call-cap", handleAICallCapPut(d.Flags))
				r.Get("/users", handleAdminUsersList(d.SystemDB, d.Flags))
				r.Get("/idp-delete-failures", handleAdminIdPFailures(d.SystemDB))
				r.With(audit(auditSpec{action: "idp_failure.retry", targetType: "idp_delete_failure", idParam: "id"})).
					Post("/idp-delete-failures/{id}/retry", handleAdminIdPFailureRetry(d.SystemDB, d.Users, d.Logger))
				r.Get("/users/{id}", handleAdminUserDetail(d.SystemDB))
				r.With(audit(auditSpec{action: "user.create", targetType: "user", usernameID: "username", keys: []string{"role", "disabled"}})).
					Post("/users", handleAdminUserCreate(d.SystemDB, d.Users, d.Logger))
				r.With(audit(auditSpec{action: "user.set_role", targetType: "user", idParam: "id", userTarget: true, keys: []string{"role"}})).
					Post("/users/{id}/role", handleAdminUserSetRole(d.SystemDB))
				r.With(audit(auditSpec{action: "user.set_disabled", targetType: "user", idParam: "id", userTarget: true, keys: []string{"disabled"}})).
					Post("/users/{id}/disabled", handleAdminUserSetDisabled(d.SystemDB))
				// Support's "secure this account": password, remembered logins, sessions and passkeys in one audited action. The audit
				// row holds the action and the target only; the temporary password is in the response and nowhere else.
				if d.Kratos != nil && d.Kratos.Enabled() && d.SessionRevoker != nil {
					sec := d.adminSecure()
					r.With(audit(auditSpec{action: "user.secure", targetType: "user", idParam: "id", userTarget: true})).
						Post("/users/{id}/secure", sec.handle)
				}
				r.With(audit(auditSpec{action: "user.sync_rivian", targetType: "user", idParam: "id", userTarget: true})).
					Post("/users/{id}/sync-rivian", handleAdminUserSyncRivian(d.Accounts, d.SystemDB, d.Logger))
				r.With(audit(auditSpec{action: "user.refresh_rivian_session", targetType: "user", idParam: "id", userTarget: true})).
					Post("/users/{id}/refresh-rivian-session", handleAdminUserRefreshRivianSession(d.Accounts, d.systemSecrets(), d.SystemDB, d.Logger)) // system pool: another user's secrets
				r.With(audit(auditSpec{action: "user.delete", targetType: "user", idParam: "id", userTarget: true})).
					Delete("/users/{id}", handleAdminUserDelete(d.SystemDB, d.Users, d.AppleRevocation, d.PurgeUserCaches, d.Logger))
				r.Get("/settings/ai", handleAISettingsGet(d.SettingsMgr))
				r.With(audit(auditSpec{action: "settings.ai", targetType: "settings", targetID: "ai", fields: true})).
					Put("/settings/ai", handleAISettingsPut(d.SettingsMgr))
				r.Get("/settings/ai/models/{provider}", handleAIModelsList(d.SettingsMgr))
				r.Post("/ai/ping", handleAIPing(d.SettingsMgr))
				r.Get("/settings/recap", handleRecapSettingsGet(d.SettingsMgr))
				r.With(audit(auditSpec{action: "settings.recap", targetType: "settings", targetID: "recap", fields: true})).
					Put("/settings/recap", handleRecapSettingsPut(d.SettingsMgr))
				r.Get("/settings/gps", handleGPSSettingsGet(d.SettingsMgr))
				r.With(audit(auditSpec{action: "settings.gps", targetType: "settings", targetID: "gps", fields: true})).
					Put("/settings/gps", handleGPSSettingsPut(d.SettingsMgr))
				if d.SignupRequests != nil && !signupgate.Disabled() {
					r.Get("/signup-requests", handleAdminSignupRequestsList(d.SignupRequests))
					r.With(audit(auditSpec{action: "signup_request.approve", targetType: "signup_request", idParam: "id"})).
						Post("/signup-requests/{id}/approve", handleAdminSignupRequestApprove(d.SystemDB, d.SignupRequests, d.Email, d.BaseURL, d.Logger))
					r.With(audit(auditSpec{action: "signup_request.reject", targetType: "signup_request", idParam: "id"})).
						Post("/signup-requests/{id}/reject", handleAdminSignupRequestReject(d.SignupRequests))
				}
				// Raw Rivian probes: arbitrary queries to Rivian as the admin.
				// Off unless RIVOLT_ADMIN_DEBUG_PROBES (Deps.AdminDebugProbes).
				if d.AdminDebugProbes {
					// Trip-planner debug: send arbitrary planTripWithMultiStop
					// variables and get the gateway response (data + errors)
					// verbatim. Lets us iterate on schema/value shape without
					// chart bumps. Body shape:
					//   { "variables": { ... whatever the caller wants ... } }
					r.Post("/trips/plan/raw", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
						handleTripPlanRawDebug(clientFor(d, uid))(w, r)
					}))
					// Schema introspection: GET /api/admin/gql/type?name=CoordinatesInput
					// returns the input-object field set the gateway publishes.
					// Bypasses guess-and-check on undocumented shapes.
					r.Get("/gql/type", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
						handleGraphQLIntrospect(clientFor(d, uid))(w, r)
					}))
					r.Get("/gql/enum", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
						handleGraphQLIntrospectEnum(clientFor(d, uid))(w, r)
					}))
					// Generic raw-GraphQL probe. POST {operation, query,
					// variables} → gateway response. Used to exercise
					// arbitrary query shapes (different operation names,
					// alternate field positions, nested input objects)
					// when introspection is locked down on the upstream.
					r.Post("/gql/raw", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
						handleGraphQLRaw(clientFor(d, uid))(w, r)
					}))
					// Trip-planner diagnostic bundle. GET-only so an operator
					// can click the URL on mobile (no DevTools needed). Runs:
					//   1. __type introspection on a few likely names
					//   2. planTripWithMultiStop for the caller's own truck between two city centres
					// Dumps every response together. Paste-back-to-debug.
					r.Get("/trips/plan/diag", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
						handleTripPlanDiag(clientFor(d, uid))(w, r)
					}))
				}
			})

			// Read-only session/telemetry endpoints. Populated by either the
			// ElectraFi importer or the (future) live Rivian ingester.
			// The big list and series reads are gzipped.
			gz := middleware.Compress(5, "application/json")
			r.With(gz).Get("/drives", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDrives(d.Drives.For(uid), d.Charges.For(uid), d.Settings.For(uid))(w, r)
			}))
			r.Get("/drives/{id}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDriveGet(d.Drives.For(uid), d.Charges.For(uid), d.Settings.For(uid))(w, r)
			}))
			// Efficiency analysis: AI-driven breakdown of what drove
			// efficiency variance for a drive, with actionable
			// recommendations.
			//
			// GET returns the persisted result so the SPA can show a
			// previously-analyzed drive without re-billing on every
			// page load. 404 on first visit; the SPA falls back to
			// the empty-state form. The matching POST lives in the
			// AI-bound sibling group below — it can take >30s on
			// thinking models (Gemini 3.1 Pro Preview), so the chi
			// 30s middleware here would cut the call and return 502
			// to the SPA mid-analysis.
			r.Get("/drives/{id}/efficiency", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDriveEfficiencyGet(d, uid)(w, r)
			}))
			// Standalone weather snapshot for a drive. Independent of
			// the recap path so the detail-page chart can render the
			// outside-temp line even when no AI recap was generated
			// (e.g. operator never configured an LLM, or the drive
			// is too short to be worth narrating). 404 = no row,
			// regardless of whether the toggle is currently on.
			r.Get("/drives/{id}/weather", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDriveWeatherGet(d.DB, uid)(w, r)
			}))
			// Time-series sibling of the start-hour snapshot. Returns
			// an array of samples (15-min cadence for recent drives
			// from Open-Meteo's forecast endpoint, hourly for older
			// drives from the archive endpoint). Empty array when
			// the drive was never enriched. Drives the temperature
			// + precipitation panel on the drive detail page.
			r.Get("/drives/{id}/weather/series", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDriveWeatherSeriesGet(d.DB, uid)(w, r)
			}))
			// Bulk weather backfill for historical drives. Each call
			// processes up to a fixed batch (see handler) so a slow
			// upstream can't lock up a worker; the SPA polls until
			// `remaining == 0`. Gated on the same RecapWeatherEnabled
			// pref the per-drive recap fetch consults.
			r.Post("/drives/weather/backfill", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDriveWeatherBackfill(d, uid)(w, r)
			}))
			r.With(gz).Get("/charges", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleCharges(d.Charges.For(uid), d.Settings.For(uid), billedFor(d, uid))(w, r)
			}))
			// What the iPhone's Home charts plot: compact drive and charge points
			// with no coordinates, ids, costs or places (chart_points.go).
			// The iPhone's Activity lists in one compact answer (activity.go).
			// activity, charge-split and chart-points each read the user's last thousands of
			// rows (the first two also classify and cluster them), so they share one
			// per-user ceiling: 30 a minute, with a Retry-After on the 429. A Home
			// refresh is three calls and the app caches for 15 minutes; a loop is not.
			aggLimiter := mapsUserLimiter(30)
			r.With(aggLimiter.Middleware, gz).Get("/activity", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleActivity(d.Drives.For(uid), d.Charges.For(uid), d.Settings.For(uid), billedFor(d, uid))(w, r)
			}))
			// Home / Away / Fast charge split for the iPhone (charge_split.go).
			r.With(aggLimiter.Middleware).Get("/charge-split", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargeSplit(d.Charges.For(uid), d.Settings.For(uid), billedFor(d, uid))(w, r)
			}))
			r.With(aggLimiter.Middleware, gz).Get("/chart-points", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChartPoints(d.Drives.For(uid), d.Charges.For(uid))(w, r)
			}))
			r.Delete("/charges/{id}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDeleteCharge(d.Charges.For(uid))(w, r)
			}))
			r.Post("/charges/restamp-place", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleRestampPlace(restamperFor(d, uid), restampSettingsFor(d, uid))(w, r)
			}))
			r.Post("/charges/restamp-place/undo", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleUndoRestamp(restamperFor(d, uid))(w, r)
			}))
			r.Post("/charges/apply-rate", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleApplyNetworkRate(d.Charges.For(uid), d.Settings.For(uid))(w, r)
			}))
			// The user's own correction of whether a trailer was connected on a drive (drive_trailer.go): web only, not on
			// the app write allowlist.
			r.With(settingsWriteLimiter.Middleware).Patch("/drives/{id}/trailer", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				st := d.Drives.For(uid)
				if st == nil {
					handleDriveTrailerPatch(nil)(w, r)
					return
				}
				handleDriveTrailerPatch(st)(w, r)
			}))
			// Per-charge overrides (#906, charge_overrides.go): web only, not on the app write allowlist.
			r.Put("/charges/{id}/place", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargePlacePut(overriderFor(d, uid), restampSettingsFor(d, uid).home)(w, r)
			}))
			r.Put("/charges/{id}/location", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargeLocationPut(overriderFor(d, uid))(w, r)
			}))
			r.Patch("/charges/{id}/pricing", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handlePatchChargePricing(d.Charges.For(uid), d.Settings.For(uid))(w, r)
			}))
			r.Post("/charges/{id}/pricing/confirm", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				st := d.Charges.For(uid)
				if st == nil {
					http.Error(w, "charges disabled", http.StatusServiceUnavailable)
					return
				}
				handleConfirmChargeNetwork(st, d.Settings.For(uid), billedFor(d, uid))(w, r)
			}))
			r.Get("/charges/{id}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				st := d.Charges.For(uid)
				if st == nil {
					http.Error(w, "charges disabled", http.StatusServiceUnavailable)
					return
				}
				handleGetCharge(st, d.Settings.For(uid), billedFor(d, uid))(w, r)
			}))
			// The iPhone Money tab's "To confirm" list, coordinate-free (charges_to_confirm.go).
			r.With(aggLimiter.Middleware).Get("/charges/to-confirm", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargesToConfirm(d.Charges.For(uid), d.Settings.For(uid), billedFor(d, uid))(w, r)
			}))
			r.Get("/charges/clusters", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargeClusters(d.Charges.For(uid))(w, r)
			}))
			// Money tab ledger: server-side spend/energy statement. Read-only.
			r.Route("/ledger", func(r chi.Router) {
				la := newLedgerAPI(d.DB, d.Settings).withBilled(d.Flags, d.Settings)
				r.Get("/enabled", withUser(handleLedgerEnabled))
				r.Get("/monthly", withUser(la.monthly))
				r.Get("/monthly.csv", withUser(la.statementCSV))
				r.Get("/summary", withUser(la.summary))
				r.Get("/trips", withUser(la.trips))
				r.Get("/savings", withUser(la.savings))
			})
			r.With(gz).Get("/samples", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSamples(d.Samples.For(uid))(w, r)
			}))
			// Per-day asleep/awake hours for the overview sleep graph,
			// from the persisted power_state column (migration 0038).
			r.Get("/sleep-activity", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSleepActivity(uid, d.Samples.For(uid))(w, r)
			}))
			// Efficiency breakdowns (Insights): mi/kWh by outside
			// temperature, average speed and drive mode over a period,
			// from the user's own drives and samples.
			r.With(defaultUnits).Get("/insights/efficiency", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				var store driveConditionLister
				if st := d.Samples.For(uid); st != nil {
					store = st
				}
				handleEfficiencyInsights(store, d.Logger)(w, r)
			}))
			// Trip planner — pass-through to Rivian's
			// planTripWithMultiStop. Caller supplies origin +
			// destination + optional intermediate waypoints +
			// optional target arrival SoC. The handler resolves
			// the user's vehicle + current SoC from cache; the
			// gateway computes charging stops and per-leg numbers.
			// 404'd when the trip-planner feature flag is off.
			// Where to sleep: hotels with a listed Level 2 charger near the
			// day's drive budget. Archive-only, no Rivian call.
			if d.ChargersArchive != nil {
				r.With(requireTripPlannerEnabledMW(d.Flags), mapsUserLimiter(tripsOvernightPerMin).Middleware).Post("/trips/overnight-candidates", handleOvernightCandidates(d.ChargersArchive, d.pathRouter(), d.lodgingSource()))
			}
			r.With(requireTripPlannerEnabledMW(d.Flags)).Post("/trips/plan", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleTripPlan(withLegCache(d, uid, clientFor(d, uid)), monitorFor(d, uid), d.DB, uid, d.SettingsMgr, d.Settings.For(uid), d.WeatherClient, d.WeatherCache)(w, r.WithContext(withPlanOwnRates(r.Context(), ownRatesFor(d, uid))))
			}))
			// Multi-day plan is mounted in the AI-bound group below
			// (5-minute timeout). N sequential planTrip2 calls easily
			// blow this group's 30s budget; chi cancels mid-second-leg
			// and the SPA sees "Bad Gateway".
			// Saved trip templates. Inputs are required; plan/advice are
			// optional snapshots so reopening a saved trip can render
			// the map instantly while still letting the user re-plan
			// against current station / weather state.
			// Plan vs Actual (B12): compare a saved trip with the outing
			// that happened.
			// Saves, edits and links: 30 a minute per user, for the web and the phone alike.
			savedTripWriteLimiter := mapsUserLimiter(30)
			if d.DB != nil && d.Trips != nil {
				pa := newPlanActualAPI(d.DB, d.Trips, d.Settings)
				r.Get("/trips/saved/{id}/actual", withUser(pa.actual))
				r.With(savedTripWriteLimiter.Middleware).Post("/trips/saved/{id}/link", withUser(pa.link))
				r.With(savedTripWriteLimiter.Middleware).Post("/trips/saved/{id}/unlink", withUser(pa.unlink))
			}
			// Where a saved multi-day trip stands (which day is under way, where
			// the truck is), for "re-plan from here". Reads only.
			if d.DB != nil && d.Trips != nil {
				r.With(requireTripPlannerEnabledMW(d.Flags)).Get("/trips/saved/{id}/active", withUser(handleSavedTripActive(d.Trips, func(u uuid.UUID) *rivian.StateMonitor { return monitorFor(d, u) }, d.DB)))
			}
			r.Get("/trips/saved", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSavedTripsList(d.Trips.For(uid))(w, r)
			}))
			r.With(savedTripWriteLimiter.Middleware).Post("/trips/saved", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSavedTripCreate(d.Trips.For(uid))(w, r)
			}))
			r.With(savedTripWriteLimiter.Middleware).Put("/trips/saved/{id}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSavedTripUpdate(d.Trips.For(uid))(w, r)
			}))
			r.Delete("/trips/saved/{id}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSavedTripDelete(d.Trips.For(uid))(w, r)
			}))
			// Geocoding for the trip planner. Forwards a free-text
			// query to Open-Meteo's geocoding endpoint (same
			// provider as weather; privacy trade-off is identical).
			// Returns city-level matches sorted by population so the
			// SPA can render a sensible suggestion dropdown.
			r.With(mapsUserLimiter(geocodePerMin).Middleware).Get("/geocode", withUser(func(_ uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleGeocode(d.Photon)(w, r)
			}))
		}) // end of timed authenticated /api group

		// AI-bound POSTs. Same auth as the timed group above, but
		// with a 5-minute timeout because "thinking" LLMs like
		// Gemini 3.1 Pro Preview routinely take 30–90s to respond
		// on non-trivial drive prompts. The chi 30s timeout in the
		// timed group writes 504 mid-call and the SPA shows
		// "Analysis failed" — keep these endpoints here so the
		// connection stays alive long enough for the model.
		r.Group(func(r chi.Router) {
			if d.AuthEnforced {
				r.Use(requireUserMW)
				r.Use(requireTermsMW(termsLookupFor(d)))
			}
			r.Use(middleware.Timeout(5 * time.Minute))
			r.Use(maxBodyBytes(maxJSONBody))
			// Per-user daily cap on the two LLM-backed endpoints so a
			// single account can't run up the AI bill. Applied per-route
			// (not group-wide) so plan-multiday — Rivian calls, not LLM —
			// stays uncapped.
			aiBudgetMW := requireAIBudgetMW(d.Flags, d.AIBudget)
			// The user's own consent comes first, so a refused call is not
			// charged to the budget.
			aiGate := requireAIConsentMW(d.Settings)
			r.With(aiGate, aiBudgetMW).Post("/drives/{id}/efficiency", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDriveEfficiencyPost(d, uid)(w, r)
			}))
			r.With(aiGate, aiBudgetMW).Post("/trips/plan/advice", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleTripPlanAdvice(d.SettingsMgr, d.Settings.For(uid))(w, r)
			}))
			// Multi-day orchestrator: N sequential planTrip2 calls.
			// Lives here so it inherits the 5-minute timeout — three
			// legs at ~10s each blow the timed-group's 30s budget.
			r.With(requireTripPlannerEnabledMW(d.Flags)).Post("/trips/plan-multiday", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleTripPlanMultiday(withLegCache(d, uid, clientFor(d, uid)), monitorFor(d, uid), d.DB, uid, d.SettingsMgr, d.Settings.For(uid), d.WeatherClient, d.WeatherCache)(w, r.WithContext(withPlanOwnRates(r.Context(), ownRatesFor(d, uid))))
			}))
		})

		// Bulk data routes. Identical auth, no 30s timeout — an
		// ElectraFi import or a year-long backup can legitimately
		// take minutes. chi's timeout middleware cancels the
		// request context mid-write, which previously produced
		// partial imports and a "superfluous WriteHeader" warning.
		r.Group(func(r chi.Router) {
			if d.Auth != nil && d.Auth.Configured() {
				r.Use(requireUserMW)
				r.Use(requireTermsMW(termsLookupFor(d)))
			}

			// Accepts a multipart upload of an ElectraFi CSV export. Streams
			// it through the importer so users don't have to drop into a
			// terminal to load data.
			r.Post("/import/electrafi", handleImportElectrafi(d))

			// Data management. GET /data/backup streams every
			// drive/charge/sample for the current user as a single
			// downloadable JSON bundle. POST /data/restore is its
			// inverse. DELETE /data/sessions wipes those three
			// tables (preserves vehicles/settings/push). The UI
			// pairs backup+reset for the re-import-after-tz-change
			// flow and backup+restore for disaster recovery.
			r.Get("/data/backup", handleDataBackup(d))
			// Human-friendly "take your data with you": zip of CSVs
			// (charges incl. the ledger's effective cost source,
			// drives, trips) + settings/profiles JSON. Never behind the
			// Money tab; no secrets, no raw samples.
			r.Get("/export", handleExport(d))
			r.Post("/data/restore", handleDataRestore(d))
			r.Delete("/data/sessions", handleDataReset(d))
			r.Delete("/data/account", handleDataAccountDelete(d))
		}) // end of bulk-data authenticated /api group
	})

	// Everything else falls through to the embedded SPA. The SPA
	// itself is always reachable — it needs to render the login
	// page when the /api/auth/me bootstrap returns 401.
	// The release-notes feed, next to the /changelog page. Outside /api, so
	// it is mounted only on the public site, not the admin or demo hosts.
	if d.Changelog != nil && !d.AdminOnly && !demo.OnlyEnabled() {
		// HEAD too: feed readers and validators probe with it first.
		feed := handleChangelogAtom(d.Changelog)
		r.With(clLimiter.Middleware).Get("/changelog.atom", feed)
		r.With(clLimiter.Middleware).Head("/changelog.atom", feed)
	}
	// The iPhone app's associated domain (RIVOLT_IOS_APP_IDS): the AASA file
	// and the sign-in callback's browser fallback. Public site only.
	if len(d.IOSAppIDs) > 0 && !d.AdminOnly && !demo.OnlyEnabled() {
		r.Get("/.well-known/apple-app-site-association", handleAASA(d.IOSAppIDs))
		r.Get(appCallbackPath, handleAppCallbackPage())
	}
	r.Handle("/*", spaHandler(d.WebFS))

	return r
}

// securityHeaders applies a baseline set of HTTP security response
// headers to every API + SPA response. Per-deployment hardening
// (e.g. CSP tightening for an embedded admin) can layer on top.
//
//   - Content-Security-Policy:
//     'self' for scripts and connections, 'unsafe-inline' on styles
//     because the SPA paints Leaflet/DriveMap markers via inline
//     style attributes — moving them off-thread would buy nothing
//     versus the actual XSS hardening here. img-src allows data:
//     and blob: so canvas-derived images (chart screenshots) and
//     pmtiles glyph atlases render without a header break, plus
//     https://rivian.com + https://*.rivian.com because the per-vehicle
//     configurator hero image is served from rivian.com/mobile/static/img/...
//     (bare host) today; the wildcard absorbs the eventual move to a
//     proper CDN subdomain. The IP leak to
//     Rivian on every page load is acceptable because the user has
//     already authenticated to Rivian for telemetry — we're not
//     widening the surface, just rendering an asset they already
//     associated with their account.
//     frame-ancestors 'none' prevents clickjacking; the SPA never
//     embeds in an iframe.
//     No third-party script or connect origins are allowed: Rivolt
//     serves no analytics beacon (Cloudflare Web Analytics origins
//     were removed), so script-src and connect-src are 'self' only.
//   - Strict-Transport-Security: 1 year with subdomains. Cloudflare
//     in front already terminates TLS for rivolt.dev / preview.rivolt.dev,
//     but the header makes the policy explicit on origin responses
//     too (and protects self-hosted installs that put their own
//     reverse proxy in front).
//   - X-Content-Type-Options: nosniff. Belt-and-braces against
//     content-type confusion attacks.
//   - Referrer-Policy: strict-origin-when-cross-origin. Avoids
//     leaking deep app URLs to third-party AI / weather providers
//     when the SPA's about page or share button is used.
//   - X-Frame-Options: DENY. Same intent as the CSP frame-ancestors
//     directive; included for older browsers / proxies that don't
//     parse CSP.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob: https://rivian.com https://*.rivian.com; " +
		"font-src 'self' data:; " +
		"connect-src 'self'; " +
		"worker-src 'self'; " +
		"manifest-src 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"object-src 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// withUser adapts a uid-aware handler to chi. Resolves the request
// user from auth context; 401s if none. Used by every per-user
// route so handlers can be plain (uid, *Store, *Store, ...) closures
// without having to repeat the auth resolution at every call site.
func withUser(fn func(uid uuid.UUID, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		fn(uid, w, r)
	}
}

// clientFor resolves the per-user Rivian client. Falls back to the
// shared d.Rivian (typically the stub) when no per-user account is
// available — that path is what keeps the API alive in stub-only
// installs that never sign anyone in.
func clientFor(d Deps, uid uuid.UUID) rivian.Client {
	if d.Accounts != nil {
		if a := d.Accounts.For(uid); a != nil {
			if c, ok := a.(rivian.Client); ok && c != nil {
				// Self-heal a stale per-pod client: if it was built
				// before the user finished signing in on a peer pod it
				// stays unauthenticated and every data-plane call 502s.
				// Load the persisted session and restore it. Guarded on
				// !Authenticated so the warm path stays a pure memory
				// read with no per-request DB hit.
				if d.Secrets != nil && !a.Authenticated() {
					// Detached from the request, so it carries the user explicitly: Secrets is request-side under row-level
					// security, and a context with no user reads no rows (the self-heal would silently never run).
					ctx, cancel := context.WithTimeout(auth.WithUser(context.Background(), uid), 3*time.Second)
					if sess, err := secrets.LoadRivianSession(ctx, d.Secrets, uid); err == nil && sess.UserSessionToken != "" {
						a.Restore(sess)
					}
					cancel()
				}
				return c
			}
		}
	}
	return d.Rivian
}

// monitorFor resolves the per-user StateMonitor. Returns nil when
// no monitor is running for that user yet (login path will start
// one once the user signs in to Rivian).
func monitorFor(d Deps, uid uuid.UUID) *rivian.StateMonitor {
	if d.Monitors == nil {
		return nil
	}
	return d.Monitors.For(uid)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// otelTraceRoute renames the active span (created by otelhttp at the
// outer wrap) to "HTTP <method> <chi-pattern>" after chi resolves
// the route. otelhttp's default name is "HTTP <method>", which is
// useless in Tempo because every request lands under one bucket.
// We could instead use a custom SpanNameFormatter on otelhttp, but
// that runs before routing — the chi pattern isn't filled in yet.
//
// Also stamps http.route as a span attribute (semconv-correct).
// Cheap no-op when tracing is disabled (span is a no-op span).
func otelTraceRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		span := trace.SpanFromContext(r.Context())
		if !span.IsRecording() {
			return
		}
		if pattern := chi.RouteContext(r.Context()).RoutePattern(); pattern != "" {
			span.SetName("HTTP " + r.Method + " " + pattern)
		}
	})
}

// signupCapOK reports whether a new account fits under the signup_cap flag
// (limit <= 0 blocks; otherwise the user count must be under it): the same
// rule the OIDC callback applies. nil without flags or a database.
func signupCapOK(d Deps) func(ctx context.Context) (bool, error) {
	if d.Flags == nil || d.SystemDB == nil {
		return nil
	}
	return func(ctx context.Context) (bool, error) {
		lim := d.Flags.SignupCap().Limit
		if lim <= 0 {
			return false, nil
		}
		n, err := db.CountUsers(ctx, d.SystemDB) // system pool: counts every user, before the new one exists
		if err != nil {
			return false, err
		}
		return n < lim, nil
	}
}

// newAutoApprove wires the automatic approval of email signup requests
// (verified email under the cap); nil leaves every request to an admin.
func newAutoApprove(d Deps) *autoApprove {
	capOK := signupCapOK(d)
	if capOK == nil || d.Email == nil {
		return nil
	}
	return &autoApprove{
		capOK: capOK,
		userExists: func(ctx context.Context, email string) (bool, error) {
			return db.UserExistsByUsername(ctx, d.SystemDB, email) // system pool: signup auto-approve, no session
		},
		send: func(ctx context.Context, to, link string) error {
			return sendApprovalEmail(ctx, d.Email, to, link)
		},
		baseURL: d.BaseURL,
	}
}
