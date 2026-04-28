// Package api wires the HTTP surface for Rivolt. It assembles routes,
// middleware, and handler dependencies into a single chi Mux.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	"github.com/apohor/rivolt/internal/analytics"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/electrafi"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/settings"
)

// Deps is the bag of dependencies the API router needs. Keep this
// small; avoid accumulating a "dependency soup" pattern.
type Deps struct {
	Rivian rivian.Client
	// RivianAccount drives the /api/settings/rivian sign-in surface.
	// Both *rivian.LiveClient and *rivian.MockClient satisfy it, so
	// the UI sign-in flow works identically under RIVIAN_CLIENT=live
	// and RIVIAN_CLIENT=mock. nil when the stub client is in use
	// (nothing to sign into).
	RivianAccount rivian.Account
	SettingsStore *settings.Store
	PushService   *push.Service
	PushStore     *push.Store
	SettingsMgr   *settings.Manager
	Drives        *drives.Store
	Charges       *charges.Store
	Samples       *samples.Store
	// StateMonitor, when set, backs /api/state/:id with a cached
	// snapshot kept fresh by a websocket subscription. When nil the
	// handler falls back to one-shot REST polls against Rivian's
	// GetVehicleState query.
	StateMonitor *rivian.StateMonitor
	// Auth, when non-nil and Configured(), gates /api/* behind a
	// login cookie or trusted proxy header. When nil or
	// unconfigured (no RIVOLT_USERNAME / RIVOLT_PASSWORD) the API
	// is open, preserving the pre-auth single-tenant UX so
	// upgrades don't lock users out of their own NAS.
	Auth *auth.Service
	// OIDC, when non-nil, mounts /api/auth/oidc/* — the third
	// auth issuer alongside static creds and trusted-proxy
	// header. nil disables the social-login button row in the
	// SPA but doesn't affect any other code path.
	OIDC    *oidc.Service
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
}

