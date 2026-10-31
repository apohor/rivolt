package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/settings"
)

// The free plan shows one vehicle (its "primary"). Every vehicle keeps recording, keeps its alerts and stays in the full export: only the
// reads of a non-primary vehicle's history and detail are Pro (402 pro_required), and the vehicle list still lists it, marked pro_locked.
//
// The primary is the user's choice in Settings (PUT /api/settings/primary-vehicle), stored under keyPrimaryVehicle. Until they choose, it
// defaults to the vehicle with the most recent drive or charge, and that default is stored the first time a free account needs it, so it
// does not move when another vehicle drives later. An account with one vehicle has no locked vehicle. Like every gate it fails open: a
// lookup that fails locks nothing.

const keyPrimaryVehicle = "vehicles.primary"

type proVehicles struct {
	db       *sql.DB
	settings *settings.Factory
	ent      *entitlements.Service
	log      *slog.Logger
}

// freeVehicle is the Rivian id a free account with several vehicles is limited to; "" when nothing is limited.
func (p *proVehicles) freeVehicle(ctx context.Context, uid uuid.UUID) string {
	if p == nil || p.db == nil || p.settings == nil || isPro(ctx, p.ent, uid) {
		return ""
	}
	owned, err := db.ListUserVehicles(ctx, p.db, uid)
	if err != nil {
		p.warn("vehicle limit: list", err)
		return ""
	}
	if len(owned) <= 1 {
		return ""
	}
	st := p.settings.For(uid)
	if st == nil {
		return ""
	}
	all, err := st.GetAll(ctx)
	if err != nil {
		p.warn("vehicle limit: settings", err)
		return ""
	}
	chosen := all[keyPrimaryVehicle]
	for _, v := range owned {
		if v.RivianVehicleID == chosen {
			return chosen
		}
	}
	// None chosen (or the chosen one is gone): the vehicle with the latest drive or charge, then the oldest vehicle. Stored once.
	var id string
	err = p.db.QueryRowContext(ctx, `
		SELECT v.rivian_vehicle_id FROM vehicles v
		 WHERE v.user_id = $1 AND v.rivian_vehicle_id NOT LIKE 'electrafi-%'
		 ORDER BY GREATEST(
		   COALESCE((SELECT max(started_at) FROM drives d WHERE d.vehicle_id = v.id), 'epoch'),
		   COALESCE((SELECT max(started_at) FROM charges c WHERE c.vehicle_id = v.id), 'epoch')) DESC, v.created_at, v.id
		 LIMIT 1`, uid).Scan(&id)
	if err != nil {
		p.warn("vehicle limit: default", err)
		return ""
	}
	if err := st.Set(ctx, keyPrimaryVehicle, id); err != nil {
		p.warn("vehicle limit: store default", err)
	}
	return id
}

func (p *proVehicles) warn(msg string, err error) {
	if p.log != nil {
		p.log.Warn(msg, "err", err.Error())
	}
}

type freeVehicleKey struct{}

// VehicleScopeHeader tells a client that this response was narrowed to one vehicle, the free account's primary: its value is that vehicle's
// Rivian id. Every route behind freeVehicleMiddleware (the drive and charge lists, the activity feed, the Money statement, summary, savings
// and CSV, charge split, samples and the rest) sets it, so a client can say "Showing <vehicle>. Pro includes all vehicles" without a field
// in each body. Absent for Pro, one vehicle, billing off or a failed plan lookup (nothing was narrowed).
const VehicleScopeHeader = "X-Rivolt-Vehicle-Scope"

