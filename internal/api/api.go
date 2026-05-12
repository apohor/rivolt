// Package api wires the HTTP surface for Rivolt. It assembles routes,
// middleware, and handler dependencies into a single chi Mux.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	"github.com/apohor/rivolt/internal/analytics"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/idp"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/electrafi"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/invites"
	"github.com/apohor/rivolt/internal/signuprequests"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/recap"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/tripadvice"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/settings"
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
	// OIDC, when non-nil, mounts /api/auth/oidc/* — the third
	// auth issuer alongside static creds and trusted-proxy
	// header. nil disables the social-login button row in the
	// SPA but doesn't affect any other code path.
	OIDC    *oidc.Service
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
	WebFS   fs.FS
	Version string
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
	// Invites, when non-nil, enables the invite-code signup flow:
	// POST /api/signup validates + redeems a code and creates the
	// user, and POST+GET /api/admin/invite-codes generate / list
	// codes. nil disables the signup route (existing installs that
	// don't need invite-based onboarding are unaffected).
	Invites *invites.Store
	// SignupRequests, when non-nil, enables the public-facing
	// "request beta access" form at POST /api/signup/request and
	// the admin review surface at /api/admin/signup-requests/*.
	// Approve mints an invite via the Invites store and (if Email
	// is wired) emails the requester.
	SignupRequests *signuprequests.Store
	// Email, when non-nil, sends transactional mail (currently just
	// signup approvals) via the Resend HTTP API. nil disables the
	// approval-email send; the admin still gets the code in the
	// approval response so it can be forwarded manually.
	Email *email.Client
	// OSRMProxy, when non-nil, mounts a same-origin reverse
	// proxy at /api/maps/osrm/* that forwards to a self-hosted
	// OSRM (cluster Service typically). nil leaves the route
	// unmounted; the SPA falls back to the public OSRM demo.
	// /api/config advertises whether the proxy is mounted so the
	// SPA picks the right base URL at boot.
	OSRMProxy http.Handler
	// ValhallaProxy mirrors OSRMProxy for the Valhalla routing
	// engine. When non-nil, /api/maps/valhalla/* forwards to a
	// self-hosted Valhalla. /api/config advertises which engines
	// are available so the SPA can offer them as a user choice.
	// Both proxies can be set simultaneously — the user picks per
	// install which engine to use, OSRM stays as a fallback.
	ValhallaProxy http.Handler
	// TilesProxy, when non-nil, mounts a same-origin reverse
	// proxy at /api/maps/tiles/* that forwards to a self-hosted
	// PMTiles file server (nginx serving the .pmtiles bundle
	// with byte-range support). nil leaves the route unmounted;
	// the SPA falls back to CARTO's public dark raster basemap.
	TilesProxy http.Handler
	// Photon is the self-hosted geocoder client. Empty BaseURL on
	// the client disables; /api/geocode falls through to
	// Open-Meteo's city-level service.
	Photon *geocoding.PhotonClient
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

	r.Route("/api", func(r chi.Router) {
		// Health + auth endpoints stay reachable without a session,
		// otherwise the browser has no way to log in.
		r.Get("/health", handleHealth(d.Version))
		// /api/config advertises optional runtime knobs to the SPA
		// (today: whether the OSRM same-origin proxy is mounted).
		// Public so the SPA can fetch it before login as well as
		// after; reveals no user-scoped data.
		r.Get("/config", handleConfig(d.OSRMProxy != nil, d.ValhallaProxy != nil, d.TilesProxy != nil, d.SettingsMgr != nil && d.SettingsMgr.Analyzer() != nil, d.Flags, d.SettingsMgr))
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
		if d.Invites != nil {
			r.Post("/signup", handleSignup(d.DB, d.Invites, d.Users, d.Logger))
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
			r.With(signupReqLimiter.Middleware).Post(
				"/signup/request",
				handleSignupRequestCreate(d.SignupRequests, d.Logger),
			)
		}

		// Everything else sits behind requireUser when auth is
		// enforced. Unenforced mode (no issuer wired) keeps the
		// API open — legacy single-tenant docker-compose UX.
		r.Group(func(r chi.Router) {
			if d.AuthEnforced {
				r.Use(requireUserMW)
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

			// Same-origin OSRM proxy. Mounted only when the operator
			// configured RIVOLT_OSRM_BASE_URL — otherwise the route is
			// absent entirely and the SPA falls back to the public
			// demo. chi's Mount strips the prefix from r.URL.Path
			// itself; the proxy.Director then forwards as-is.
			if d.OSRMProxy != nil {
				r.Mount("/maps/osrm", http.StripPrefix("/api/maps/osrm", d.OSRMProxy))
			}
			if d.ValhallaProxy != nil {
				r.Mount("/maps/valhalla", http.StripPrefix("/api/maps/valhalla", d.ValhallaProxy))
			}
			if d.TilesProxy != nil {
				r.Mount("/maps/tiles", http.StripPrefix("/api/maps/tiles", d.TilesProxy))
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
				r.Get("/", handleRivianStatus(d.Accounts))
				r.Post("/login", handleRivianLogin(d.Accounts, d.Secrets, d.Monitors))
				r.Post("/mfa", handleRivianMFA(d.Accounts, d.Secrets, d.Monitors))
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
				r.Get("/users", handleAdminUsersList(d.DB))
				r.Post("/users", handleAdminUserCreate(d.DB, d.Users, d.Logger))
				r.Post("/users/{id}/role", handleAdminUserSetRole(d.DB))
				r.Post("/users/{id}/disabled", handleAdminUserSetDisabled(d.DB))
				r.Delete("/users/{id}", handleAdminUserDelete(d.DB, d.Users, d.Logger))
			r.Get("/settings/ai", handleAISettingsGet(d.SettingsMgr))
			r.Put("/settings/ai", handleAISettingsPut(d.SettingsMgr))
			r.Get("/settings/ai/models/{provider}", handleAIModelsList(d.SettingsMgr))
			r.Post("/ai/ping", handleAIPing(d.SettingsMgr))
			r.Get("/settings/recap", handleRecapSettingsGet(d.SettingsMgr))
			r.Put("/settings/recap", handleRecapSettingsPut(d.SettingsMgr))
			r.Get("/settings/gps", handleGPSSettingsGet(d.SettingsMgr))
			r.Put("/settings/gps", handleGPSSettingsPut(d.SettingsMgr))
			if d.Invites != nil {
				r.Post("/invite-codes", handleAdminInviteCodesCreate(d.DB, d.Invites))
				r.Get("/invite-codes", handleAdminInviteCodesList(d.Invites))
			}
			if d.SignupRequests != nil {
				r.Get("/signup-requests", handleAdminSignupRequestsList(d.SignupRequests))
				r.Post("/signup-requests/{id}/approve", handleAdminSignupRequestApprove(d.DB, d.SignupRequests, d.Invites, d.Email, d.Logger))
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
				handleTripPlan(clientFor(d, uid), monitorFor(d, uid), d.DB, uid)(w, r)
			}))
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
			r.Use(middleware.Timeout(5 * time.Minute))
			r.Post("/drives/{id}/efficiency", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleDriveEfficiencyPost(d, uid)(w, r)
			}))
			r.Post("/trips/plan/advice", withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
				handleTripPlanAdvice(d.SettingsMgr, d.Settings.For(uid))(w, r)
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
		"img-src 'self' data: blob: https://rivian.com https://*.rivian.com; " +
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

func handleHealth(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"version": version,
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// handleConfig advertises optional runtime knobs to the SPA. Today
// it returns whether the same-origin map proxies are mounted; the
// SPA uses the paths it returns as base URLs for OSRM (/match,
// /route) and PMTiles (drive map basemap), falling back to the
// public demos when a path is empty. Public so the SPA can fetch
// it without a session.
func handleConfig(osrmEnabled, valhallaEnabled, tilesEnabled, aiEnabled bool, flagsStore *flags.Store, settingsMgr *settings.Manager) http.HandlerFunc {
	type osrmCfg struct {
		// Path is the same-origin URL prefix the SPA should hit
		// (empty when the proxy is not configured server-side).
		Path string `json:"path,omitempty"`
	}
	type tilesCfg struct {
		// URL is the full same-origin URL of the served basemap
		// .pmtiles file (empty when not configured). protomaps-leaflet
		// fetches this with byte-range reads.
		URL string `json:"url,omitempty"`
		// ChargersURL is the same-origin URL of the chargers POI
		// .pmtiles archive (built from Geofabrik North America +
		// osmium + tippecanoe -- see apps/tiles/manifests/
		// chargers.yaml in rivolt-infra). Empty when the chargers
		// archive isn't deployed; the SPA then falls back to the
		// basemap pois layer for nearest-charger lookup, which is
		// less accurate (planet build strips POI tags down to
		// name/kind/min_zoom).
		ChargersURL string `json:"chargers_url,omitempty"`
	}
	type aiCfg struct {
		// Enabled is true when the install has at least one
		// AI provider configured with a working key+model. The
		// SPA reads it to gate AI-powered features (trip recap,
		// future weekly digest, etc.) so we don't render dead
		// buttons. Snapshot at /api/config request time -- a
		// follow-up Settings save flips this on the next page
		// reload.
		Enabled bool `json:"enabled"`
	}
	type featuresCfg struct {
		// TripPlannerEnabled gates the Plan nav link and route on
		// the SPA. Polled value, so flipping the admin toggle
		// takes effect on next page load (or whenever the SPA
		// re-fetches /api/config).
		TripPlannerEnabled bool `json:"trip_planner_enabled"`
	}
	type valhallaCfg struct {
		// Path is the same-origin URL prefix for Valhalla's HTTP
		// API. Empty means the proxy isn't wired and Valhalla
		// shouldn't be offered as an engine option.
		Path string `json:"path,omitempty"`
	}
	type gpsCfg struct {
		// MissingPct, StaleSec, JumpCount drive the "Low GPS accuracy"
		// pill on the drive detail page. Surfaced here so the SPA
		// reads them once on boot instead of re-querying per drive.
		MissingPct float64 `json:"missing_pct"`
		StaleSec   int     `json:"stale_sec"`
		JumpCount  int     `json:"jump_count"`
	}
	type cfg struct {
		OSRM     osrmCfg     `json:"osrm"`
		Valhalla valhallaCfg `json:"valhalla"`
		Tiles    tilesCfg    `json:"tiles"`
		AI       aiCfg       `json:"ai"`
		Features featuresCfg `json:"features"`
		GPS      gpsCfg      `json:"gps"`
	}
	base := cfg{}
	if osrmEnabled {
		base.OSRM.Path = "/api/maps/osrm"
	}
	if valhallaEnabled {
		base.Valhalla.Path = "/api/maps/valhalla"
	}
	if tilesEnabled {
		base.Tiles.URL = "/api/maps/tiles/us.pmtiles"
		// chargers.pmtiles lives next to us.pmtiles on the same
		// PVC and is served by the same nginx, so its presence is
		// gated on the same flag. If the chargers Job hasn't run
		// yet, the URL still resolves; the SPA's PMTiles client
		// will see a 404 on the file root and gracefully fall back.
		base.Tiles.ChargersURL = "/api/maps/tiles/chargers.pmtiles"
	}
	base.AI.Enabled = aiEnabled
	return func(w http.ResponseWriter, _ *http.Request) {
		c := base
		if flagsStore != nil {
			c.Features.TripPlannerEnabled = flagsStore.TripPlanner().Enabled
		}
		if settingsMgr != nil {
			g := settingsMgr.GPSPublic()
			c.GPS.MissingPct = g.MissingPct
			c.GPS.StaleSec = g.StaleSec
			c.GPS.JumpCount = g.JumpCount
		} else {
			c.GPS.MissingPct = settings.DefaultGPSMissingPct
			c.GPS.StaleSec = settings.DefaultGPSStaleSec
			c.GPS.JumpCount = settings.DefaultGPSJumpCount
		}
		writeJSON(w, http.StatusOK, c)
	}
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

// handleOwnedVehicles returns the calling user's vehicles straight
// from the local DB. Used by the SPA's import picker so the user can
// always see their existing vehicles, even when Rivian's gateway is
// unreachable. Returns {vehicles: [...]} so the wire shape can grow
// metadata fields without breaking existing clients.
func handleOwnedVehicles(sqlDB *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if sqlDB == nil {
			writeJSON(w, http.StatusOK, map[string]any{"vehicles": []any{}})
			return
		}
		vs, err := db.ListUserVehicles(r.Context(), sqlDB, uid)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if vs == nil {
			vs = []db.VehicleSummary{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"vehicles": vs})
	}
}

// handleVehicleProfileGet returns the per-vehicle profile JSON for
// the path-param vehicle (Rivian gateway id). Always returns 200 with
// a (possibly empty) profile object so the SPA settings form can bind
// without nil-checking — empty fields render as unset placeholders.
func handleVehicleProfileGet(sqlDB *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if sqlDB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		rivianID := chi.URLParam(r, "vehicleID")
		if rivianID == "" {
			http.Error(w, "missing vehicle id", http.StatusBadRequest)
			return
		}
		// Ownership middleware (vehicleScoped) has already checked
		// that uid owns rivianID. Resolve to the internal UUID
		// without upserting a new row — the vehicle must exist or
		// ownership wouldn't have passed.
		var vid uuid.UUID
		if err := sqlDB.QueryRowContext(r.Context(), `
			SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2
		`, uid, rivianID).Scan(&vid); err != nil {
			http.Error(w, "vehicle not found", http.StatusNotFound)
			return
		}
		p, err := db.GetVehicleProfile(r.Context(), sqlDB, uid, vid)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

// handleVehicleProfilePut writes the per-vehicle profile JSON into
// vehicles.metadata.profile. The full profile object replaces the
// stored value (no field-level merge): the SPA settings form sends
// the complete current state on every save.
func handleVehicleProfilePut(sqlDB *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if sqlDB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		rivianID := chi.URLParam(r, "vehicleID")
		if rivianID == "" {
			http.Error(w, "missing vehicle id", http.StatusBadRequest)
			return
		}
		var p db.VehicleProfile
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		// Light validation: clamp wheel size to plausible Rivian
		// fitments; reject implausible loads to keep prompt
		// numerically sane. These aren't security checks (the
		// fields go into a prompt, not an exec path) -- they keep
		// the model from anchoring on absurd values.
		if p.WheelInches != 0 && (p.WheelInches < 18 || p.WheelInches > 24) {
			http.Error(w, "wheel_inches out of range", http.StatusBadRequest)
			return
		}
		if p.DefaultExtraLoadLb < 0 || p.DefaultExtraLoadLb > 5000 {
			http.Error(w, "default_extra_load_lb out of range", http.StatusBadRequest)
			return
		}
		var vid uuid.UUID
		if err := sqlDB.QueryRowContext(r.Context(), `
			SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2
		`, uid, rivianID).Scan(&vid); err != nil {
			http.Error(w, "vehicle not found", http.StatusNotFound)
			return
		}
		if err := db.SetVehicleProfile(r.Context(), sqlDB, uid, vid, p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

func handleVehicles(c rivian.Client, mon *rivian.StateMonitor, sqlDB *sql.DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeJSON(w, http.StatusOK, []rivian.Vehicle{})
			return
		}
		vs, err := c.Vehicles(r.Context())
		if err != nil {
			// Stub client just hasn't been configured — empty list is
			// fine. Real failures (network, auth, upstream) surface so
			// the UI can say what's wrong.
			if errors.Is(err, rivian.ErrNotImplemented) {
				writeJSON(w, http.StatusOK, []rivian.Vehicle{})
				return
			}
			writeUpstreamError(w, err)
			return
		}
		// Prime the local `vehicles` table for the calling user. The
		// ownership middleware on /api/state/{vehicleID} et al. checks
		// this table, so a brand-new account with no recorded samples
		// would otherwise 404 forever (recorder writes are the only
		// other path that creates rows, but recording requires a WS
		// subscription that is itself gated by the ownership check).
		// One upsert per upstream vehicle on each /api/vehicles call
		// is cheap and idempotent.
		if sqlDB != nil {
			if userID, ok := auth.UserFromContext(r.Context()); ok {
				for i := range vs {
					if vs[i].ID == "" {
						continue
					}
					_, uerr := sqlDB.ExecContext(r.Context(), `
						INSERT INTO vehicles (user_id, rivian_vehicle_id, vin, display_name, model, model_year, pack_kwh)
						VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, 0)::int, NULLIF($7, 0)::double precision)
						ON CONFLICT (user_id, rivian_vehicle_id) DO UPDATE SET
							vin          = COALESCE(EXCLUDED.vin,          vehicles.vin),
							display_name = COALESCE(EXCLUDED.display_name, vehicles.display_name),
							model        = COALESCE(EXCLUDED.model,        vehicles.model),
							model_year   = COALESCE(EXCLUDED.model_year,   vehicles.model_year),
							pack_kwh     = COALESCE(EXCLUDED.pack_kwh,     vehicles.pack_kwh),
							updated_at   = NOW()
					`, userID, vs[i].ID, vs[i].VIN, vs[i].Name, vs[i].Model, vs[i].ModelYear, vs[i].PackKWh)
					if uerr != nil && logger != nil {
						logger.Warn("vehicles prime upsert failed",
							"user_id", userID.String(),
							"rivian_vehicle_id", vs[i].ID,
							"err", uerr.Error())
					}
				}
			}
		}
		// Enrich each vehicle with cached monitor metadata (PackKWh +
		// ImageURL), when available. The live Vehicles() call returns
		// trim/year/pack already, but image URLs come from a separate
		// Rivian endpoint cached only on the monitor.
		if mon != nil {
			missingInfo := false
			for i := range vs {
				if info := mon.VehicleInfo(vs[i].ID); info != nil {
					if vs[i].ImageURL == "" {
						vs[i].ImageURL = info.ImageURL
					}
					if len(vs[i].Images) == 0 {
						vs[i].Images = info.Images
					}
					if vs[i].PackKWh == 0 {
						vs[i].PackKWh = info.PackKWh
					}
				} else if vs[i].ID != "" {
					missingInfo = true
				}
			}
			// Cold-start: when the user links Rivian *after* server
			// boot, the monitor's cache is empty. Without this branch
			// the first /api/vehicles call returns vehicles with no
			// ImageURL, the frontend renders a placeholder, and only
			// a manual reload (after the async refresh below
			// completed) shows the real photo.
			//
			// Fix: on cold-start try the refresh synchronously with
			// a tight budget (the request context already caps the
			// upper bound). If it lands in time, re-enrich the
			// response so first-load already has images. If it
			// doesn't, fall back to the background path — the user
			// still sees a placeholder this once, but at least the
			// cache is warm for the next call.
			if missingInfo {
				rctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				err := mon.RefreshVehicleInfo(rctx)
				cancel()
				if err == nil {
					for i := range vs {
						if info := mon.VehicleInfo(vs[i].ID); info != nil {
							if vs[i].ImageURL == "" {
								vs[i].ImageURL = info.ImageURL
							}
							if len(vs[i].Images) == 0 {
								vs[i].Images = info.Images
							}
							if vs[i].PackKWh == 0 {
								vs[i].PackKWh = info.PackKWh
							}
						}
					}
				} else {
					if logger != nil {
						logger.Warn("post-login vehicle info refresh (sync) failed; falling back to async",
							"err", err.Error())
					}
					go func() {
						bgctx, bgcancel := context.WithTimeout(context.Background(), 20*time.Second)
						defer bgcancel()
						if rerr := mon.RefreshVehicleInfo(bgctx); rerr != nil {
							if logger != nil {
								logger.Warn("post-login vehicle info refresh (async) failed", "err", rerr.Error())
							}
						}
					}()
				}
			}
		}
		writeJSON(w, http.StatusOK, vs)
	}
}

// handleVehicleState returns a current snapshot for the given vehicle.
// 404 if no live client is configured, 502 for upstream failures.
//
// WS subscriptions are owned by the lease coordinator. A pod that
// doesn't own the lease serves cache hits from its local snapshot
// when present, and falls back to a one-shot REST fetch on miss —
// it does NOT open its own subscription. Two pods subscribed to the
// same Rivian session token would kick each other repeatedly and
// fragment drives at every WS bounce.
func handleVehicleState(c rivian.Client, mon *rivian.StateMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			http.Error(w, "no rivian client configured", http.StatusNotFound)
			return
		}
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		if mon != nil {
			if st, _ := mon.Latest(id); st != nil {
				writeJSON(w, http.StatusOK, st)
				return
			}
		}
		st, err := c.State(r.Context(), id)
		if err != nil {
			if errors.Is(err, rivian.ErrNotImplemented) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			writeUpstreamError(w, err)
			return
		}
		if mon != nil {
			mon.Prime(id, st)
		}
		writeJSON(w, http.StatusOK, st)
	}
}

// handleVehicleStateDebug returns the raw decoded vehicleState object
// from Rivian (as a JSON map) so we can see which fields upstream
// populates versus leaves null. Only works with a live client.
func handleVehicleStateDebug(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		raw, err := lc.StateRaw(r.Context(), id)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, raw)
	}
}

// handleVehicleStateFresh bypasses the monitor cache and returns the
// typed State from a direct REST call. Used to diagnose cache-vs-parse
// issues when /api/state shows zeros but /api/state/.../debug shows
// populated upstream fields.
func handleVehicleStateFresh(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			http.Error(w, "no rivian client configured", http.StatusNotFound)
			return
		}
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		st, err := c.State(r.Context(), id)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	}
}