// New builds the root mux with all routes mounted.
func New(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
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

	r.Route("/api", func(r chi.Router) {
		// Health + auth endpoints stay reachable without a session,
		// otherwise the browser has no way to log in.
		r.Get("/health", handleHealth(d.Version))
		if d.Auth != nil {
			r.Route("/auth", func(r chi.Router) {
				r.Post("/login", d.Auth.Login)
				r.Post("/logout", d.Auth.Logout)
				r.Get("/me", d.Auth.Me)
				if d.OIDC != nil {
					d.OIDC.Mount(r)
				}
			})
		}

		// Everything else sits behind requireUser when auth is
		// configured. The bool guard means `docker run` with no env
		// vars still works exactly like v0.3.x — login is opt-in via
		// setting RIVOLT_USERNAME / RIVOLT_PASSWORD.
		r.Group(func(r chi.Router) {
			if d.Auth != nil && d.Auth.Configured() {
				r.Use(requireUserMW)
			}
			// 30s is plenty for regular JSON endpoints; it keeps
			// stuck Rivian calls from pinning a connection. Bulk
			// data routes (import / backup / restore) live in a
			// second group below without this timeout — large CSV
			// exports can take minutes.
			r.Use(middleware.Timeout(30 * time.Second))

			r.Route("/push", func(r chi.Router) {
				r.Get("/vapid-key", handlePushVAPIDKey(d.PushService))
				r.Post("/subscribe", handlePushSubscribe(d.PushStore))
				r.Post("/unsubscribe", handlePushUnsubscribe(d.PushStore))
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
			r.Get("/vehicles", handleVehicles(d.Rivian, d.StateMonitor))
			r.With(vehicleScoped).Get("/state/{vehicleID}", handleVehicleState(d.Rivian, d.StateMonitor))
			r.With(vehicleScoped).Get("/state/{vehicleID}/debug", handleVehicleStateDebug(d.Rivian))
			r.With(vehicleScoped).Get("/state/{vehicleID}/fresh", handleVehicleStateFresh(d.Rivian))
			r.With(vehicleScoped).Get("/live-session/{vehicleID}", handleLiveSession(d.Rivian, d.StateMonitor, d.SettingsStore))
			r.With(vehicleScoped).Get("/live-drive/{vehicleID}", handleLiveDrive(d.StateMonitor))
			r.Get("/charging-schema", handleChargingSchemaProbe(d.Rivian))
			r.Get("/charging-field/{field}", handleChargingFieldProbe(d.Rivian))
			r.Get("/charging-frames", handleChargingFrames(d.Rivian))

			// Rivian account management. Only wired when a live client is
			// present; with the stub/mock these return 404.
			r.Route("/settings/rivian", func(r chi.Router) {
				r.Get("/", handleRivianStatus(d.RivianAccount))
				r.Post("/login", handleRivianLogin(d.RivianAccount, d.Secrets))
				r.Post("/mfa", handleRivianMFA(d.RivianAccount, d.Secrets))
				r.Post("/logout", handleRivianLogout(d.RivianAccount, d.Secrets))
			})

			// Home electricity cost settings, applied locally to estimate
			// the price of sessions Rivian reports as free (home AC, L2,
			// non-RAN public chargers).
			r.Route("/settings/charging", func(r chi.Router) {
				r.Get("/", handleChargingSettingsGet(d.SettingsStore))
				r.Put("/", handleChargingSettingsPut(d.SettingsStore))
				// Price book for fast/public charging networks: a flat
				// list of {name, price_per_kwh, currency} rows the UI
				// uses to one-click prefill the PricingCard.
				r.Get("/networks", handleChargingNetworksGet(d.SettingsStore))
				r.Put("/networks", handleChargingNetworksPut(d.SettingsStore))
			})

			// AI provider configuration (OpenAI / Anthropic / Gemini).
			// GET returns the redacted public view (api keys reported as
			// has_key only); PUT accepts a partial patch. The /models
			// endpoint proxies the provider's catalogue API so the UI can
			// populate a dropdown instead of asking users to memorise IDs.
			r.Get("/settings/ai", handleAISettingsGet(d.SettingsMgr))
			r.Put("/settings/ai", handleAISettingsPut(d.SettingsMgr))
			r.Get("/settings/ai/models/{provider}", handleAIModelsList(d.SettingsMgr))
			// Smoke-test the currently configured provider. Sends a
			// trivial prompt and echoes the reply + token usage +
			// round-trip latency. Lets the Settings UI verify the
			// key+model pair without waiting for a downstream AI
			// feature (digest, anomaly, etc.) to exercise it.
			r.Post("/ai/ping", handleAIPing(d.SettingsMgr))

			// Operational admin surface. Today's only endpoint is the
			// Rivian-upstream kill switch (ARCHITECTURE decision 6 /
			// ROADMAP Phase 1). GET returns the cached flag state;
			// PUT flips it and refreshes the local snapshot
			// immediately. Remote pods see the change on their next
			// poll (~10s) — decision 6 sizes that delay explicitly.
			r.Route("/admin", func(r chi.Router) {
				r.Get("/kill-switch", handleFlagsGet(d.Flags))
				r.Put("/kill-switch", handleFlagsKillPut(d.Flags))
			})

			// Read-only session/telemetry endpoints. Populated by either the
			// ElectraFi importer or the (future) live Rivian ingester.
			r.Get("/drives", handleDrives(d.Drives, d.Charges, d.SettingsStore))
			r.Get("/charges", handleCharges(d.Charges, d.SettingsStore))
			// DELETE /charges/{id} removes a single charge row owned
			// by the current user. Used by the UI's per-row "delete"
			// affordance to clear obviously-broken sessions (e.g.
			// pre-v0.10.7 phantom rows where SoC went down).
			r.Delete("/charges/{id}", handleDeleteCharge(d.Charges))
			// PATCH /charges/{id}/pricing lets the UI override cost /
			// currency / price-per-kWh on a single row — useful for
			// DCFC sessions paid outside the Rivian app, where we
			// have no upstream price.
			r.Patch("/charges/{id}/pricing", handlePatchChargePricing(d.Charges))
			// Pure-local analysis over the stored charge set. Groups
			// sessions into Home / Public / Fast buckets: peak-power
			// >=50 kW is Fast (DCFC) regardless of where it happened,
			// and the remaining slow sessions are DBSCAN-clustered on
			// (lat, lon) with the largest cluster winning Home and
			// everything else being Public. No external calls; no LLM.
			r.Get("/charges/clusters", handleChargeClusters(d.Charges))
			r.Get("/samples", handleSamples(d.Samples))
		}) // end of timed authenticated /api group

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
		}) // end of bulk-data authenticated /api group
	})

	// Everything else falls through to the embedded SPA. The SPA
	// itself is always reachable — it needs to render the login
	// page when the /api/auth/me bootstrap returns 401.
	r.Handle("/*", spaHandler(d.WebFS))

	return r
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

func handleVehicles(c rivian.Client, mon *rivian.StateMonitor) http.HandlerFunc {
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
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		// Enrich each vehicle with cached monitor metadata (PackKWh +
		// ImageURL), when available. The live Vehicles() call returns
		// trim/year/pack already, but image URLs come from a separate
		// Rivian endpoint cached only on the monitor.
		if mon != nil {
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
		}
		writeJSON(w, http.StatusOK, vs)
	}
}

