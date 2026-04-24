// Package api wires the HTTP surface for Rivolt. It assembles routes,
// middleware, and handler dependencies into a single chi Mux.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
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
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
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
	Auth    *auth.Service
	WebFS   fs.FS
	Version string
}

// New builds the root mux with all routes mounted.
func New(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
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

			r.Route("/push", func(r chi.Router) {
				r.Get("/vapid-key", handlePushVAPIDKey(d.PushService))
				r.Post("/subscribe", handlePushSubscribe(d.PushStore))
				r.Post("/unsubscribe", handlePushUnsubscribe(d.PushStore))
			})

			// Rivian live endpoints. /api/vehicles returns [] when no real
			// client is configured (the stub returns ErrNotImplemented);
			// other errors surface as 502 so the UI can show them.
			r.Get("/vehicles", handleVehicles(d.Rivian, d.StateMonitor))
			r.Get("/state/{vehicleID}", handleVehicleState(d.Rivian, d.StateMonitor))
			r.Get("/state/{vehicleID}/debug", handleVehicleStateDebug(d.Rivian))
			r.Get("/state/{vehicleID}/fresh", handleVehicleStateFresh(d.Rivian))
			r.Get("/live-session/{vehicleID}", handleLiveSession(d.Rivian, d.StateMonitor, d.SettingsStore))
			r.Get("/live-drive/{vehicleID}", handleLiveDrive(d.StateMonitor))
			r.Get("/charging-schema", handleChargingSchemaProbe(d.Rivian))
			r.Get("/charging-field/{field}", handleChargingFieldProbe(d.Rivian))
			r.Get("/charging-frames", handleChargingFrames(d.Rivian))

			// Rivian account management. Only wired when a live client is
			// present; with the stub/mock these return 404.
			r.Route("/settings/rivian", func(r chi.Router) {
				r.Get("/", handleRivianStatus(d.RivianAccount))
				r.Post("/login", handleRivianLogin(d.RivianAccount, d.SettingsStore))
				r.Post("/mfa", handleRivianMFA(d.RivianAccount, d.SettingsStore))
				r.Post("/logout", handleRivianLogout(d.RivianAccount, d.SettingsStore))
			})

			// Home electricity cost settings, applied locally to estimate
			// the price of sessions Rivian reports as free (home AC, L2,
			// non-RAN public chargers).
			r.Route("/settings/charging", func(r chi.Router) {
				r.Get("/", handleChargingSettingsGet(d.SettingsStore))
				r.Put("/", handleChargingSettingsPut(d.SettingsStore))
			})

			// AI provider configuration (OpenAI / Anthropic / Gemini).
			// GET returns the redacted public view (api keys reported as
			// has_key only); PUT accepts a partial patch. The /models
			// endpoint proxies the provider's catalogue API so the UI can
			// populate a dropdown instead of asking users to memorise IDs.
			r.Get("/settings/ai", handleAISettingsGet(d.SettingsMgr))
			r.Put("/settings/ai", handleAISettingsPut(d.SettingsMgr))
			r.Get("/settings/ai/models/{provider}", handleAIModelsList(d.SettingsMgr))

			// Read-only session/telemetry endpoints. Populated by either the
			// ElectraFi importer or the (future) live Rivian ingester.
			r.Get("/drives", handleDrives(d.Drives))
			r.Get("/charges", handleCharges(d.Charges, d.SettingsStore))
			// Pure-local analysis over the stored charge set. Groups
			// sessions into Home / Work / Public buckets using DBSCAN on
			// (lat, lon). No external calls; no LLM involved.
			r.Get("/charges/clusters", handleChargeClusters(d.Charges))
			r.Get("/samples", handleSamples(d.Samples))

			// Accepts a multipart upload of an ElectraFi CSV export. Streams
			// it through the importer so users don't have to drop into a
			// terminal to load data.
			r.Post("/import/electrafi", handleImportElectrafi(d))
		}) // end of authenticated /api group
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

func handleDrives(store *drives.Store) http.HandlerFunc {
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
		if out == nil {
			out = []drives.Drive{}
		}
		writeJSON(w, http.StatusOK, out)
	}
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
		results := make([]electrafi.Result, 0, len(files))
		for _, fh := range files {
			f, err := fh.Open()
			if err != nil {
				http.Error(w, fh.Filename+": open: "+err.Error(), http.StatusBadRequest)
				return
			}
			res, err := imp.ImportReader(r.Context(), fh.Filename, f)
			f.Close()
			if err != nil {
				http.Error(w, fh.Filename+": import: "+err.Error(), http.StatusBadRequest)
				return
			}
			results = append(results, res)
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": results})
	}
}

// --- Charge clustering ----------------------------------------------------
//
// Pure-local DBSCAN grouping of charge sessions by (lat, lon). The UI
// uses it to label Home vs. Work vs. Public charging on /charges and
// in the overview summary. No external calls.

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