// handleLiveSession returns the current charging session snapshot.
// Prefers the cached payload from the StateMonitor (populated by
// both the WebSocket ChargingSession subscription and the REST
// getLiveSessionHistory poller), falling back to a direct REST hit
// if nothing has been cached yet. The monitor cache is what carries
// home AC / L2 telemetry — REST alone returns active:false with a
// zeroed payload for those sessions.
//
// The response is decorated with an estimated_cost field computed
// from the user's configured home $/kWh rate. For sessions Rivian
// reports as free (home AC, L2 on non-RAN chargers) this is the
// only signal of what the charge cost.
func handleLiveSession(c rivian.Client, mon *rivian.StateMonitor, store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		cfg, _ := settings.GetChargingConfig(r.Context(), store)
		if mon != nil {
			if sess := mon.LatestLiveSession(id); sess != nil {
				writeJSON(w, http.StatusOK, decorateLiveSession(sess, cfg))
				return
			}
		}
		sess, err := lc.LiveSession(r.Context(), id)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, decorateLiveSession(sess, cfg))
	}
}

// tripPlanRequest is the SPA-facing input. VehicleID + StartingSoC
// can be omitted; the handler back-fills them from the monitor's
// state cache so callers don't repeat what the server already knows.
type tripPlanRequest struct {
	VehicleID               string                  `json:"vehicle_id"`
	StartingSoC             *float64                `json:"starting_soc,omitempty"`
	StartingRangeMeters     float64                 `json:"starting_range_meters,omitempty"`
	OriginBearing           float64                 `json:"origin_bearing"`
	Waypoints               []tripPlanWaypoint      `json:"waypoints"`
	TargetArrivalSocPercent *float64                `json:"target_arrival_soc_percent,omitempty"`
	DriveMode               string                  `json:"drive_mode,omitempty"`
	HasAdapter              *bool                   `json:"has_adapter,omitempty"`
	TrailerProfile          string                  `json:"trailer_profile,omitempty"`
	AvoidAdapterRequired    bool                    `json:"avoid_adapter_required,omitempty"`
	SupportedConnectorTypes []string                `json:"supported_connector_types,omitempty"`
	NetworkPreferences      []tripPlanNetworkPref   `json:"network_preferences,omitempty"`
}

type tripPlanWaypoint struct {
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	WaypointType string  `json:"waypoint_type"`
	EntityID     string  `json:"entity_id,omitempty"`
}

type tripPlanNetworkPref struct {
	NetworkID  string `json:"network_id"`
	Preference int    `json:"preference"`
}