// handleVehicleState returns a current snapshot for the given vehicle.
// 404 if no live client is configured, 502 for upstream failures.
//
// When a StateMonitor is wired, serves from its cache (populated by a
// long-lived websocket subscription). On cache miss it falls back to
// a one-shot REST fetch, primes the cache with the result, and kicks
// off the subscription so subsequent calls are free.
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
			mon.EnsureSubscribed(id)
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
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
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
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
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
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
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
// from the operator-configured home $/kWh rate. For sessions Rivian
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
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, decorateLiveSession(sess, cfg))
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
// operator has set a home $/kWh rate and the Rivian-reported price
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
// PricePerKWh (set when Rivian or the operator-configured home rate
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
// operator has set a home $/kWh rate. Cost is only attached when
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
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := store.ListSince(r.Context(), since, limit)
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
		if d.Drives == nil || d.Charges == nil || d.Samples == nil {
			http.Error(w, "import unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "parse upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		files := r.MultipartForm.File["file"]
		if len(files) == 0 {
			http.Error(w, "no files uploaded under field 'file'", http.StatusBadRequest)
			return
		}
		imp := &electrafi.Importer{Drives: d.Drives, Charges: d.Charges, Samples: d.Samples}
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
		for i, fh := range files {
			f, err := fh.Open()
			if err != nil {
				emit(map[string]any{"event": "error", "file": fh.Filename, "error": "open: " + err.Error()})
				return
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
				emit(map[string]any{"event": "error", "file": fh.Filename, "error": err.Error()})
				return
			}
			results = append(results, res)
			emit(map[string]any{"event": "file_done", "index": i, "result": res})
		}
		emit(map[string]any{"event": "done", "files": results})
	}
}

// handleDataBackup streams a single JSON bundle containing every
// drive, charge, and raw sample for the current user. Intended to
// be paired with the reset endpoint so an operator can snapshot
// their data before wiping it. The response is served with a
// Content-Disposition attachment so browsers download it directly;
// nothing is kept server-side.
func handleDataBackup(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Drives == nil || d.Charges == nil || d.Samples == nil {
			http.Error(w, "backup unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		ctx := r.Context()
		drv, err := d.Drives.ListAll(ctx)
		if err != nil {
			http.Error(w, "list drives: "+err.Error(), http.StatusInternalServerError)
			return
		}
		chg, err := d.Charges.ListAll(ctx)
		if err != nil {
			http.Error(w, "list charges: "+err.Error(), http.StatusInternalServerError)
			return
		}
		smp, err := d.Samples.ListAll(ctx)
		if err != nil {
			http.Error(w, "list samples: "+err.Error(), http.StatusInternalServerError)
			return
		}
		stamp := time.Now().UTC().Format("20060102-150405")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition",
			`attachment; filename="rivolt-backup-`+stamp+`.json"`)
		writeJSON(w, http.StatusOK, map[string]any{
			"version":    d.Version,
			"created_at": time.Now().UTC().Format(time.RFC3339),
			"drives":     drv,
			"charges":    chg,
			"samples":    smp,
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
		if d.Drives == nil || d.Charges == nil || d.Samples == nil {
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
			if err := d.Drives.Upsert(ctx, drv); err != nil {
				http.Error(w, "upsert drive "+drv.ID+": "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		for _, chg := range bundle.Charges {
			if err := d.Charges.Upsert(ctx, chg); err != nil {
				http.Error(w, "upsert charge "+chg.ID+": "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if err := d.Samples.InsertBatch(ctx, bundle.Samples); err != nil {
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
		if d.Drives == nil || d.Charges == nil || d.Samples == nil {
			http.Error(w, "reset unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		ctx := r.Context()
		// Wipe in an order that can't violate FKs; there are no
		// cross-table FKs on user_id so order is cosmetic.
		samplesN, err := d.Samples.Reset(ctx)
		if err != nil {
			http.Error(w, "reset samples: "+err.Error(), http.StatusInternalServerError)
			return
		}
		drivesN, err := d.Drives.Reset(ctx)
		if err != nil {
			http.Error(w, "reset drives: "+err.Error(), http.StatusInternalServerError)
			return
		}
		chargesN, err := d.Charges.Reset(ctx)
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
