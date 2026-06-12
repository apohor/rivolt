package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/electrafi"
	"github.com/apohor/rivolt/internal/samples"
)

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
			RivianVehicleID string            `json:"rivian_vehicle_id"`
			DisplayName     string            `json:"display_name,omitempty"`
			Profile         db.VehicleProfile `json:"profile"`
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