// handleGeocode resolves a free-text query to a list of locations.
// When a self-hosted Photon is wired (RIVOLT_PHOTON_BASE_URL),
// queries hit Photon first for street-level resolution; on empty
// results or transient failure we fall back to Open-Meteo's city-
// level search. With Photon unwired the handler is just Open-Meteo.
func handleGeocode(photon *geocoding.PhotonClient) http.HandlerFunc {
	gc := geocoding.NewClient()
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if strings.TrimSpace(q) == "" {
			writeJSON(w, http.StatusOK, []geocoding.Result{})
			return
		}
		count := 5
		if c := r.URL.Query().Get("count"); c != "" {
			if n, err := strconv.Atoi(c); err == nil && n > 0 {
				count = n
			}
		}
		var results []geocoding.Result
		if photon.Enabled() {
			r1, err := photon.Search(r.Context(), q, count)
			if err == nil && len(r1) > 0 {
				results = r1
			}
		}
		if len(results) == 0 {
			r2, err := gc.Search(r.Context(), q, count)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
				return
			}
			results = r2
		}
		if results == nil {
			results = []geocoding.Result{}
		}
		writeJSON(w, http.StatusOK, results)
	}
}

// handleHomeLocationGet returns the user's saved home location, or
// {"set": false} when none is configured. Used by the trip planner
// to decide whether to render the "Home" preset.
func handleHomeLocationGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, err := settings.GetHomeLocation(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, h)
	}
}

// handleHomeLocationPut writes the user's home location. Body shape
// matches HomeLocation; passing Set=false clears the saved value.
func handleHomeLocationPut(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var h settings.HomeLocation
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := settings.SetHomeLocation(r.Context(), store, h); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Echo back what's now stored (post-validation) so the SPA
		// can update its in-memory copy without a second GET.
		out, _ := settings.GetHomeLocation(r.Context(), store)
		writeJSON(w, http.StatusOK, out)
	}
}

