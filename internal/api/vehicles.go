package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/packhealth"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
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

// packHealthHeadline and packHealthResponse are GET /api/vehicles/{id}/pack-health.
// The fields are in the alphabetical order the map they replaced encoded in, so the
// bytes are the same.
type packHealthHeadline struct {
	DocumentedKWh           float64 `json:"documented_kwh"`
	EffectiveKWh            float64 `json:"effective_kwh"`
	NameplateKWh            float64 `json:"nameplate_kwh"`
	PctOfNameplate          float64 `json:"pct_of_nameplate"`
	ReportedKWh             float64 `json:"reported_kwh"`
	ReportedPctOfDocumented float64 `json:"reported_pct_of_documented"`
	SampleCount             int     `json:"sample_count"`
	Window                  int     `json:"window"`
}

type packHealthResponse struct {
	Headline packHealthHeadline  `json:"headline"`
	Samples  []packhealth.Sample `json:"samples"`
}

// packHealthMaxLimit bounds ?limit on pack-health.
const packHealthMaxLimit = 5000

// newestSamples is the last n of an oldest-first series (all of it when n is 0 or
// at least its length). It never returns nil.
func newestSamples(samples []packhealth.Sample, n int) []packhealth.Sample {
	if n > 0 && len(samples) > n {
		samples = samples[len(samples)-n:]
	}
	if samples == nil {
		return []packhealth.Sample{}
	}
	return samples
}

// handlePackHealthGet returns the derived effective-pack-capacity
// time series for a vehicle, plus a headline (current effective
// kWh + % of nameplate). Time series is oldest-first so the SPA
// can plot left-to-right directly. Returns 200 with empty samples
// when no qualifying charges exist yet — the SPA handles the empty
// state.
func handlePackHealthGet(sqlDB *sql.DB, ph *packhealth.Store) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		// ?limit=N keeps only the newest N samples in the answer (a phone chart does
		// not need every charge since the truck was bought); the headline is still
		// worked out from all of them. Without it, every sample, as before.
		limit := 0
		if s := r.URL.Query().Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > packHealthMaxLimit {
				http.Error(w, "limit must be 1 to "+strconv.Itoa(packHealthMaxLimit), http.StatusBadRequest)
				return
			}
			limit = n
		}
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
		// Only a pack we identified is a spec; the model-only default is a guess
		// and is reported as 0 ("unknown"), so the UI draws no spec line and no
		// degradation against it.
		documentedKWh, specKnown := rivian.InferPackKWhKnown(model.String, trim.String, int(modelYear.Int64))
		if !specKnown {
			documentedKWh = 0
		}
		reportedKWh := nameplateKWh.Float64
		var reportedPctOfDocumented float64
		if documentedKWh > 0 && reportedKWh > 0 {
			reportedPctOfDocumented = reportedKWh / documentedKWh * 100.0
		}
		writeJSON(w, http.StatusOK, packHealthResponse{
			Samples: newestSamples(samples, limit),
			Headline: packHealthHeadline{
				EffectiveKWh:            headlineEffective,
				NameplateKWh:            nameplateKWh.Float64,
				PctOfNameplate:          pctOfNameplate,
				DocumentedKWh:           documentedKWh,
				ReportedKWh:             reportedKWh,
				ReportedPctOfDocumented: reportedPctOfDocumented,
				SampleCount:             len(samples),
				Window:                  len(clean),
			},
		})
	}
}