// freeVehicleMiddleware stamps the free account's only vehicle on the request (nothing for Pro, one vehicle, billing off or a failed
// lookup). vehicleFilter then limits the drive and charge lists to it and answers 402 for another vehicle.
func freeVehicleMiddleware(p *proVehicles) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if p == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if uid, ok := auth.UserFromContext(r.Context()); ok {
				if id := p.freeVehicle(r.Context(), uid); id != "" {
					r = r.WithContext(context.WithValue(r.Context(), freeVehicleKey{}, id))
					w.Header().Set(VehicleScopeHeader, id)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// vehicleGateMiddleware answers 402 pro_required to a free account's read of a vehicle-scoped route ({vehicleID} in the path) for a
// vehicle that is not its primary. Mount it after the ownership check.
func vehicleGateMiddleware(p *proVehicles) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if p == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if uid, ok := auth.UserFromContext(r.Context()); ok {
				if only := p.freeVehicle(r.Context(), uid); only != "" && only != chi.URLParam(r, "vehicleID") {
					writeProRequired(w)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rowVehicleMiddleware is the same gate for a single drive or charge named by {id} (its external id): 402 when it belongs to a vehicle
// other than the free account's primary. table is "drives" or "charges". A row that does not exist passes, so the handler's 404 answers.
func rowVehicleMiddleware(p *proVehicles, table string) func(http.Handler) http.Handler {
	if table != "drives" && table != "charges" {
		panic("rowVehicleMiddleware: " + table)
	}
	return func(next http.Handler) http.Handler {
		if p == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := auth.UserFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			only := p.freeVehicle(r.Context(), uid)
			if only == "" {
				next.ServeHTTP(w, r)
				return
			}
			var rid string
			err := p.db.QueryRowContext(r.Context(), `SELECT v.rivian_vehicle_id FROM `+table+` t JOIN vehicles v ON v.id = t.vehicle_id
				 WHERE t.user_id = $1 AND t.external_id = $2 ORDER BY t.started_at DESC LIMIT 1`, uid, chi.URLParam(r, "id")).Scan(&rid)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				p.warn("vehicle limit: row", err) // fail open
			} else if err == nil && rid != only {
				writeProRequired(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// lockedID is the free account's only vehicle for the signed-in user of ctx ("" when nothing is limited): the vehicle list marks the rest.
func (p *proVehicles) lockedID(ctx context.Context) (only string) {
	if uid, ok := auth.UserFromContext(ctx); ok {
		return p.freeVehicle(ctx, uid)
	}
	return ""
}

// handlePrimaryVehicle: GET returns {"vehicle_id"} (empty until chosen or defaulted); PUT {"vehicle_id"} chooses one of the user's own
// vehicles. Free for every plan (it is a setting), 404 for a vehicle the user does not own.
func handlePrimaryVehicle(p *proVehicles) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if p == nil || p.db == nil || p.settings == nil || p.settings.For(uid) == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
			return
		}
		st := p.settings.For(uid)
		if r.Method == http.MethodPut {
			var b struct {
				VehicleID string `json:"vehicle_id"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&b); err != nil || b.VehicleID == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "vehicle_id required"})
				return
			}
			owns, err := db.OwnsRivianID(r.Context(), p.db, uid, b.VehicleID)
			if err != nil {
				serverError(w, r, "primary vehicle: ownership", err)
				return
			}
			if !owns {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
				return
			}
			if err := st.Set(r.Context(), keyPrimaryVehicle, b.VehicleID); err != nil {
				serverError(w, r, "primary vehicle: save", err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"vehicle_id": b.VehicleID})
			return
		}
		all, err := st.GetAll(r.Context())
		if err != nil {
			serverError(w, r, "primary vehicle: read", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"vehicle_id": all[keyPrimaryVehicle]})
	}
}

// statementScope is what a user's statement email (and its preview) covers: the Rivian id of the one vehicle a free account with several
// vehicles is limited to, and the line the email carries about it. ("", "") for a Pro account, one vehicle, billing off or a failed lookup
// (the whole account, no line). It needs no request: the job calls it per user, with the same rules as the middleware.
func (p *proVehicles) statementScope(ctx context.Context, uid uuid.UUID) (vehicleID, note string) {
	only := p.freeVehicle(ctx, uid)
	if only == "" {
		return "", ""
	}
	label := "your primary vehicle"
	if vs, err := db.ListUserVehicles(ctx, p.db, uid); err == nil {
		label = vehicleLabel(vs, only)
	}
	return only, "Showing " + label + ". Pro includes all vehicles."
}

// vehicleLabel names a vehicle in an email: its model ("R1T"), never the user's own text, except when the account has two vehicles of the
// same model, where the nickname is the only way to tell them apart. Without either, "your primary vehicle".
func vehicleLabel(vs []db.VehicleSummary, id string) string {
	for _, v := range vs {
		if v.RivianVehicleID != id {
			continue
		}
		if v.Model == "" {
			if v.DisplayName != "" {
				return v.DisplayName
			}
			break
		}
		same := 0
		for _, o := range vs {
			if o.Model == v.Model {
				same++
			}
		}
		if same > 1 && v.DisplayName != "" {
			return v.DisplayName
		}
		return v.Model
	}
	return "your primary vehicle"
}