// handlePlannerPrefsGet returns the user's saved trip-planner
// defaults (drive mode + Tesla adapter). Used by the SPA to
// pre-fill the per-trip form.
func handlePlannerPrefsGet(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := settings.GetPlannerPrefs(r.Context(), store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

// handlePlannerPrefsPut persists planner defaults.
func handlePlannerPrefsPut(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p settings.PlannerPrefs
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := settings.SetPlannerPrefs(r.Context(), store, p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := settings.GetPlannerPrefs(r.Context(), store)
		writeJSON(w, http.StatusOK, out)
	}
}

// savedTripBody is the wire shape for create/update. Name is the only
// required field; plan/advice are optional snapshots from a successful
// /api/trips/plan + /api/trips/plan/advice round-trip.
type savedTripBody struct {
	Name   string          `json:"name"`
	Inputs json.RawMessage `json:"inputs"`
	Plan   json.RawMessage `json:"plan,omitempty"`
	Advice json.RawMessage `json:"advice,omitempty"`
}

func handleSavedTripsList(store *trips.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		out, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []trips.SavedTrip{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleSavedTripCreate(store *trips.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		var b savedTripBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(b.Name)
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if len(b.Inputs) == 0 {
			http.Error(w, "inputs required", http.StatusBadRequest)
			return
		}
		t, err := store.Create(r.Context(), name, b.Inputs, b.Plan, b.Advice)
		if err != nil {
			// Surface the unique (user_id, name) violation as 409 so
			// the SPA can prompt "name already exists, overwrite?".
			if isUniqueViolation(err) {
				http.Error(w, "trip name already in use", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, t)
	}
}

func handleSavedTripUpdate(store *trips.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var b savedTripBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(b.Name)
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if len(b.Inputs) == 0 {
			http.Error(w, "inputs required", http.StatusBadRequest)
			return
		}
		t, err := store.Update(r.Context(), id, name, b.Inputs, b.Plan, b.Advice)
		if errors.Is(err, trips.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			if isUniqueViolation(err) {
				http.Error(w, "trip name already in use", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func handleSavedTripDelete(store *trips.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		err = store.Delete(r.Context(), id)
		if errors.Is(err, trips.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// isUniqueViolation returns true for Postgres SQLSTATE 23505 so the
// saved-trips handlers can map "name already in use" to 409 without
// pulling in a dedicated pq error import. lib/pq + pgx both surface
// the code in the error string when wrapped in driver.Value Scanner;
// the canonical match is the pgconn.PgError.Code path. To stay driver-
// agnostic we string-match on the SQLSTATE prefix.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "SQLSTATE 23505") ||
		strings.Contains(err.Error(), "unique constraint") ||
		strings.Contains(err.Error(), "duplicate key value")
}

// handleTripPlan calls Rivian's planTripWithMultiStop. Slice 1 of
// the trip-planner feature: read-only pass-through with no AI, no
// save, no places search. Caller provides waypoint coordinates
// directly (geocoding lands in slice 2).
//
// vehicle_id and starting_soc may be omitted in the body — the
// handler back-fills them, preferring the per-pod monitor cache
// (zero-ms hot path), then falling back to the DB when this pod
// doesn't own the vehicle's lease (multi-pod path: the lease holder
// is a different replica, so this pod's monitor cache is empty for
// that vehicle).
func handleTripPlan(c rivian.Client, mon *rivian.StateMonitor, pool *sql.DB, uid uuid.UUID) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		var req tripPlanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Waypoints) < 2 {
			http.Error(w, "at least origin + destination waypoints required", http.StatusBadRequest)
			return
		}
		startingSoC := 0.0
		if req.StartingSoC != nil {
			startingSoC = *req.StartingSoC
		}
		// Prefer the in-memory monitor cache (fastest, freshest).
		if mon != nil && req.VehicleID == "" {
			for _, v := range mon.AllVehicleInfo() {
				req.VehicleID = v.ID
				break
			}
		}
		if mon != nil && req.StartingSoC == nil && req.VehicleID != "" {
			if st, _ := mon.Latest(req.VehicleID); st != nil {
				startingSoC = st.BatteryLevelPct
				req.StartingSoC = &startingSoC
				if req.StartingRangeMeters == 0 && st.DistanceToEmpty > 0 {
					req.StartingRangeMeters = st.DistanceToEmpty * 1000
				}
			}
		}
		// DB fallback when the monitor cache didn't cover us — the
		// typical multi-pod case where the OTHER replica holds the
		// lease for this user's vehicle and our cache is empty.
		if req.VehicleID == "" && pool != nil {
			vs, err := db.ListUserVehicles(r.Context(), pool, uid)
			if err == nil && len(vs) > 0 {
				req.VehicleID = vs[0].RivianVehicleID
			}
		}
		if req.VehicleID == "" {
			http.Error(w, "vehicle_id required (no vehicle linked to this user)", http.StatusBadRequest)
			return
		}
		if req.StartingSoC == nil && pool != nil {
			var soc, rangeMi sql.NullFloat64
			err := pool.QueryRowContext(r.Context(), `
				SELECT battery_level_pct, range_mi
				  FROM vehicle_state
				 WHERE user_id = $1
				   AND vehicle_id = (
				       SELECT id FROM vehicles
				        WHERE user_id = $1 AND rivian_vehicle_id = $2
				        LIMIT 1)
				 ORDER BY at DESC
				 LIMIT 1`, uid, req.VehicleID).Scan(&soc, &rangeMi)
			if err == nil && soc.Valid {
				startingSoC = soc.Float64
				if rangeMi.Valid && req.StartingRangeMeters == 0 {
					req.StartingRangeMeters = rangeMi.Float64 * 1609.34 // mi → m
				}
			}
		}
		if req.StartingSoC == nil && startingSoC == 0 {
			http.Error(w, "starting_soc required (no recent vehicle_state row for this vehicle)", http.StatusBadRequest)
			return
		}

		// Drop unknown drive_mode values before they reach Rivian's
		// GraphQL — the enum is strict and an unknown value fails the
		// whole query. Stale SPA bundles or bad clients can send
		// legacy labels (CONSERVE, ALL_PURPOSE) that aren't in the
		// gateway's enum.
		drive := ""
		switch req.DriveMode {
		case "", settings.DriveModeEveryday, settings.DriveModeDistance,
			settings.DriveModeSport, settings.DriveModeWinter,
			settings.DriveModeOffRoadAuto:
			drive = req.DriveMode
		}
		in := rivian.PlanTripInput{
			VehicleID:               req.VehicleID,
			StartingSoC:             startingSoC,
			StartingRangeMeters:     req.StartingRangeMeters,
			OriginBearing:           req.OriginBearing,
			TargetArrivalSocPercent: req.TargetArrivalSocPercent,
			DriveMode:               drive,
			HasAdapter:              req.HasAdapter,
			TrailerProfile:          req.TrailerProfile,
			AvoidAdapterRequired:    req.AvoidAdapterRequired,
			SupportedConnectorTypes: req.SupportedConnectorTypes,
		}
		for _, wp := range req.Waypoints {
			in.Waypoints = append(in.Waypoints, rivian.PlanTripWaypoint{
				Latitude:     wp.Latitude,
				Longitude:    wp.Longitude,
				WaypointType: wp.WaypointType,
				EntityID:     wp.EntityID,
			})
		}
		for _, np := range req.NetworkPreferences {
			in.NetworkPreferences = append(in.NetworkPreferences, rivian.NetworkPreference{
				NetworkID:  np.NetworkID,
				Preference: np.Preference,
			})
		}

		plan, err := lc.PlanTrip(r.Context(), in)
		if err != nil {
			slog.WarnContext(r.Context(), "trip plan failed",
				"vehicle_id", in.VehicleID,
				"waypoints", len(in.Waypoints),
				"err", err.Error())
			// Map upstream error class to an HTTP status the SPA
			// can render. 4xx passes through Cloudflare cleanly;
			// 5xx gets replaced with Cloudflare's HTML error page.
			writeUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, plan)
	}
}

// handleTripPlanAdvice takes a completed TripPlan (returned by
// /api/trips/plan) plus minimal context labels, calls the configured
// AI provider, and returns a short structured analysis: headline +
// 2-4 plain-language insights. Lives in the AI-bound route group so
// the 5-minute timeout applies.
func handleTripPlanAdvice(mgr *settings.Manager, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil || mgr.Analyzer() == nil {
			http.Error(w, "AI provider not configured", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Plan              *rivian.TripPlan `json:"plan"`
			Origin            string           `json:"origin"`
			Destination       string           `json:"destination"`
			DriveMode         string           `json:"drive_mode"`
			StartingSoC       float64          `json:"starting_soc"`
			HasAdapter        bool             `json:"has_adapter"`
			TireFLBar         float64          `json:"tire_fl_bar"`
			TireFRBar         float64          `json:"tire_fr_bar"`
			TireRLBar         float64          `json:"tire_rl_bar"`
			TireRRBar         float64          `json:"tire_rr_bar"`
			TirePlacardPSI    float64          `json:"tire_placard_psi"`
			PackKWh           float64          `json:"pack_kwh"`
			DepartureDatetime string           `json:"departure_datetime"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Plan == nil {
			http.Error(w, "plan required", http.StatusBadRequest)
			return
		}
		tc := tripadvice.Context{
			OriginLabel:      body.Origin,
			DestinationLabel: body.Destination,
			DriveMode:        body.DriveMode,
			StartingSoC:      body.StartingSoC,
			HasAdapter:       body.HasAdapter,
			TirePressureBars: [4]float64{body.TireFLBar, body.TireFRBar, body.TireRLBar, body.TireRRBar},
			TirePlacardPSI:   body.TirePlacardPSI,
			PackKWh:          body.PackKWh,
		}
		// Pull the user's at-home charging rate so the cost section
		// of the advice can quote real numbers, not assumptions.
		if settingsStore != nil {
			if cfg, err := settings.GetChargingConfig(r.Context(), settingsStore); err == nil {
				tc.HomePricePerKWh = cfg.HomePricePerKWh
				tc.HomeCurrency = cfg.HomeCurrency
			}
		}
		// Fetch weather at the origin when the operator has enabled the
		// weather feature. Use the user-supplied departure datetime when
		// present so a future plan gets a forecast for that hour, not now.
		// Best-effort: a failure just omits the context.
		if mgr.RecapWeatherEnabled() {
			if lat, lon, ok := originLatLon(body.Plan); ok {
				at := time.Now()
				if body.DepartureDatetime != "" {
					if t, err := time.Parse(time.RFC3339, body.DepartureDatetime); err == nil {
						at = t
					}
				}
				if snap, _, err := weather.NewClient().FetchHour(r.Context(), lat, lon, at, 0, false); err == nil {
					tc.Weather = snap
				}
			}
		}
		result, err := tripadvice.Generate(r.Context(), mgr.Analyzer(), body.Plan, tc)
		if err != nil {
			slog.WarnContext(r.Context(), "trip advice generation failed", "err", err.Error())
			http.Error(w, "AI analysis failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		type response struct {
			Headline   string                  `json:"headline"`
			Cost       []string                `json:"cost"`
			Efficiency []string                `json:"efficiency"`
			Weather    []string                `json:"weather"`
			Vehicle    []string                `json:"vehicle"`
			CostEst    tripadvice.CostEstimate `json:"cost_estimate"`
			Model      string                  `json:"model"`
		}
		nonNil := func(s []string) []string {
			if s == nil {
				return []string{}
			}
			return s
		}
		var resp response
		resp.Model = result.Model
		resp.CostEst = result.Cost
		if result.Parsed != nil {
			resp.Headline = result.Parsed.Headline
			resp.Cost = nonNil(result.Parsed.Cost)
			resp.Efficiency = nonNil(result.Parsed.Efficiency)
			resp.Weather = nonNil(result.Parsed.Weather)
			resp.Vehicle = nonNil(result.Parsed.Vehicle)
		} else {
			resp.Cost = []string{}
			resp.Efficiency = []string{}
			resp.Weather = []string{}
			resp.Vehicle = []string{}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// originLatLon extracts the lat/lon of the origin waypoint from the
// first route in a plan. Returns ok=false when no origin is found.
func originLatLon(plan *rivian.TripPlan) (lat, lon float64, ok bool) {
	if plan == nil {
		return 0, 0, false
	}
	for _, r := range plan.Routes {
		for _, w := range r.Waypoints {
			if strings.EqualFold(w.WaypointType, "origin") && (w.Latitude != 0 || w.Longitude != 0) {
				return w.Latitude, w.Longitude, true
			}
		}
	}
	return 0, 0, false
}

// handleTripPlanRawDebug forwards an arbitrary variables JSON to
// Rivian's planTripWithMultiStop and returns the gateway's response
// or error verbatim. Admin-only via the chi.Route("/admin") group
// it lives in. Used to reverse-engineer schema/value mismatches
// without round-tripping through chart bumps.
func handleTripPlanRawDebug(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body.Variables) == 0 {
			http.Error(w, "variables required (object key 'variables')", http.StatusBadRequest)
			return
		}
		data, err := lc.PlanTripRaw(r.Context(), body.Variables)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
}

// handleGraphQLRaw posts an arbitrary GraphQL document to the
// gateway. Body: {"operation": "...", "query": "...", "variables": {...}}.
// Returns {data, errors} verbatim from upstream — a 200 here means
// the wire roundtrip happened, errors land in the body. Used to
// reverse-engineer schemas that ban introspection.
func handleGraphQLRaw(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		var body struct {
			Operation string         `json:"operation"`
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Query == "" {
			http.Error(w, "query is required", http.StatusBadRequest)
			return
		}
		data, err := lc.RawGraphQL(r.Context(), body.Operation, body.Query, body.Variables)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
}

// handleGraphQLIntrospect runs an __type introspection on the
// gateway and returns the response verbatim. Lets us read the exact
// input-object shape the gateway publishes, which is the
// authoritative answer for "what fields does CoordinatesInput
// accept?"
func handleGraphQLIntrospect(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name query parameter required (e.g. ?name=CoordinatesInput)", http.StatusBadRequest)
			return
		}
		data, err := lc.IntrospectInputType(r.Context(), name)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
}

// handleTripPlanDiag is the mobile-friendly diagnostic endpoint
// for the trip planner. Slimmed down after slice 1 shipped: runs
// one known-good v2 call (operation planTripWithMultiStopV2 / field
// planTrip2) with literal values inlined, plus the introspection
// probe on CoordinatesInput as a sanity check that the gateway is
// answering us. Click in browser bar → paste response to debug.
// Admin-only via the route group.
//
// History (deleted): earlier versions had ~10 variant-axis tests
// (waypointType / driveMode / connector lists / soc-as-fraction)
// + 7-name introspection probe + 7-name input-type probe + the
// v1 `planTrip` and broken `planTripMultiStop` shapes. They served
// their purpose during reverse engineering and are gone now —
// kept here as comments so a future operator can tell what was
// already tried:
//
//   v0.17.118  intro of diag (variants: full + minimal)
//   v0.17.121  v119_* fan-out (10 single-axis variants)
//   v0.17.122  query_axes (response-set + planTrip-legacy probes)
//   v0.17.124  added breaker bypass for diag-only calls
//   v0.17.125  full_spec_with_entityid (Place ID test)
//   v0.17.127  v126_correct_schema (planTrip + origin/destination)
//   v0.17.128  v128_v2_schema (planTrip2 with declared types)
//   v0.17.129  inline + 7 input-type-name probe
//   v0.17.130  inlined query merged into the typed PlanTrip path
func handleTripPlanDiag(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "live rivian client required", http.StatusNotFound)
			return
		}
		out := map[string]any{}

		// Known-good v2 inlined call. Returns real plans when the
		// upstream is healthy; an INTERNAL_SERVER_ERROR here means
		// Rivian's planner is degraded again.
		v2Query := `query planTripWithMultiStopV2 {
  planTrip2(
    waypoints: [
      {latitude: 30.5538, longitude: -97.7622},
      {latitude: 32.7767, longitude: -96.797}
    ],
    vehicle: "01-242521064",
    startingSoc: 54.0,
    startingRangeMeters: 270000.0,
    targetArrivalSocPercent: 20.0
  ) {
    status
    plans {
      summary {
        destinationReachable
        socBelowLimitAtDestination
        totalChargeDurationSeconds
        totalDriveDurationSeconds
        totalDriveDistanceMeters
        totalTripDurationSeconds
        arrivalSOCPercent
        arrivalRangeMeters
        arrivalEnergyKwh
      }
      waypoints {
        waypointType
        latitude
        longitude
        arrivalSOCPercent
        departureSOCPercent
        arrivalRangeMeters
        departureRangeMeters
        chargeDurationSeconds
      }
    }
  }
}`
		if data, err := lc.RawGraphQL(r.Context(), "planTripWithMultiStopV2", v2Query, map[string]any{}); err != nil {
			out["plan_trip_v2"] = map[string]any{"error": err.Error()}
		} else {
			out["plan_trip_v2"] = map[string]any{"data": data}
		}

		// Light gateway health check — succeeds when the gateway
		// answers introspection at all (currently always rejects
		// it, GRAPHQL_VALIDATION_FAILED). Useful only as a sentinel
		// in case Rivian re-enables introspection in the future.
		if data, err := lc.IntrospectInputType(r.Context(), "CoordinatesInput"); err != nil {
			out["introspect_sentinel"] = map[string]any{"error": err.Error()}
		} else {
			out["introspect_sentinel"] = map[string]any{"data": data}
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// handleLiveDrive returns a snapshot of the in-flight drive session
// for a vehicle, or 204 when none is active. Analogous to
// handleLiveSession for charges. The monitor is the sole source of
// truth — there's no REST fallback because Rivian exposes no drive
// equivalent of getLiveSessionHistory, and the snapshot is derived
// entirely from locally-observed telemetry frames.
func handleLiveDrive(mon *rivian.StateMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		if mon == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		drive := mon.ActiveDrive(id)
		if drive == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, drive)
	}
}

// liveSessionResponse is the wire shape for /api/live-session/:id —
// the base LiveSession plus locally-computed estimated cost when the
// user has set a home $/kWh rate and the Rivian-reported price
// is absent.
type liveSessionResponse struct {
	*rivian.LiveSession
	EstimatedCost     float64 `json:"estimated_cost,omitempty"`
	EstimatedCurrency string  `json:"estimated_currency,omitempty"`
}

func decorateLiveSession(sess *rivian.LiveSession, cfg settings.ChargingConfig) liveSessionResponse {
	resp := liveSessionResponse{LiveSession: sess}
	if sess == nil {
		return resp
	}
	// Only compute when we have both a configured rate and observed
	// energy. Don't overwrite a Rivian-reported price — those come
	// from RAN / Wall Charger sessions where the real billing rate
	// is authoritative.
	if cfg.HomePricePerKWh > 0 && sess.TotalChargedEnergyKWh > 0 && sess.CurrentPrice == "" {
		resp.EstimatedCost = cfg.HomePricePerKWh * sess.TotalChargedEnergyKWh
		resp.EstimatedCurrency = cfg.HomeCurrency
	}
	return resp
}

// handleChargingSchemaProbe introspects the chrg/user/graphql
// endpoint and returns the list of query fields + their args. Used
// when upstream renames a field (e.g. getLiveSessionData →
// getSessionStatus) to discover the new shape without deploying a
// blind guess.
func handleChargingSchemaProbe(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		data, err := lc.ChargingSchemaProbe(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, data)
	}
}

// handleChargingFieldProbe fires a deliberately wrong query for the
// named charging-endpoint field and returns Rivian's validation
// error, which lists the required args and subfields. ?vehicleID=...
// opts into passing a vehicleId argument.
func handleChargingFieldProbe(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		field := chi.URLParam(r, "field")
		vid := r.URL.Query().Get("vehicleID")
		sel := r.URL.Query().Get("sel")
		data, err := lc.ChargingFieldProbeWithSelection(r.Context(), field, vid, sel)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, data)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// handleChargingFrames returns the ring buffer of recent raw
// ChargingSession WS frames. Filter with ?vehicleID=... for a
// specific vehicle.
func handleChargingFrames(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, lc.RecentChargingFrames(r.URL.Query().Get("vehicleID")))
	}
}

func handleDrives(store *drives.Store, chargesStore *charges.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := store.ListRecent(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Drop ignition-cycle / gear-bounce rows so the SPA list
		// shows only real travel. The rows still exist in DB.
		out = drives.FilterReparking(out)
		cfg, _ := settings.GetChargingConfig(r.Context(), settingsStore)
		// Pull every charge once and sort ascending by EndedAt so we
		// can binary-search for the most recent charge that closed
		// before each drive started. Drive cost is then billed at
		// that charge's rate — a drive after fast-charging gets the
		// fast-charge rate, a drive after a home top-up gets the
		// home rate. Falls back to a blended rate for drives that
		// happened before the first known charge.
		priced := loadPricedCharges(r.Context(), chargesStore, cfg)
		fallbackRate, fallbackCur := computeBlendedRate(priced, cfg)
		decorated := make([]driveResponse, 0, len(out))
		for _, d := range out {
			rate, cur := rateForDrive(d, priced, fallbackRate, fallbackCur)
			decorated = append(decorated, decorateDrive(d, rate, cur))
		}
		writeJSON(w, http.StatusOK, decorated)
	}
}

// driveResponse is the wire shape for /api/drives: the stored drive
// row plus a locally-computed cost estimate based on the most recent
// charge that ended before the drive started (with a blended-rate
// fallback for drives that predate the first known charge).
type driveResponse struct {
	drives.Drive
	EstimatedCost     float64 `json:"estimated_cost,omitempty"`
	EstimatedCurrency string  `json:"estimated_currency,omitempty"`
	// EstimatedPricePerKWh is the rate used to compute EstimatedCost
	// — sourced from the most recent prior charge (or a blended
	// fallback for drives that predate the first known charge).
	// Surfaced so the UI can render "~$5.23 at $0.14/kWh" instead
	// of treating the cost as a hard number.
	EstimatedPricePerKWh float64 `json:"estimated_price_per_kwh,omitempty"`
}

func decorateDrive(d drives.Drive, rate float64, cur string) driveResponse {
	resp := driveResponse{Drive: d}
	if rate > 0 && d.EnergyUsedKWh > 0 {
		resp.EstimatedCost = rate * d.EnergyUsedKWh
		resp.EstimatedCurrency = cur
		resp.EstimatedPricePerKWh = rate
	}
	return resp
}

// pricedCharge is a normalized view of a charge row used for drive
// cost lookup: ended-at + a usable per-kWh rate + currency. Rows
// without a usable rate are skipped at load time.
type pricedCharge struct {
	endedAt time.Time
	rate    float64
	cur     string
}

// loadPricedCharges fetches every charge for the user, derives a
// per-kWh rate (persisted PricePerKWh, or persisted Cost / Energy,
// or the configured home rate as fallback), and returns the slice
// sorted ascending by EndedAt. Empty slice on store errors.
func loadPricedCharges(ctx context.Context, store *charges.Store, cfg settings.ChargingConfig) []pricedCharge {
	if store == nil {
		return nil
	}
	rows, err := store.ListAll(ctx)
	if err != nil {
		return nil
	}
	out := make([]pricedCharge, 0, len(rows))
	for _, c := range rows {
		if c.EnergyAddedKWh <= 0 {
			continue
		}
		rate, cur := chargeRate(c, cfg)
		if rate <= 0 {
			continue
		}
		out = append(out, pricedCharge{endedAt: c.EndedAt, rate: rate, cur: cur})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].endedAt.Before(out[j].endedAt) })
	return out
}

// chargeRate picks the best $/kWh for a single charge row. Persisted
// PricePerKWh (set when Rivian or the user's configured home rate
// stamped the row at close time) wins. If only Cost is set, derive
// rate from Cost/Energy. Otherwise fall back to the current home
// rate so legacy / unpriced rows still contribute a sensible value.
func chargeRate(c charges.Charge, cfg settings.ChargingConfig) (float64, string) {
	if c.PricePerKWh > 0 {
		return c.PricePerKWh, c.Currency
	}
	if c.Cost > 0 && c.EnergyAddedKWh > 0 {
		return c.Cost / c.EnergyAddedKWh, c.Currency
	}
	if cfg.HomePricePerKWh > 0 {
		return cfg.HomePricePerKWh, cfg.HomeCurrency
	}
	return 0, ""
}

// rateForDrive looks up the most recent charge that ended at or
// before d.StartedAt. Returns the fallback when the drive predates
// every known charge.
func rateForDrive(d drives.Drive, priced []pricedCharge, fallbackRate float64, fallbackCur string) (float64, string) {
	if len(priced) == 0 {
		return fallbackRate, fallbackCur
	}
	// sort.Search returns the smallest index where endedAt > drive
	// start; the most recent charge that ended before is at idx-1.
	start := d.StartedAt
	idx := sort.Search(len(priced), func(i int) bool {
		return priced[i].endedAt.After(start)
	})
	if idx == 0 {
		return fallbackRate, fallbackCur
	}
	pc := priced[idx-1]
	return pc.rate, pc.cur
}

// computeBlendedRate returns Σ(cost) / Σ(energy) across every priced
// charge plus the dominant currency. Used as the fallback rate for
// drives that predate the first known charge.
func computeBlendedRate(priced []pricedCharge, cfg settings.ChargingConfig) (float64, string) {
	if len(priced) == 0 {
		return cfg.HomePricePerKWh, cfg.HomeCurrency
	}
	var totalCost, totalEnergy float64
	currencies := map[string]float64{}
	// We only have rate + endedAt here, not energy, so weight every
	// session equally. That's fine — this is just the fallback for
	// pre-first-charge drives.
	for _, pc := range priced {
		totalCost += pc.rate
		totalEnergy += 1
		currencies[pc.cur]++
	}
	if totalEnergy <= 0 {
		return cfg.HomePricePerKWh, cfg.HomeCurrency
	}
	dominant := cfg.HomeCurrency
	var top float64
	for cur, n := range currencies {
		if n > top {
			top = n
			dominant = cur
		}
	}
	return totalCost / totalEnergy, dominant
}

func handleCharges(store *charges.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := store.ListRecent(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cfg, _ := settings.GetChargingConfig(r.Context(), settingsStore)
		decorated := make([]chargeResponse, 0, len(out))
		for _, c := range out {
			decorated = append(decorated, decorateCharge(c, cfg))
		}
		writeJSON(w, http.StatusOK, decorated)
	}
}

// handleDeleteCharge removes a single charge row by external ID,
// scoped to the authenticated user. 204 on success, 404 if no row
// matched, 500 on a DB error. The store filters by user_id so a
// caller can't reach into another user's data.
func handleDeleteCharge(store *charges.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "charges disabled", http.StatusServiceUnavailable)
			return
		}
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		n, err := store.DeleteByExternalID(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n == 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handlePatchChargePricing accepts {cost?, currency?, price_per_kwh?}
// and overwrites those three columns on the matching charge. Any
// missing/zero field clears its column, letting the API-layer
// fallbacks (recent-charge rate, home rate) take over again on the
// next read. Returns 204/404/400/500.
func handlePatchChargePricing(store *charges.Store) http.HandlerFunc {
	type body struct {
		Cost        *float64 `json:"cost"`
		Currency    *string  `json:"currency"`
		PricePerKWh *float64 `json:"price_per_kwh"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "charges disabled", http.StatusServiceUnavailable)
			return
		}
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		var b body
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		var cost, ppk float64
		var cur string
		if b.Cost != nil {
			cost = *b.Cost
		}
		if b.PricePerKWh != nil {
			ppk = *b.PricePerKWh
		}
		if b.Currency != nil {
			cur = strings.ToUpper(strings.TrimSpace(*b.Currency))
		}
		// Reject negatives — the column is unsigned in spirit even
		// though Postgres NUMERIC is signed.
		if cost < 0 || ppk < 0 {
			http.Error(w, "values must be non-negative", http.StatusBadRequest)
			return
		}
		n, err := store.UpdatePricing(r.Context(), id, cost, cur, ppk)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n == 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// chargeResponse is the wire shape for /api/charges: the stored
// charge row plus a locally-computed estimated cost when the
// user has set a home $/kWh rate. Cost is only attached when
// both the rate and the observed energy are non-zero.
type chargeResponse struct {
	charges.Charge
	EstimatedCost     float64 `json:"estimated_cost,omitempty"`
	EstimatedCurrency string  `json:"estimated_currency,omitempty"`
}

func decorateCharge(c charges.Charge, cfg settings.ChargingConfig) chargeResponse {
	resp := chargeResponse{Charge: c}
	// Persisted cost wins: it was snapshotted at the rate in effect
	// when the session closed. Only fall back to the current rate
	// for legacy rows (imports, pre-v0.3.29 live) that have no
	// persisted cost.
	if c.Cost > 0 {
		return resp
	}
	if cfg.HomePricePerKWh > 0 && c.EnergyAddedKWh > 0 {
		resp.EstimatedCost = cfg.HomePricePerKWh * c.EnergyAddedKWh
		resp.EstimatedCurrency = cfg.HomeCurrency
	}
	return resp
}

// handleSamples serves raw vehicle_state rows newer than ?since=<rfc3339>
// (default: 24h ago), capped at ?limit= (default 1000, max 10000).
// Optional ?until=<rfc3339> bounds the upper end so callers like the
// drive detail page don't pull every post-drive sample through now.
func handleSamples(store *samples.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		since := time.Now().Add(-24 * time.Hour)
		if s := r.URL.Query().Get("since"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				since = t
			}
		}
		var until time.Time
		if s := r.URL.Query().Get("until"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				until = t
			}
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := store.ListBetween(r.Context(), since, until, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []samples.Sample{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleImportElectrafi accepts one or more CSV files in a multipart
// upload under the field name "file" and streams each through the
// ElectraFi importer. Returns per-file results as JSON. The upload is
// rejected if any required store is unavailable; we don't want partial
// imports that silently drop samples or charge sessions.
//
// A 1 GiB cap guards against accidental large uploads; ElectraFi
// exports for a single month are typically 30-50 MiB.
func handleImportElectrafi(d Deps) http.HandlerFunc {
	const maxUpload = 1 << 30 // 1 GiB
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ds := d.Drives.For(uid)
		cs := d.Charges.For(uid)
		ss := d.Samples.For(uid)
		if ds == nil || cs == nil || ss == nil {
			http.Error(w, "import unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "parse upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		// vehicle_id is the rivian-gateway id the user picked in the
		// SPA. We validate ownership server-side so a crafted upload
		// can't land samples under another user's vehicle.
		rivianVehicleID := strings.TrimSpace(r.FormValue("vehicle_id"))
		if rivianVehicleID == "" {
			http.Error(w, "vehicle_id form field is required", http.StatusBadRequest)
			return
		}
		owns, oerr := db.OwnsRivianID(r.Context(), d.DB, uid, rivianVehicleID)
		if oerr != nil {
			http.Error(w, "vehicle ownership check: "+oerr.Error(), http.StatusInternalServerError)
			return
		}
		if !owns {
			// 404 (not 403) so an attacker probing vehicle ids can't
			// confirm whether a given id belongs to someone else.
			http.Error(w, "vehicle not found", http.StatusNotFound)
			return
		}
		files := r.MultipartForm.File["file"]
		if len(files) == 0 {
			http.Error(w, "no files uploaded under field 'file'", http.StatusBadRequest)
			return
		}
		imp := &electrafi.Importer{
			Drives:    ds,
			Charges:   cs,
			Samples:   ss,
			VehicleID: rivianVehicleID,
		}
		if v := r.FormValue("pack_kwh"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				imp.PackKWh = f
			}
		}
		// tz picks the timezone the CSV timestamps were recorded in;
		// ElectraFi exports are local-without-zone so parsing as UTC
		// (the pre-v0.4.2 default) shifts every row. Default to the
		// server's local zone, which matches the typical self-hosted
		// setup.
		tz := strings.TrimSpace(r.FormValue("tz"))
		if tz == "" {
			tz = "Local"
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			http.Error(w, "invalid tz "+strconv.Quote(tz)+": "+err.Error(), http.StatusBadRequest)
			return
		}
		imp.Location = loc

		// Stream results as NDJSON. Most default nginx setups
		// close idle upstream connections after ~60s, producing a 504
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no") // nginx hint
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		enc := json.NewEncoder(w)
		emit := func(v any) {
			_ = enc.Encode(v)
			if flusher != nil {
				flusher.Flush()
			}
		}
		emit(map[string]any{"event": "start", "files": len(files)})

		results := make([]electrafi.Result, 0, len(files))
		var firstErr string
		for i, fh := range files {
			f, err := fh.Open()
			if err != nil {
				if firstErr == "" {
					firstErr = fh.Filename + ": open: " + err.Error()
				}
				emit(map[string]any{"event": "error", "file": fh.Filename, "error": "open: " + err.Error()})
				continue
			}
			emit(map[string]any{"event": "file_start", "index": i, "file": fh.Filename})
			// Heartbeat inside a single file. Large CSVs have 20k+
			// rows and can easily spend >60s parsing + inserting;
			// without an in-flight progress line the proxy idles out.
			idx := i
			name := fh.Filename
			imp.OnProgress = func(phase string, n int) {
				emit(map[string]any{
					"event": "progress",
					"index": idx,
					"file":  name,
					"phase": phase,
					"rows":  n,
				})
			}
			res, err := imp.ImportReader(r.Context(), fh.Filename, f)
			f.Close()
			if err != nil {
				if firstErr == "" {
					firstErr = fh.Filename + ": " + err.Error()
				}
				emit(map[string]any{"event": "error", "file": fh.Filename, "error": err.Error()})
				continue
			}
			results = append(results, res)
			emit(map[string]any{"event": "file_done", "index": i, "result": res})
		}
		// If at least one file succeeded, still emit `done` with the
		// successful slice so the UI can display partial results
		// alongside the error. Pure-failure imports surface only the
		// per-file `error` events the client already knows how to
		// render.
		if firstErr != "" && len(results) == 0 {
			emit(map[string]any{"event": "error", "error": firstErr})
			return
		}
		emit(map[string]any{"event": "done", "files": results, "error": firstErr})
	}
}

// handleDataBackup streams a single JSON bundle containing every
// drive, charge, and raw sample for the current user. Intended to
// be paired with the reset endpoint so a user can snapshot
// their data before wiping it. The response is served with a
// Content-Disposition attachment so browsers download it directly;
// nothing is kept server-side.
func handleDataBackup(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ds := d.Drives.For(uid)
		cs := d.Charges.For(uid)
		ss := d.Samples.For(uid)
		if ds == nil || cs == nil || ss == nil {
			http.Error(w, "backup unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		ctx := r.Context()
		drv, err := ds.ListAll(ctx)
		if err != nil {
			http.Error(w, "list drives: "+err.Error(), http.StatusInternalServerError)
			return
		}
		chg, err := cs.ListAll(ctx)
		if err != nil {
			http.Error(w, "list charges: "+err.Error(), http.StatusInternalServerError)
			return
		}
		smp, err := ss.ListAll(ctx)
		if err != nil {
			http.Error(w, "list samples: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// User settings (charging cost, currency, home location,
		// trip planner defaults, drive mode, display prefs, etc.).
		// Flat key/value map — survives schema changes because the
		// store itself is schemaless.
		var userSettings map[string]string
		if ss2 := d.Settings.For(uid); ss2 != nil {
			if m, serr := ss2.GetAll(ctx); serr == nil {
				userSettings = m
			}
		}
		if userSettings == nil {
			userSettings = map[string]string{}
		}
		// Per-vehicle profile (pack capacity, tire placard PSI,
		// wheel size, accessories, frequently-tows flag). The
		// vehicle row itself includes its rivian_vehicle_id so a
		// future restore can match it back to the right truck.
		type profileEntry struct {
			RivianVehicleID string             `json:"rivian_vehicle_id"`
			DisplayName     string             `json:"display_name,omitempty"`
			Profile         db.VehicleProfile  `json:"profile"`
		}
		profiles := []profileEntry{}
		if d.DB != nil {
			vehs, verr := db.ListUserVehicles(ctx, d.DB, uid)
			if verr == nil {
				for _, v := range vehs {
					p, perr := db.GetVehicleProfile(ctx, d.DB, uid, v.ID)
					if perr != nil {
						// best-effort: skip vehicles whose profile
						// can't be read rather than failing the whole
						// backup over one bad row.
						continue
					}
					profiles = append(profiles, profileEntry{
						RivianVehicleID: v.RivianVehicleID,
						DisplayName:     v.DisplayName,
						Profile:         p,
					})
				}
			}
		}
		stamp := time.Now().UTC().Format("20060102-150405")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition",
			`attachment; filename="rivolt-backup-`+stamp+`.json"`)
		writeJSON(w, http.StatusOK, map[string]any{
			"version":          d.Version,
			"created_at":       time.Now().UTC().Format(time.RFC3339),
			"drives":           drv,
			"charges":          chg,
			"samples":          smp,
			"user_settings":    userSettings,
			"vehicle_profiles": profiles,
		})
	}
}

// handleDataRestore accepts a previously downloaded backup bundle
// (see handleDataBackup) and upserts every drive/charge/sample
// into the current user's stores. Existing rows with the same
// external_id (drives/charges) or (vehicle_id, at) (samples) are
// left as-is for samples and overwritten for drives/charges — this
// matches the importer's own behavior, so re-running is idempotent.
//
// The request body is the raw JSON file from /data/backup. Capped
// at 100 MiB; a year of 60 s polls is ~100 MB in the current shape,
// so this is the realistic ceiling.
func handleDataRestore(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ds := d.Drives.For(uid)
		cs := d.Charges.For(uid)
		ss := d.Samples.For(uid)
		if ds == nil || cs == nil || ss == nil {
			http.Error(w, "restore unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
		var bundle struct {
			Version   string           `json:"version"`
			CreatedAt string           `json:"created_at"`
			Drives    []drives.Drive   `json:"drives"`
			Charges   []charges.Charge `json:"charges"`
			Samples   []samples.Sample `json:"samples"`
		}
		if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
			http.Error(w, "parse backup: "+err.Error(), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		for _, drv := range bundle.Drives {
			if err := ds.Upsert(ctx, drv); err != nil {
				http.Error(w, "upsert drive "+drv.ID+": "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		for _, chg := range bundle.Charges {
			if err := cs.Upsert(ctx, chg); err != nil {
				http.Error(w, "upsert charge "+chg.ID+": "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if err := ss.InsertBatch(ctx, bundle.Samples); err != nil {
			http.Error(w, "insert samples: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"drives":  len(bundle.Drives),
			"charges": len(bundle.Charges),
			"samples": len(bundle.Samples),
		})
	}
}

// handleDataReset truncates the three session tables for the current
// user (drives, charges, vehicle_state). Vehicles, user_settings,
// push_subscriptions, and the user row are preserved so settings
// and the Rivian account link survive. Returns deleted row counts.
//
// This is the UI counterpart to what used to be a psql TRUNCATE.
// Pair with /data/backup to avoid losing work.
func handleDataReset(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ds := d.Drives.For(uid)
		cs := d.Charges.For(uid)
		ss := d.Samples.For(uid)
		if ds == nil || cs == nil || ss == nil {
			http.Error(w, "reset unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		ctx := r.Context()
		// Wipe in an order that can't violate FKs; there are no
		// cross-table FKs on user_id so order is cosmetic.
		samplesN, err := ss.Reset(ctx)
		if err != nil {
			http.Error(w, "reset samples: "+err.Error(), http.StatusInternalServerError)
			return
		}
		drivesN, err := ds.Reset(ctx)
		if err != nil {
			http.Error(w, "reset drives: "+err.Error(), http.StatusInternalServerError)
			return
		}
		chargesN, err := cs.Reset(ctx)
		if err != nil {
			http.Error(w, "reset charges: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"drives":  drivesN,
			"charges": chargesN,
			"samples": samplesN,
		})
	}
}

// handleDataAccountDelete is the self-service counterpart to the
// admin user-delete endpoint: a signed-in user can purge their own
// account end-to-end. Mirrors the admin path's safety rails:
//
//   - "last admin" guard so the install never ends up with zero
//     admins by self-service (the user has to ask another admin to
//     promote a successor first).
//   - resolve the IdP username BEFORE the cascade delete because
//     the rivolt users row is gone afterward.
//
// Cascade order: rivolt DB delete (cascades through every FK'd
// per-user table — drives, charges, samples, settings,
// user_secrets, push_subscriptions, sessions, ...), then Kratos /
// IdP delete, then clear the auth cookie. The next request from
// the same browser is unauthenticated.
func handleDataAccountDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		// Last-admin guard. An admin can self-delete only if there's
		// at least one other admin — otherwise the install ends up
		// with no admin and recovery requires direct DB access.
		role, rerr := db.RoleFor(r.Context(), d.DB, uid)
		if rerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": rerr.Error()})
			return
		}
		if role == "admin" {
			n, cerr := db.CountAdmins(r.Context(), d.DB)
			if cerr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": cerr.Error()})
				return
			}
			if n <= 1 {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "cannot delete the last admin — promote another user first",
				})
				return
			}
		}
		// Resolve username for IdP cleanup before cascade delete.
		var username string
		if d.Users != nil && d.Users.Enabled() {
			u, uerr := db.RawUsernameByID(r.Context(), d.DB, uid)
			if uerr != nil && d.Logger != nil {
				d.Logger.Warn("self-delete: lookup username for idp delete failed",
					"id", uid.String(), "err", uerr.Error())
			}
			username = u
		}
		if err := db.DeleteUser(r.Context(), d.DB, uid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if d.Users != nil && d.Users.Enabled() && username != "" {
			if err := d.Users.DeleteUser(r.Context(), username); err != nil && d.Logger != nil {
				d.Logger.Warn("self-delete: idp delete failed (rivolt row already removed)",
					"username", username, "err", err.Error())
			}
		}
		// Clear the session cookie so the next request from this
		// browser is unauthenticated. The session row was already
		// cascaded out of `sessions` by DeleteUser; this just stops
		// the now-orphan cookie from showing up in the next request.
		if d.Auth != nil {
			d.Auth.Logout(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true})
	}
}

// --- Charge clustering ----------------------------------------------------
//
// Pure-local classification of charge sessions into Home / Public / Fast.
// Fast is anything peaking >=50 kW (DCFC) regardless of location; the
// rest is DBSCAN-clustered on (lat, lon) with the biggest cluster
// winning Home. The UI uses this for /charges badges and the Overview
// Charging locations card. No external calls.

type chargeClusterResponse struct {
	Label       string   `json:"label"`
	Lat         float64  `json:"lat"`
	Lon         float64  `json:"lon"`
	Sessions    int      `json:"sessions"`
	EnergyKWh   float64  `json:"energy_kwh"`
	RadiusMeter float64  `json:"radius_m"`
	MemberIDs   []string `json:"member_ids"`
}

func handleChargeClusters(store *charges.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []chargeClusterResponse{})
			return
		}
		// Pull the full usable window — clustering is cheap and the
		// store caps list size anyway. A bigger corpus just gives
		// better Home detection.
		rows, err := store.ListRecent(r.Context(), 5000)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pts := make([]analytics.ChargePoint, 0, len(rows))
		for _, c := range rows {
			pts = append(pts, analytics.ChargePoint{
				ID:             c.ID,
				Lat:            c.Lat,
				Lon:            c.Lon,
				EnergyAddedKWh: c.EnergyAddedKWh,
				// Peak kW drives the Home/Public/Fast split: anything
				// >=50 kW is DCFC regardless of location. Zero means
				// unknown peak and falls through to location clustering.
				MaxPowerKW: c.MaxPowerKW,
			})
		}
		clusters := analytics.ClusterCharges(pts, analytics.DefaultParams())
		out := make([]chargeClusterResponse, 0, len(clusters))
		for _, c := range clusters {
			out = append(out, chargeClusterResponse{
				Label:       string(c.Label),
				Lat:         c.Centroid.Lat,
				Lon:         c.Centroid.Lon,
				Sessions:    c.Sessions,
				EnergyKWh:   c.EnergyKWh,
				RadiusMeter: c.RadiusMeter,
				MemberIDs:   c.MemberIDs,
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// --- AI settings ----------------------------------------------------------
//
// Thin wrappers around settings.Manager so the Settings UI can configure
// which LLM provider Rivolt uses for AI features (weekly digest, trip
// planner, anomaly explanations). The manager enforces the redaction
// contract: API keys are reported as has_key=true/false, never echoed back.

// handleAISettingsGet returns the redacted AI configuration.
func handleAISettingsGet(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, mgr.Public())
	}
}

// handleAISettingsPut accepts a partial patch: nil fields are untouched,
// empty-string fields clear, non-empty values update.
func handleAISettingsPut(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		var patch settings.AIUpdate
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
			return
		}
		pub, err := mgr.Update(r.Context(), patch)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, pub)
	}
}

// handleRecapSettingsGet returns the current recap configuration.
func handleRecapSettingsGet(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, mgr.RecapPublic())
	}
}

// handleRecapSettingsPut accepts a partial patch for recap settings.
func handleRecapSettingsPut(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		var patch settings.RecapUpdate
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
			return
		}
		pub, err := mgr.UpdateRecap(r.Context(), patch)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, pub)
	}
}

// handleGPSSettingsGet returns the GPS accuracy thresholds.
func handleGPSSettingsGet(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, mgr.GPSPublic())
	}
}

// handleGPSSettingsPut accepts a partial patch for GPS thresholds.
func handleGPSSettingsPut(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		var patch settings.GPSUpdate
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
			return
		}
		pub, err := mgr.UpdateGPS(r.Context(), patch)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, pub)
	}
}

// handleAIModelsList proxies the provider's catalogue endpoint using the
// stored API key so the UI can offer a live dropdown instead of asking
// users to remember model IDs that drift across releases.
func handleAIModelsList(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		provider := chi.URLParam(r, "provider")
		// Independent timeout: the provider list endpoints are small but
		// we don't want to inherit a stalled request's context.
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		models, err := mgr.ListModels(ctx, provider)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	}
}

// handleAIPing sends a trivial prompt to the currently configured
// provider and returns the reply along with token usage and
// round-trip latency. Two goals:
//   - Let the Settings UI confirm the provider/key/model triple is
//     valid without waiting for a downstream feature to exercise it.
//   - Surface real error messages from the provider verbatim (wrong
//     key, expired credit, model not available on account, etc.)
//     so the operator can self-diagnose.
//
// The prompt is intentionally minimal — we bill the user's account
// for each ping, so we want to spend the fewest possible tokens.
// Replies cap at ~20 tokens in practice because we ask for one
// short sentence; we still log input/output token counts for
// transparency.
func handleAIPing(mgr *settings.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings manager unavailable"})
			return
		}
		analyzer := mgr.Analyzer()
		if analyzer == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "no AI provider configured — add an API key in Settings → AI providers",
			})
			return
		}
		// Hard cap the outbound call at 20s. Provider completion APIs
		// typically respond in 1-3s for a 20-token answer; anything
		// beyond that points to an outage or a wedged model, and we
		// don't want the button spinning forever.
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		const system = "You are a connectivity smoke test. Answer in one short sentence only."
		const user = "Reply with a single sentence confirming that this integration works."
		start := time.Now()
		reply, usage, err := analyzer.Complete(ctx, system, user)
		latency := time.Since(start)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error":      err.Error(),
				"model":      analyzer.ModelName(),
				"latency_ms": latency.Milliseconds(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"reply":         strings.TrimSpace(reply),
			"model":         analyzer.ModelName(),
			"latency_ms":    latency.Milliseconds(),
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
		})
	}
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