// hasSession says whether the user has a stored Rivian session (nil: none can be stored, so none is). It decides
// what a client with no session means: never linked (an empty list) or linked but unreadable right now (an error).
func handleVehicles(c rivian.Client, mon *rivian.StateMonitor, sqlDB *sql.DB, logger *slog.Logger, hasSession func(context.Context) (bool, error), onlyVehicle func(context.Context) string) http.HandlerFunc {
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
			// A user who has not linked a Rivian account has no client session: that is
			// "no vehicles yet", not a bad gateway (a new signup's first load got a 502).
			// A linked account whose session died is ErrNeedsReauth, a 401 below; one whose
			// stored session could not be read or restored stays an error, so the user is
			// never told they have no vehicles when the truth is that we could not look.
			if errors.Is(err, rivian.ErrNotAuthenticated) {
				linked := false
				if hasSession != nil {
					var lerr error
					if linked, lerr = hasSession(r.Context()); lerr != nil {
						linked = true
						if logger != nil {
							logger.Warn("vehicles: stored rivian session unreadable", "err", lerr.Error())
						}
					}
				}
				if !linked {
					writeJSON(w, http.StatusOK, []rivian.Vehicle{})
					return
				}
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
					uerr := db.UpsertVehicleFromSync(r.Context(), sqlDB, userID, db.VehicleSync{
						RivianID: vs[i].ID, VIN: vs[i].VIN, Name: vs[i].Name, Model: vs[i].Model,
						ModelYear: vs[i].ModelYear, Trim: vs[i].TrimID, PackKWh: vs[i].PackKWh,
					})
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
		// The hero image arrives after the upsert (monitor cache), so store it
		// now: vehicles.image_url was empty for every vehicle.
		if sqlDB != nil {
			if userID, ok := auth.UserFromContext(r.Context()); ok {
				for i := range vs {
					if vs[i].ID == "" || vs[i].ImageURL == "" {
						continue
					}
					if ierr := db.SetVehicleImageURL(r.Context(), sqlDB, userID, vs[i].ID, vs[i].ImageURL); ierr != nil && logger != nil {
						logger.Warn("vehicle image persist failed", "rivian_vehicle_id", vs[i].ID, "err", ierr.Error())
					}
				}
			}
		}
		// OTA release details (current and available version, release-notes
		// links) come from what the OTA sync stored; this never calls Rivian.
		if sqlDB != nil {
			if userID, ok := auth.UserFromContext(r.Context()); ok {
				if stored, oerr := db.ListVehicleOTAs(r.Context(), sqlDB, userID); oerr == nil {
					attachOTA(vs, stored, func(id string) *rivian.State {
						if mon == nil {
							return nil
						}
						st, _ := mon.Latest(id)
						return st
					})
				}
			}
		}
		for i := range vs {
			vs[i].FieldSupport = rivian.FieldSupport(vs[i].Model)
			if mon != nil {
				if st, _ := mon.Latest(vs[i].ID); st != nil {
					liveFieldSupport(vs[i].FieldSupport, st)
				}
			}
		}
		// A free account with several vehicles sees one: the others stay listed, marked, so the user knows they are there.
		if onlyVehicle != nil {
			if only := onlyVehicle(r.Context()); only != "" {
				for i := range vs {
					vs[i].ProLocked = vs[i].ID != only
				}
			}
		}
		writeJSON(w, http.StatusOK, vs)
	}
}

