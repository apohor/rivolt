package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/packhealth"
	"github.com/apohor/rivolt/internal/rivian"
)

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

// handlePackHealthGet returns the derived effective-pack-capacity
// time series for a vehicle, plus a headline (current effective
// kWh + % of nameplate). Time series is oldest-first so the SPA
// can plot left-to-right directly. Returns 200 with empty samples
// when no qualifying charges exist yet — the SPA handles the empty
// state.
func handlePackHealthGet(sqlDB *sql.DB, ph *packhealth.Store) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if sqlDB == nil || ph == nil {
			http.Error(w, "pack-health unavailable", http.StatusServiceUnavailable)
			return
		}
		rivianID := chi.URLParam(r, "vehicleID")
		if rivianID == "" {
			http.Error(w, "missing vehicle id", http.StatusBadRequest)
			return
		}
		var (
			vid          uuid.UUID
			nameplateKWh sql.NullFloat64
			model, trim  sql.NullString
			modelYear    sql.NullInt64
		)
		if err := sqlDB.QueryRowContext(r.Context(), `
			SELECT id, pack_kwh, model, trim, model_year FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2
		`, uid, rivianID).Scan(&vid, &nameplateKWh, &model, &trim, &modelYear); err != nil {
			http.Error(w, "vehicle not found", http.StatusNotFound)
			return
		}
		samples, err := ph.ListByVehicle(r.Context(), vid, 0)
		if err != nil {
			http.Error(w, "list samples: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Never serialize JSON null for samples — the SPA reads
		// .length on the array unconditionally.
		if samples == nil {
			samples = []packhealth.Sample{}
		}
		// Headline: median of the most recent N samples that aren't
		// flagged as derate_active. Median is more robust than mean
		// against the occasional bad-data outlier; 10 samples is a
		// stable window without flattening genuine month-over-month
		// trends.
		const headlineWindow = 10
		recent := samples
		if len(recent) > headlineWindow {
			recent = recent[len(recent)-headlineWindow:]
		}
		clean := make([]float64, 0, len(recent))
		for _, s := range recent {
			if s.DerateActive {
				continue
			}
			clean = append(clean, s.PackKWhEffective)
		}
		var headlineEffective float64
		if len(clean) > 0 {
			sort.Float64s(clean)
			headlineEffective = clean[len(clean)/2]
		}
		var pctOfNameplate float64
		if nameplateKWh.Valid && nameplateKWh.Float64 > 0 && headlineEffective > 0 {
			pctOfNameplate = (headlineEffective / nameplateKWh.Float64) * 100.0
		}
		// Documented (nameplate spec) vs current (vehicle-reported).
		// documented_kwh is the static InferPackKWh lookup by
		// model/trim/year — the "when new" spec. reported_kwh is
		// pack_kwh, which the batteryCapacityHook overwrites with the
		// vehicle's own reported usable capacity once observed (from the
		// Parallax charge_state / legacy batteryCapacity). Their ratio is
		// the vehicle's own degradation signal; equal (100%) when no live
		// capacity has been observed yet.
		documentedKWh := rivian.InferPackKWh(model.String, trim.String, int(modelYear.Int64))
		reportedKWh := nameplateKWh.Float64
		var reportedPctOfDocumented float64
		if documentedKWh > 0 && reportedKWh > 0 {
			reportedPctOfDocumented = reportedKWh / documentedKWh * 100.0
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"samples": samples,
			"headline": map[string]any{
				"effective_kwh":              headlineEffective,
				"nameplate_kwh":              nameplateKWh.Float64,
				"pct_of_nameplate":           pctOfNameplate,
				"documented_kwh":             documentedKWh,
				"reported_kwh":               reportedKWh,
				"reported_pct_of_documented": reportedPctOfDocumented,
				"sample_count":               len(samples),
				"window":                     len(clean),
			},
		})
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
			// Local cache: populated on the pod that owns this vehicle's
			// WS lease (and therefore runs the subscriptions).
			if st, _ := mon.Latest(id); st != nil {
				writeJSON(w, http.StatusOK, st)
				return
			}
			// Peer snapshot: a replica that doesn't own the lease reads
			// the live State the owner published to the shared store, so
			// the response carries subscription-only / Parallax-only
			// fields (pack temp, tire pressures, charging context, driver
			// chips, windows) instead of the REST fallback below, which
			// omits every field vehicleState doesn't expose over REST.
			if st := mon.RemoteLatest(r.Context(), id); st != nil {
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