// driveWeatherResponse mirrors the persisted drive_weather row in the
// units the SPA renders. We keep this DTO instead of leaking
// internal/weather.Snapshot directly so the SPA contract stays stable
// even if we swap the upstream provider.
type driveWeatherResponse struct {
	TempF       *float64 `json:"temp_f,omitempty"`
	ApparentF   *float64 `json:"feels_like_f,omitempty"`
	WindMPH     *float64 `json:"wind_mph,omitempty"`
	WindFromDeg *float64 `json:"wind_from_deg,omitempty"`
	// HeadwindMPH is signed: positive = headwind, negative =
	// tailwind. The SPA does its own pretty-print.
	HeadwindMPH *float64 `json:"headwind_mph,omitempty"`
	PrecipIn    *float64 `json:"precip_in,omitempty"`
	HumidityPct *float64 `json:"humidity_pct,omitempty"`
	Conditions  string   `json:"conditions,omitempty"`
}

// driveWeatherBackfillResponse is the JSON the SPA polls. Counts are
// cumulative for the single call; remaining is recomputed at the end
// so the client can stop polling when it hits zero.
type driveWeatherBackfillResponse struct {
	Disabled  bool `json:"disabled"`
	Processed int  `json:"processed"`
	Succeeded int  `json:"succeeded"`
	Failed    int  `json:"failed"`
	Remaining int  `json:"remaining"`
}