// liveFieldSupport refines the model's table with what the vehicle is sending
// now: a time to full the vehicle itself reports is "reported" on any model,
// and one taken from Parallax is an "estimate".
func liveFieldSupport(fs map[string]string, st *rivian.State) {
	if st.TimeToEndMins > 0 {
		if st.TimeToEndSource == "estimate" {
			fs["charge_time_remaining"] = rivian.FieldEstimate
		} else {
			fs["charge_time_remaining"] = rivian.FieldReported
		}
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
func handleVehicleState(c rivian.Client, mon *rivian.StateMonitor, hasSession func(context.Context) (bool, error)) http.HandlerFunc {
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
		// The owner's own cache, else the owner's published snapshot
		// (subscription-only fields the REST fallback lacks), else the
		// local entry, fetched from Rivian at most every few minutes and
		// never while asleep. See StateMonitor.ServeState.
		var have *rivian.State
		if mon != nil {
			cached, fetch := mon.ServeState(r.Context(), id)
			if !fetch {
				if cached == nil {
					w.Header().Set("Retry-After", "60")
					http.Error(w, "vehicle state not available yet", http.StatusServiceUnavailable)
					return
				}
				writeJSON(w, http.StatusOK, cached)
				return
			}
			have = cached
		}
		st, err := c.State(r.Context(), id)
		if err != nil {
			if have != nil {
				writeJSON(w, http.StatusOK, have)
				return
			}
			if errors.Is(err, rivian.ErrNotImplemented) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			writeStateError(w, r, err, hasSession)
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
func handleVehicleStateFresh(c rivian.Client, hasSession func(context.Context) (bool, error)) http.HandlerFunc {
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
			writeStateError(w, r, err, hasSession)
			return
		}
		writeJSON(w, http.StatusOK, st)
	}
}

// notConnectedBody is the 409 answer for a vehicle of an account with no linked Rivian session.
type notConnectedBody struct {
	Error  string `json:"error"`
	Class  string `json:"class"`
	Reason string `json:"reason"`
}

// writeStateError answers a failed read of one of the account's vehicles. A vehicle of an account that has never linked a
// Rivian session (a seeded vehicle, a user who has not connected yet) is the user's state, not a failing gateway: 409 with
// class user_action and reason not_connected, which the UI can show as "connect your Rivian" and which the metrics and alerts
// do not count as a server failure. It is told apart from "linked, but this pod could not restore the session" (also
// ErrNotAuthenticated) by asking whether a session is stored: if one is, or it cannot be read, the error stays a 502, so a
// user is never told to reconnect when the truth is that we could not look (the /api/vehicles list makes the same call).
func writeStateError(w http.ResponseWriter, r *http.Request, err error, hasSession func(context.Context) (bool, error)) {
	if errors.Is(err, rivian.ErrNotAuthenticated) && hasSession != nil {
		if linked, lerr := hasSession(r.Context()); lerr == nil && !linked {
			writeJSON(w, http.StatusConflict, notConnectedBody{
				Error:  "Rivian is not connected to this account",
				Class:  rivian.ClassUserAction.String(),
				Reason: "not_connected",
			})
			return
		}
	}
	writeUpstreamError(w, err)
}

// attachOTA sets each vehicle's OTA details from the stored JSON and adds
// the live status. Where Rivian's getOTAUpdateDetails gave no version (it
// fails for the R2s), the live state's versions stand in (vehicleState, or
// Parallax ota.deployment.state), without release-notes links. A row that
// does not decode is treated as absent.
func attachOTA(vs []rivian.Vehicle, stored map[string]json.RawMessage, live func(id string) *rivian.State) {
	for i := range vs {
		var info rivian.OTAInfo
		raw, have := stored[vs[i].ID]
		if have && json.Unmarshal(raw, &info) != nil {
			info, have = rivian.OTAInfo{}, false
		}
		var st *rivian.State
		if live != nil {
			st = live(vs[i].ID)
		}
		if st != nil {
			if info.Current == nil || info.Current.Version == "" {
				if v := strings.TrimSpace(st.OtaCurrentVersion); v != "" {
					info.Current, have = &rivian.OTAVersion{Version: v}, true
					if a := strings.TrimSpace(st.OtaAvailableVersion); a != "" && a != v && info.Available == nil {
						info.Available = &rivian.OTAVersion{Version: a}
					}
				}
			}
			info.Status = st.OtaStatus
			info.InstallProgress = rivian.OTAInstallPercent(st.OtaStatus, st.OtaInstallProgress)
		}
		if !have {
			continue
		}
		vs[i].OTA = &info
	}
}

// rivianSessionCheck says whether the user has a Rivian session stored (nil without a secrets store).
func (d Deps) rivianSessionCheck(uid uuid.UUID) func(context.Context) (bool, error) {
	if d.Secrets == nil {
		return nil
	}
	return func(ctx context.Context) (bool, error) {
		sess, err := secrets.LoadRivianSession(ctx, d.Secrets, uid)
		return sess.UserSessionToken != "", err
	}
}
