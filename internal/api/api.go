// Package api wires the HTTP surface for Rivolt. It assembles routes,
// middleware, and handler dependencies into a single chi Mux.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	"github.com/apohor/rivolt/internal/aibudget"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/idp"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/packhealth"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/signuprequests"
	"github.com/apohor/rivolt/internal/trips"
	"github.com/apohor/rivolt/internal/weather"
)

// Deps is the bag of dependencies the API router needs. Keep this
// small; avoid accumulating a "dependency soup" pattern.
//
// All per-user state (data-plane stores, Rivian client, monitor)
// is reached through factories/registries keyed by the request
// user's uid — there is no singleton "current user" anywhere in
// the API surface.
type Deps struct {
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
	// AuthEnforced flips on the requireUser middleware for /api/*.
	// True when at least one real issuer is configured (OIDC,
	// trusted-proxy, or the debug bypass).
	AuthEnforced bool
	// ImpersonationEnabled turns on the admin "view as user" flow
	// (impersonationMW + the SPA button). Defaults true; set false via
	// RIVOLT_IMPERSONATION_DISABLED to hard-off the feature (header
	// ignored, admin UI hidden).
	ImpersonationEnabled bool
	// OIDC, when non-nil, mounts /api/auth/oidc/* — the third
	// auth issuer alongside static creds and trusted-proxy
	// header. nil disables the social-login button row in the
	// SPA but doesn't affect any other code path.
	OIDC *oidc.Service
	// Hydra, when non-nil along with Kratos, mounts the custom
	// login + consent handlers under /api/auth/hydra. This makes
	// Rivolt the bring-your-own-UI for an Ory Hydra OAuth2 / OIDC
	// provider — downstream apps (ArgoCD, Grafana, etc.) federate
	// against Hydra and Hydra calls back into us for the user-
	// facing login prompt.
	Hydra  *hydra.Client
	Kratos *kratos.Client
	// HydraRememberFor controls how long Hydra remembers the
	// user's login + consent before forcing a fresh prompt. Zero
	// defers to Hydra's login_session lifespan. Sourced from the
	// RIVOLT_OIDC_HYDRA_REMEMBER_FOR env var in main.go.
	HydraRememberFor time.Duration
	WebFS            fs.FS
	Version          string
	// DB is the shared Postgres pool. Used by request middleware
	// that needs to answer "does this session user own this
	// vehicle?" without round-tripping through a per-user store.
	// Safe to be nil in legacy code paths that predate the
	// ownership middleware; ownership enforcement is only wired
	// when DB is non-nil.
	DB *sql.DB
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
	// BaseURL is the install's public origin (e.g. https://rivolt.dev),
	// sourced from $RIVOLT_BASE_URL. Used today only for composing
	// the magic-link signup URL in approval emails; safe to leave
	// empty in tests — the link falls back to a relative path so
	// copy-paste-into-the-same-tab still works.
	BaseURL string
	// ValhallaProxy, when non-nil, mounts a same-origin reverse
	// proxy at /api/maps/valhalla/* that forwards to a self-hosted
	// Valhalla. /api/config advertises whether the proxy is
	// mounted so the SPA picks the right base URL at boot. nil
	// leaves the route unmounted; the SPA renders raw GPS chords
	// without snapping.
	ValhallaProxy http.Handler
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

// New builds the root mux with all routes mounted.
func New(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
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

	// Authentication middleware runs on every request so handlers
	// below can read auth.UserFromContext regardless of whether
	// auth is enforced. With auth unconfigured (nil Service) the
	// middleware is a no-op — the single-tenant legacy UX stays.
	if d.Auth != nil {
		r.Use(d.Auth.Middleware)
	}

	// /metrics is intentionally mounted at the root, outside the
	// /api tree, with no auth. The Prometheus scraper inside the
	// cluster reaches it via the pod IP; the Ingress doesn't expose
	// /metrics externally.
	if d.Metrics != nil {
		r.Handle("/metrics", d.Metrics.Handler())
	}

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
	r.Get("/readyz", handleReadyz(d.DB))

	r.Route("/api", func(r chi.Router) {
		// Admin "view as user" middleware, built once and reused across
		// the authed route groups below (main JSON + the long-timeout
		// AI/bulk group). Needs the DB for the role lookup; nil when
		// there's no DB, in which case the groups simply skip it.
		var impersonateMW func(http.Handler) http.Handler
		if d.DB != nil {
			impersonateMW = impersonationMW(func(ctx context.Context, uid uuid.UUID) (string, error) {
				return db.RoleFor(ctx, d.DB, uid)
			}, d.ImpersonationEnabled)
		}
		// Health + auth endpoints stay reachable without a session,
		// otherwise the browser has no way to log in. /api/health is
		// kept as a stable alias of /healthz for the preview-version
		// poller, external monitoring, and any docs that reference it.
		r.Get("/health", handleHealth(d.Version))
		// /api/config advertises optional runtime knobs to the SPA
		// (which same-origin proxies are mounted, feature flags,
		// admin-configurable GPS thresholds). Public so the SPA can
		// fetch it before login; reveals no user-scoped data.
		r.Get("/config", handleConfig(d.ValhallaProxy != nil, d.TilesProxy != nil, d.SettingsMgr != nil && d.SettingsMgr.Analyzer() != nil, d.ImpersonationEnabled, d.Flags, d.SettingsMgr))
		if d.Auth != nil {
			r.Route("/auth", func(r chi.Router) {
				r.Post("/logout", d.Auth.Logout)
				r.Get("/me", handleMeEnriched(d.Auth, d.DB))
				if d.OIDC != nil {
					d.OIDC.Mount(r)
				}
				// Hydra OIDC bridge: Rivolt is the login + consent UI
				// for our Ory Hydra OAuth2 server. Both Hydra (admin
				// API) and Kratos (public auth API) must be wired
				// for this to be useful. Mounting just one would
				// leave a half-broken flow, so guard on both.
				if d.Hydra != nil && d.Hydra.Enabled() &&
					d.Kratos != nil && d.Kratos.Enabled() {
					hd := hydraDeps{
						Hydra:       d.Hydra,
						Kratos:      d.Kratos,
						Logger:      d.Logger,
						RememberFor: d.HydraRememberFor,
					}
					r.Route("/hydra", func(r chi.Router) {
						r.Get("/login", hydraLoginGET(hd))
						r.Post("/login", hydraLoginPOST(hd))
						r.Get("/consent", hydraConsentGET(hd))
					})
				}
			})
		}
		// Public sign-up: validate invite code and create account.
		// Deliberately outside the requireUser group — the user
		// does not have a session yet.
		if d.SignupRequests != nil {
			r.With(maxBodyBytes(maxJSONBody)).Post("/signup", handleSignup(d.DB, d.SignupRequests, d.Users, d.Logger))
		}
		// Public "request access" form companion to /signup. Anyone
		// without an invite code can submit an email + short note;
		// admins decide the row in /admin/signup-requests.
		//
		// Per-IP in-memory limiter: 5 requests/hour/IP with a burst
		// of 5. The real DDoS defense is the Cloudflare edge rule;
		// this is the in-pod fallback if CF ever lets traffic
		// through. Beta scale: 5/hr is generous for a human filling
		// out the form and trivial to enforce.
		if d.SignupRequests != nil {
			signupReqLimiter := newIPLimiter(5, time.Hour, time.Hour)
			r.With(signupReqLimiter.Middleware, maxBodyBytes(maxJSONBody)).Post(
				"/signup/request",
				handleSignupRequestCreate(d.SignupRequests, d.Email, d.Logger),
			)
			// Magic-link prefill — the SPA hits this on /signup?token=…
			// to resolve the token to its email before rendering the
			// password form.
			r.Get("/signup/token/{token}", handleSignupTokenLookup(d.SignupRequests))
		}

		// Everything else sits behind requireUser when auth is
		// enforced. Unenforced mode (no issuer wired) keeps the
		// API open — legacy single-tenant docker-compose UX.
		r.Group(func(r chi.Router) {
			if d.AuthEnforced {
				r.Use(requireUserMW)
			}
			if impersonateMW != nil {
				r.Use(impersonateMW)
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
				r.Mount("/maps/valhalla", http.StripPrefix("/api/maps/valhalla", d.ValhallaProxy))
			}
			if d.TilesProxy != nil {
				r.Mount("/maps/tiles", http.StripPrefix("/api/maps/tiles", d.TilesProxy))
			}
			// Server-side chargers-along-corridor query. Replaces the
			// SPA's PMTiles per-tile fan-out (hundreds of HTTP range
			// reads per planner re-render) with one POST that returns
			// the decoded POI list.
			if d.ChargersArchive != nil {
				r.Post("/maps/chargers-along", handleChargersAlong(d.ChargersArchive))
			}
			// Onboarding — marks the first-run stepper as done for the
			// current user. Called by the frontend when the user
			// reaches the final step and clicks "Done".
			r.Post("/onboarding/complete", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleOnboardingComplete(d.DB)(uid, w, r)
			}))

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
			var vehicleScoped func(http.Handler) http.Handler
			if d.DB != nil {
				vehicleScoped = requireVehicleOwnershipMW(d.DB, d.Logger)
			} else {
				vehicleScoped = func(next http.Handler) http.Handler { return next }
			}
			r.Get("/vehicles", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleVehicles(clientFor(d, uid), monitorFor(d, uid), d.DB, d.Logger)(w, r)
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
			r.With(vehicleScoped).Get("/vehicles/{vehicleID}/pack-health", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handlePackHealthGet(d.DB, d.PackHealth)(uid, w, r)
			}))
			r.With(vehicleScoped).Get("/state/{vehicleID}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleVehicleState(clientFor(d, uid), monitorFor(d, uid))(w, r)
			}))
			r.With(vehicleScoped).Get("/state/{vehicleID}/debug", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleVehicleStateDebug(clientFor(d, uid))(w, r)
			}))
			r.With(vehicleScoped).Get("/state/{vehicleID}/fresh", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleVehicleStateFresh(clientFor(d, uid))(w, r)
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
			r.Get("/charging-field/{field}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargingFieldProbe(clientFor(d, uid))(w, r)
			}))
			r.Get("/charging-frames", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargingFrames(clientFor(d, uid))(w, r)
			}))

			// Rivian account management. Only wired when a live client is
			// present; with the stub/mock these return 404.
			r.Route("/settings/rivian", func(r chi.Router) {
				r.Get("/", handleRivianStatus(d.Accounts, d.Secrets, d.DB, d.Logger))
				r.Post("/login", handleRivianLogin(d.Accounts, d.Secrets, d.Monitors, d.Email, d.DB, d.Logger))
				r.Post("/mfa", handleRivianMFA(d.Accounts, d.Secrets, d.Monitors, d.DB, d.Logger))
				r.Post("/logout", handleRivianLogout(d.Accounts, d.Secrets, d.Monitors))
			})

			// Home electricity cost settings, applied locally to estimate
			// the price of sessions Rivian reports as free (home AC, L2,
			// non-RAN public chargers).
			r.Route("/settings/charging", func(r chi.Router) {
				r.Get("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingSettingsGet(d.Settings.For(uid))(w, r)
				}))
				r.Put("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingSettingsPut(d.Settings.For(uid))(w, r)
				}))
				r.Get("/networks", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingNetworksGet(d.Settings.For(uid))(w, r)
				}))
				r.Put("/networks", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingNetworksPut(d.Settings.For(uid))(w, r)
				}))
				r.Post("/networks/reset", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleChargingNetworksReset(d.Settings.For(uid))(w, r)
				}))
			})

			// User's saved home location, used by the trip planner
			// for one-click Origin/Destination presets.
			r.Route("/settings/home-location", func(r chi.Router) {
				r.Get("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleHomeLocationGet(d.Settings.For(uid))(w, r)
				}))
				r.Put("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleHomeLocationPut(d.Settings.For(uid))(w, r)
				}))
			})

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
				r.Put("/", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
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
				r.Use(requireAdminMW(func(ctx context.Context, uid uuid.UUID) (string, error) {
					if d.DB == nil {
						return "", nil
					}
					return db.RoleFor(ctx, d.DB, uid)
				}))
				r.Get("/kill-switch", handleFlagsGet(d.Flags))
				r.Put("/kill-switch", handleFlagsKillPut(d.Flags))
				r.Put("/flags/trip-planner", handleFlagsTripPlannerPut(d.Flags))
				r.Get("/signup-cap", handleSignupCapGet(d.Flags, d.DB))
				r.Put("/signup-cap", handleSignupCapPut(d.Flags, d.DB))
				r.Put("/flags/ai-call-cap", handleAICallCapPut(d.Flags))
				r.Get("/users", handleAdminUsersList(d.DB))
				r.Get("/users/{id}", handleAdminUserDetail(d.DB))
				r.Post("/users", handleAdminUserCreate(d.DB, d.Users, d.Logger))
				r.Post("/users/{id}/role", handleAdminUserSetRole(d.DB))
				r.Post("/users/{id}/disabled", handleAdminUserSetDisabled(d.DB))
				r.Post("/users/{id}/sync-rivian", handleAdminUserSyncRivian(d.Accounts, d.DB, d.Logger))
				r.Post("/users/{id}/refresh-rivian-session", handleAdminUserRefreshRivianSession(d.Accounts, d.Secrets, d.DB, d.Logger))
				r.Delete("/users/{id}", handleAdminUserDelete(d.DB, d.Users, d.Logger))
				r.Get("/settings/ai", handleAISettingsGet(d.SettingsMgr))
				r.Put("/settings/ai", handleAISettingsPut(d.SettingsMgr))
				r.Get("/settings/ai/models/{provider}", handleAIModelsList(d.SettingsMgr))
				r.Post("/ai/ping", handleAIPing(d.SettingsMgr))
				r.Get("/settings/recap", handleRecapSettingsGet(d.SettingsMgr))
				r.Put("/settings/recap", handleRecapSettingsPut(d.SettingsMgr))
				r.Get("/settings/gps", handleGPSSettingsGet(d.SettingsMgr))
				r.Put("/settings/gps", handleGPSSettingsPut(d.SettingsMgr))
				if d.SignupRequests != nil {
					r.Get("/signup-requests", handleAdminSignupRequestsList(d.SignupRequests))
					r.Post("/signup-requests/{id}/approve", handleAdminSignupRequestApprove(d.DB, d.SignupRequests, d.Email, d.BaseURL, d.Logger))
					r.Post("/signup-requests/{id}/reject", handleAdminSignupRequestReject(d.SignupRequests))
				}
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
				//   2. planTripWithMultiStop with a known-Austin payload
				// Dumps every response together. Paste-back-to-debug.
				r.Get("/trips/plan/diag", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
					handleTripPlanDiag(clientFor(d, uid))(w, r)
				}))
			})

			// Read-only session/telemetry endpoints. Populated by either the
			// ElectraFi importer or the (future) live Rivian ingester.
			r.Get("/drives", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDrives(d.Drives.For(uid), d.Charges.For(uid), d.Settings.For(uid))(w, r)
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
			r.Get("/charges", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleCharges(d.Charges.For(uid), d.Settings.For(uid))(w, r)
			}))
			r.Delete("/charges/{id}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDeleteCharge(d.Charges.For(uid))(w, r)
			}))
			r.Patch("/charges/{id}/pricing", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handlePatchChargePricing(d.Charges.For(uid))(w, r)
			}))
			r.Get("/charges/clusters", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleChargeClusters(d.Charges.For(uid))(w, r)
			}))
			r.Get("/samples", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSamples(d.Samples.For(uid))(w, r)
			}))
			// Trip planner — pass-through to Rivian's
			// planTripWithMultiStop. Caller supplies origin +
			// destination + optional intermediate waypoints +
			// optional target arrival SoC. The handler resolves
			// the user's vehicle + current SoC from cache; the
			// gateway computes charging stops and per-leg numbers.
			// 404'd when the trip-planner feature flag is off.
			r.With(requireTripPlannerEnabledMW(d.Flags)).Post("/trips/plan", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleTripPlan(clientFor(d, uid), monitorFor(d, uid), d.DB, uid, d.SettingsMgr, d.Settings.For(uid), d.WeatherClient, d.WeatherCache)(w, r)
			}))
			// Multi-day plan is mounted in the AI-bound group below
			// (5-minute timeout). N sequential planTrip2 calls easily
			// blow this group's 30s budget; chi cancels mid-second-leg
			// and the SPA sees "Bad Gateway".
			// Saved trip templates. Inputs are required; plan/advice are
			// optional snapshots so reopening a saved trip can render
			// the map instantly while still letting the user re-plan
			// against current station / weather state.
			r.Get("/trips/saved", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSavedTripsList(d.Trips.For(uid))(w, r)
			}))
			r.Post("/trips/saved", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleSavedTripCreate(d.Trips.For(uid))(w, r)
			}))
			r.Put("/trips/saved/{id}", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
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
			r.Get("/geocode", withUser(func(_ uuid.UUID, w http.ResponseWriter, r *http.Request) {
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
			}
			if impersonateMW != nil {
				r.Use(impersonateMW)
			}
			r.Use(middleware.Timeout(5 * time.Minute))
			r.Use(maxBodyBytes(maxJSONBody))
			// Per-user daily cap on the two LLM-backed endpoints so a
			// single account can't run up the AI bill. Applied per-route
			// (not group-wide) so plan-multiday — Rivian calls, not LLM —
			// stays uncapped.
			aiBudgetMW := requireAIBudgetMW(d.Flags, d.AIBudget)
			r.With(aiBudgetMW).Post("/drives/{id}/efficiency", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDriveEfficiencyPost(d, uid)(w, r)
			}))
			r.With(aiBudgetMW).Post("/trips/plan/advice", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleTripPlanAdvice(d.SettingsMgr, d.Settings.For(uid))(w, r)
			}))
			// Multi-day orchestrator: N sequential planTrip2 calls.
			// Lives here so it inherits the 5-minute timeout — three
			// legs at ~10s each blow the timed-group's 30s budget.
			r.With(requireTripPlannerEnabledMW(d.Flags)).Post("/trips/plan-multiday", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleTripPlanMultiday(clientFor(d, uid), d.Settings.For(uid))(w, r)
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
			r.Post("/data/restore", handleDataRestore(d))
			r.Delete("/data/sessions", handleDataReset(d))
			r.Delete("/data/account", handleDataAccountDelete(d))
		}) // end of bulk-data authenticated /api group
	})

	// Everything else falls through to the embedded SPA. The SPA
	// itself is always reachable — it needs to render the login
	// page when the /api/auth/me bootstrap returns 401.
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
		"script-src 'self' https://static.cloudflareinsights.com; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob: https://rivian.com https://*.rivian.com https://basemaps.cartocdn.com https://*.basemaps.cartocdn.com; " +
		"font-src 'self' data:; " +
		"connect-src 'self' https://cloudflareinsights.com; " +
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
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