// handleDriveWeatherBackfill enriches historical drives with weather
// snapshots. Each call processes at most weatherBackfillBatch drives
// that don't yet have a cache row, so a slow upstream can't lock up
// a worker; the SPA polls until remaining == 0.
//
// Gated on RecapWeatherEnabled — backfill is the same data egress
// the per-recap fetch performs, just amortised across the archive.
// If the pref is off we return 200 with disabled=true so the UI can
// short-circuit instead of guessing from a 4xx.
func handleDriveWeatherBackfill(d Deps, uid uuid.UUID) http.HandlerFunc {
	// Bounded so one click can't spin a worker for minutes. Open-Meteo
	// is fast (~150ms) but we still want a hard ceiling per request.
	const weatherBackfillBatch = 25
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		if d.SettingsMgr == nil {
			http.Error(w, "settings unavailable", http.StatusServiceUnavailable)
			return
		}
		if !d.SettingsMgr.RecapWeatherEnabled() {
			writeJSON(w, http.StatusOK, driveWeatherBackfillResponse{Disabled: true})
			return
		}
		drivesStore := d.Drives.For(uid)
		if drivesStore == nil {
			http.Error(w, "user stores unavailable", http.StatusServiceUnavailable)
			return
		}
		ds, err := drivesStore.ListAll(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Walk newest-first so a user kicking off backfill on a fresh
		// install sees their recent drives enriched first; ListAll is
		// already sorted by start time descending.
		cache := weather.NewCache(d.DB)
		resp := driveWeatherBackfillResponse{}
		for i := range ds {
			drv := &ds[i]
			// Skip drives without a usable start fix — we can't ask
			// Open-Meteo "what was the weather at (0,0)" usefully,
			// and these rows would otherwise stay "remaining" forever.
			if drv.StartLat == 0 && drv.StartLon == 0 {
				continue
			}
			// "Done" means the time-series rows are populated.
			// Checking the snapshot alone would skip drives that
			// were enriched before the series feature shipped, so
			// users who already ran backfill once would never get
			// their graphs filled in.
			if existing, _ := cache.GetSeries(r.Context(), uid, drv.ID); len(existing) > 0 {
				continue
			}
			if resp.Processed >= weatherBackfillBatch {
				resp.Remaining++
				continue
			}
			resp.Processed++
			if _, err := fetchAndCacheDriveWeather(r.Context(), cache, uid, drv); err != nil {
				resp.Failed++
				if d.Logger != nil {
					d.Logger.Warn("weather backfill fetch failed", "err", err.Error(), "drive_id", drv.ID)
				}
				continue
			}
			resp.Succeeded++
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// fetchAndCacheDriveWeather populates both the start-hour snapshot
// (drive_weather, used by the recap prompt and the start-strip)
// and the per-cadence time series (drive_weather_series, used by
// the drive-detail weather panel). Returns the snapshot so the
// recap path can render it inline; returns (nil, nil) when the
// drive has no usable start fix.
//
// Each upstream call is given its own bounded timeout so a slow
// provider can't lock up the request. A series fetch failure
// after a successful snapshot fetch is logged at the call site
// but does not roll back the snapshot -- the recap can still
// render with start-hour data while the chart stays empty.
func fetchAndCacheDriveWeather(ctx context.Context, cache *weather.Cache, uid uuid.UUID, drv *drives.Drive) (*weather.Snapshot, error) {
	if drv == nil {
		return nil, nil
	}
	return weather.FetchAndCache(
		ctx, cache, uid, drv.ID,
		drv.StartedAt, drv.EndedAt,
		drv.StartLat, drv.StartLon,
		drv.EndLat, drv.EndLon,
	)
}

// handleDriveWeatherGet returns the persisted weather snapshot for
// (uid, driveID), or 404 when no row exists. Lightweight read off
// the drive_weather cache; never calls Open-Meteo. Independent of
// the recap path so the detail-page chart can render even when no
// AI recap was generated for this drive.
func handleDriveWeatherGet(pool *sql.DB, uid uuid.UUID) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		driveID := chi.URLParam(r, "id")
		if driveID == "" {
			http.Error(w, "missing drive id", http.StatusBadRequest)
			return
		}
		snap, err := loadDriveWeather(r.Context(), pool, uid, driveID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if snap == nil {
			http.Error(w, "no weather cached for this drive", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, snap)
	}
}

// driveWeatherSamplePoint is one entry in the time-series response.
// Matches the SPA-facing units of driveWeatherResponse so the chart
// renderer doesn't carry conversion logic.
type driveWeatherSamplePoint struct {
	At             time.Time `json:"at"`
	CadenceMinutes int       `json:"cadence_minutes"`
	TempF          *float64  `json:"temp_f,omitempty"`
	ApparentF      *float64  `json:"feels_like_f,omitempty"`
	WindMPH        *float64  `json:"wind_mph,omitempty"`
	WindFromDeg    *float64  `json:"wind_from_deg,omitempty"`
	HeadwindMPH    *float64  `json:"headwind_mph,omitempty"`
	PrecipIn       *float64  `json:"precip_in,omitempty"`
	HumidityPct    *float64  `json:"humidity_pct,omitempty"`
	Conditions     string    `json:"conditions,omitempty"`
}

type driveWeatherSeriesResponse struct {
	Points []driveWeatherSamplePoint `json:"points"`
}

// handleDriveWeatherSeriesGet returns the cached time series for
// (uid, driveID). Returns 200 with an empty `points` array (not 404)
// when no rows exist so the SPA can render a "no chart data" affordance
// instead of treating the missing series as an error -- the start-hour
// snapshot endpoint already handles the not-found case.
func handleDriveWeatherSeriesGet(pool *sql.DB, uid uuid.UUID) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		driveID := chi.URLParam(r, "id")
		if driveID == "" {
			http.Error(w, "missing drive id", http.StatusBadRequest)
			return
		}
		cache := weather.NewCache(pool)
		rows, err := cache.GetSeries(r.Context(), uid, driveID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := driveWeatherSeriesResponse{Points: make([]driveWeatherSamplePoint, 0, len(rows))}
		for _, row := range rows {
			p := driveWeatherSamplePoint{At: row.SampledAt, CadenceMinutes: row.CadenceMinutes}
			if row.HasTemp {
				v := row.TempC*1.8 + 32
				p.TempF = &v
			}
			if row.HasApparent {
				v := row.ApparentTempC*1.8 + 32
				p.ApparentF = &v
			}
			if row.HasWind {
				v := row.WindKPH * 0.621371
				p.WindMPH = &v
				wd := row.WindDirDeg
				p.WindFromDeg = &wd
			}
			if row.HasHeadwind {
				v := row.HeadwindKPH * 0.621371
				p.HeadwindMPH = &v
			}
			if row.HasPrecip {
				v := row.PrecipMM * 0.0393701
				p.PrecipIn = &v
			}
			if row.HasHumidity {
				v := row.HumidityPct
				p.HumidityPct = &v
			}
			if row.HasConditions {
				p.Conditions = row.Conditions
			}
			out.Points = append(out.Points, p)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// loadDriveWeather returns the persisted weather snapshot for
// (uid, driveID) in the SPA's units (F, mph, in). Returns (nil, nil)
// when no row exists.
func loadDriveWeather(ctx context.Context, pool *sql.DB, uid uuid.UUID, driveID string) (*driveWeatherResponse, error) {
	if pool == nil {
		return nil, nil
	}
	var (
		tC, atC, wKPH, wDir, hwKPH, pMM, hPct sql.NullFloat64
		cond                                  sql.NullString
	)
	err := pool.QueryRowContext(ctx, `
SELECT temp_c, apparent_temp_c, wind_kph, wind_dir_deg, headwind_kph,
       precip_mm, humidity_pct, conditions
FROM drive_weather
WHERE user_id = $1 AND drive_id = $2
`, uid, driveID).Scan(&tC, &atC, &wKPH, &wDir, &hwKPH, &pMM, &hPct, &cond)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &driveWeatherResponse{}
	if tC.Valid {
		v := tC.Float64*1.8 + 32
		out.TempF = &v
	}
	if atC.Valid {
		v := atC.Float64*1.8 + 32
		out.ApparentF = &v
	}
	if wKPH.Valid {
		v := wKPH.Float64 * 0.621371
		out.WindMPH = &v
	}
	if wDir.Valid {
		v := wDir.Float64
		out.WindFromDeg = &v
	}
	if hwKPH.Valid {
		v := hwKPH.Float64 * 0.621371
		out.HeadwindMPH = &v
	}
	if pMM.Valid {
		v := pMM.Float64 * 0.0393701
		out.PrecipIn = &v
	}
	if hPct.Valid {
		v := hPct.Float64
		out.HumidityPct = &v
	}
	if cond.Valid {
		out.Conditions = cond.String
	}
	return out, nil
}

// snapshotToResponseWeather converts the metric-base snapshot to the
// imperial DTO the SPA expects. The cache always stores metric so the
// conversion lives at the API boundary.
func snapshotToResponseWeather(s *weather.Snapshot) *driveWeatherResponse {
	if s == nil {
		return nil
	}
	out := &driveWeatherResponse{Conditions: s.Conditions}
	if s.HasTemp {
		v := s.TempC*1.8 + 32
		out.TempF = &v
	}
	if s.HasApparent {
		v := s.ApparentTempC*1.8 + 32
		out.ApparentF = &v
	}
	if s.HasWind {
		v := s.WindKPH * 0.621371
		out.WindMPH = &v
		dir := s.WindDirDeg
		out.WindFromDeg = &dir
	}
	if s.HasHeadwind {
		v := s.HeadwindKPH * 0.621371
		out.HeadwindMPH = &v
	}
	if s.HasPrecip {
		v := s.PrecipMM * 0.0393701
		out.PrecipIn = &v
	}
	if s.HasHumidity {
		v := s.HumidityPct
		out.HumidityPct = &v
	}
	return out
}

// snapshotToRecapWeather lifts an internal/weather.Snapshot into the
// recap.Weather DTO the prompt builder consumes. Both shapes have the
// same field names; the conversion is mechanical.
func snapshotToRecapWeather(s *weather.Snapshot) *recap.Weather {
	if s == nil {
		return nil
	}
	return &recap.Weather{
		TempC:         s.TempC,
		ApparentTempC: s.ApparentTempC,
		WindKPH:       s.WindKPH,
		WindDirDeg:    s.WindDirDeg,
		HeadwindKPH:   s.HeadwindKPH,
		PrecipMM:      s.PrecipMM,
		HumidityPct:   s.HumidityPct,
		Conditions:    s.Conditions,
		HasTemp:       s.HasTemp,
		HasApparent:   s.HasApparent,
		HasWind:       s.HasWind,
		HasHeadwind:   s.HasHeadwind,
		HasPrecip:     s.HasPrecip,
		HasHumidity:   s.HasHumidity,
		HasConditions: s.HasConditions,
	}
}

// driveEfficiencyResponse is the JSON shape returned by POST
// /api/drives/{id}/efficiency.
type driveEfficiencyResponse struct {
	Analysis       string                   `json:"analysis"`
	Factors        []recap.EfficiencyFactor `json:"factors,omitempty"`
	Recommendation string                   `json:"recommendation,omitempty"`
	Forecast       string                   `json:"forecast,omitempty"`
	Summary        string                   `json:"summary,omitempty"`
	Model          string                   `json:"model"`
	GeneratedAt    time.Time                `json:"generated_at"`
	InputTokens    int64                    `json:"input_tokens,omitempty"`
	OutputTokens   int64                    `json:"output_tokens,omitempty"`
}

// handleDriveEfficiencyPost generates an AI-driven efficiency analysis
// on demand and persists it to drive_efficiency so subsequent loads of
// the drive page hit the cache instead of re-billing the LLM key.
// Each call to this endpoint *does* re-bill (the SPA only fires it
// from an explicit Analyze / Regenerate button); the GET counterpart
// is what fetches the stored copy on page mount.
//
// The optional JSON body carries per-trip transient context (extra
// load, temperature unit) the SPA captures via the form on the
// analysis card; persisted per-vehicle settings (tire type, wheel
// size, accessories) are pulled from vehicles.metadata regardless of
// body shape. Towing is auto-detected from the persisted driveMode
// samples (Rivian 'tow' / 'towing' mode).
func handleDriveEfficiencyPost(d Deps, uid uuid.UUID) http.HandlerFunc {
	type efficiencyReq struct {
		ExtraLoadLb float64 `json:"extra_load_lb,omitempty"`
		// "f" or "c". Empty / unknown values fall back to F so a
		// legacy SPA without the field gets the historical behavior.
		// The backend can't read the SPA's preferences store
		// directly (it's localStorage, per-browser), so the SPA
		// echoes the user's pick on every request.
		TemperatureUnit string `json:"temperature_unit,omitempty"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		if d.SettingsMgr == nil {
			http.Error(w, "ai settings unavailable", http.StatusServiceUnavailable)
			return
		}
		analyzer := d.SettingsMgr.Analyzer()
		if analyzer == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "no AI provider configured -- add an API key in Settings -> AI providers",
			})
			return
		}
		driveID := chi.URLParam(r, "id")
		if driveID == "" {
			http.Error(w, "missing drive id", http.StatusBadRequest)
			return
		}

		drivesStore := d.Drives.For(uid)
		samplesStore := d.Samples.For(uid)
		if drivesStore == nil || samplesStore == nil {
			http.Error(w, "user stores unavailable", http.StatusServiceUnavailable)
			return
		}

		// Locate the (collapsed) drive.
		ds, err := drivesStore.ListAll(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		const (
			roundTripRadiusM = 200.0
			roundTripMaxGap  = 90 * time.Minute
		)
		ds = drives.CollapseRoundTrips(ds, roundTripRadiusM, roundTripMaxGap)
		var drv *drives.Drive
		for i := range ds {
			if ds[i].ID == driveID {
				drv = &ds[i]
				break
			}
		}
		if drv == nil {
			http.Error(w, "drive not found", http.StatusNotFound)
			return
		}

		// Sample window: trip +/- 3 min pad.
		since := drv.StartedAt.Add(-3 * time.Minute)
		end := drv.EndedAt.Add(3 * time.Minute)
		allSamples, err := samplesStore.ListSince(r.Context(), since, 100_000)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		windowed := make([]samples.Sample, 0, len(allSamples))
		for _, s := range allSamples {
			if s.At.Before(since) || s.At.After(end) {
				continue
			}
			windowed = append(windowed, s)
		}

		// Optional weather (same gate as the recap path used).
		var effWeather *recap.Weather
		if d.SettingsMgr.RecapWeatherEnabled() && drv.StartLat != 0 && drv.StartLon != 0 {
			cache := weather.NewCache(d.DB)
			snap, _ := cache.Get(r.Context(), uid, drv.ID)
			if snap == nil {
				if fetched, ferr := fetchAndCacheDriveWeather(r.Context(), cache, uid, drv); ferr == nil && fetched != nil {
					snap = fetched
				}
			}
			if snap != nil {
				effWeather = snapshotToRecapWeather(snap)
			}
		}

		// Baseline efficiency from the user's recent drives (last
		// 90 days, drives ending before this trip's start). Direct
		// computation off drives.ListAll keeps this independent of
		// the analytics package.
		baseMiPerKWh, baseDays := 0.0, 90
		{
			cutoff := drv.StartedAt.Add(-90 * 24 * time.Hour)
			var miles, energy float64
			for _, x := range ds {
				if x.ID == drv.ID {
					continue
				}
				if !x.EndedAt.Before(drv.StartedAt) {
					continue
				}
				if x.EndedAt.Before(cutoff) {
					continue
				}
				if x.EnergyUsedKWh <= 0 || x.DistanceMi <= 0 {
					continue
				}
				miles += x.DistanceMi
				energy += x.EnergyUsedKWh
			}
			if energy > 0 {
				baseMiPerKWh = miles / energy
			}
		}

		// Slightly less than the surrounding chi middleware.Timeout
		// so the LLM call observes ctx.Done first and returns a
		// real error message — chi's wrapper would otherwise win
		// the race and write 504 with no body. See the AI-bound
		// group registration above for the chi side of the budget.
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute+30*time.Second)
		defer cancel()

		// Per-trip transient context from the request body. Tolerate
		// an empty body (curl, legacy SPA).
		var req efficiencyReq
		if r.ContentLength > 0 {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}

		// Per-vehicle profile from vehicles.metadata. Best-effort:
		// if the resolve or read fails, the prompt just omits the
		// profile block — the analysis still runs.
		var profile *recap.VehicleProfile
		if drv.VehicleID != "" {
			resolver := db.NewVehicleResolver(d.DB, uid)
			if vid, err := resolver.Resolve(ctx, drv.VehicleID); err == nil {
				if dbProfile, err := db.GetVehicleProfile(ctx, d.DB, uid, vid); err == nil {
					profile = &recap.VehicleProfile{
						TireType:           dbProfile.TireType,
						WheelInches:        dbProfile.WheelInches,
						Accessories:        dbProfile.Accessories,
						DefaultExtraLoadLb: dbProfile.DefaultExtraLoadLb,
						FrequentlyTows:     dbProfile.FrequentlyTows,
						TirePlacardPSI:     dbProfile.TirePlacardPSI,
					}
				}
			}
		}

		// Default to F to preserve historical behavior when the SPA
		// doesn't send temperature_unit. Anything other than the
		// explicit "c" maps to F so a typo can't silently flip a
		// user's display.
		useF := !strings.EqualFold(req.TemperatureUnit, "c")

		res, err := recap.GenerateEfficiency(ctx, analyzer, recap.EfficiencyInputs{
			Drive:            *drv,
			Samples:          windowed,
			UseFahrenheit:    useF,
			BaselineMiPerKWh: baseMiPerKWh,
			BaselineDays:     baseDays,
			Weather:          effWeather,
			VehicleProfile:   profile,
			ExtraLoadLb:      req.ExtraLoadLb,
			Towing:           detectTowingFromSamples(windowed),
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": err.Error(),
				"model": analyzer.ModelName(),
			})
			return
		}

		now := time.Now()
		response := driveEfficiencyResponse{
			Analysis:       res.Analysis,
			Factors:        effFactorsOf(res.Parsed),
			Recommendation: effRecommendationOf(res.Parsed),
			Forecast:       effForecastOf(res.Parsed),
			Summary:        effSummaryOf(res.Parsed),
			Model:          res.Model,
			GeneratedAt:    now,
			InputTokens:    res.InputTokens,
			OutputTokens:   res.OutputTokens,
		}

		// Persist to drive_efficiency so the next page load hits the
		// cache. Best-effort: if the upsert fails (DB hiccup, RLS
		// flip, FK violation from a deleted user) we still return
		// the freshly-computed analysis so the user sees something.
		if err := saveDriveEfficiency(r.Context(), d.DB, uid, drv.ID, response); err != nil {
			slog.WarnContext(r.Context(), "drive_efficiency: save failed",
				"err", err, "drive_id", drv.ID)
		}

		writeJSON(w, http.StatusOK, response)
	}
}

// handleDriveEfficiencyGet returns the stored efficiency analysis for
// a drive, or 404 when none has been generated yet. The SPA fetches
// this on page mount so a previously-analyzed drive shows the result
// immediately instead of an empty form. Generating a fresh analysis
// is the POST counterpart.
func handleDriveEfficiencyGet(d Deps, uid uuid.UUID) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		driveID := chi.URLParam(r, "id")
		if driveID == "" {
			http.Error(w, "missing drive id", http.StatusBadRequest)
			return
		}
		row, err := loadDriveEfficiency(r.Context(), d.DB, uid, driveID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			http.Error(w, "no analysis", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *row)
	}
}

// saveDriveEfficiency upserts a finished analysis. The Analysis text
// is stored separately from the structured JSON so log forwarders /
// debugging tooling can read it without parsing JSONB.
func saveDriveEfficiency(
	ctx context.Context,
	db *sql.DB,
	uid uuid.UUID,
	driveID string,
	resp driveEfficiencyResponse,
) error {
	if db == nil {
		return fmt.Errorf("nil db")
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO drive_efficiency (
			user_id, drive_id, model, analysis_text, result_json,
			input_tokens, output_tokens, generated_at
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		ON CONFLICT (user_id, drive_id) DO UPDATE SET
			model         = EXCLUDED.model,
			analysis_text = EXCLUDED.analysis_text,
			result_json   = EXCLUDED.result_json,
			input_tokens  = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			generated_at  = EXCLUDED.generated_at
	`,
		uid, driveID, resp.Model, resp.Analysis, body,
		resp.InputTokens, resp.OutputTokens, resp.GeneratedAt,
	)
	return err
}

// loadDriveEfficiency returns the stored response for a drive, or nil
// when no row exists. The full driveEfficiencyResponse round-trips
// through result_json, so callers get the same JSON shape POST
// returned originally.
func loadDriveEfficiency(
	ctx context.Context,
	db *sql.DB,
	uid uuid.UUID,
	driveID string,
) (*driveEfficiencyResponse, error) {
	if db == nil {
		return nil, fmt.Errorf("nil db")
	}
	var body []byte
	err := db.QueryRowContext(ctx, `
		SELECT result_json FROM drive_efficiency
		WHERE user_id = $1 AND drive_id = $2
	`, uid, driveID).Scan(&body)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var resp driveEfficiencyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &resp, nil
}

// detectTowingFromSamples returns true when any persisted sample
// reports Rivian's tow drive mode. Cheap O(n) scan; matches case-
// insensitively against any mode containing "tow" so we catch
// "tow", "towing", and any future Rivian-side renames.
func detectTowingFromSamples(ss []samples.Sample) bool {
	for _, s := range ss {
		if s.DriveMode == "" {
			continue
		}
		if strings.Contains(strings.ToLower(s.DriveMode), "tow") {
			return true
		}
	}
	return false
}

// Nil-safe accessors for *recap.EfficiencyParsed.
func effFactorsOf(p *recap.EfficiencyParsed) []recap.EfficiencyFactor {
	if p == nil {
		return nil
	}
	return p.Factors
}
func effRecommendationOf(p *recap.EfficiencyParsed) string {
	if p == nil {
		return ""
	}
	return p.Recommendation
}
func effForecastOf(p *recap.EfficiencyParsed) string {
	if p == nil {
		return ""
	}
	return p.Forecast
}
func effSummaryOf(p *recap.EfficiencyParsed) string {
	if p == nil {
		return ""
	}
	return p.Summary
}
