package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/vehiclelease"
)

// Lease tracker: per-vehicle lease terms (GET/PUT/DELETE
// /api/vehicles/{id}/lease) and the computed standing (GET .../lease/status).
// 404 when no lease is set so the client can hide the card. Distances are
// miles; the client converts to the user's unit. vehicleScoped has already
// checked ownership.

// leaseVehicle resolves the path's Rivian id to the internal id.
func leaseVehicle(sqlDB *sql.DB, uid uuid.UUID, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if sqlDB == nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return uuid.Nil, false
	}
	var vid uuid.UUID
	if err := sqlDB.QueryRowContext(r.Context(),
		`SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2`,
		uid, chi.URLParam(r, "vehicleID")).Scan(&vid); err != nil {
		http.Error(w, "vehicle not found", http.StatusNotFound)
		return uuid.Nil, false
	}
	return vid, true
}

func handleLeaseGet(sqlDB *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		vid, ok := leaseVehicle(sqlDB, uid, w, r)
		if !ok {
			return
		}
		t, err := vehiclelease.NewStore(sqlDB).Get(r.Context(), uid, vid)
		if err != nil {
			http.Error(w, "lease unavailable", http.StatusInternalServerError)
			return
		}
		if t == nil {
			http.Error(w, "no lease", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func handleLeasePut(sqlDB *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		vid, ok := leaseVehicle(sqlDB, uid, w, r)
		if !ok {
			return
		}
		var t vehiclelease.Terms
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&t); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := t.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := vehiclelease.NewStore(sqlDB).Put(r.Context(), uid, vid, t); err != nil {
			http.Error(w, "lease save failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func handleLeaseDelete(sqlDB *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		vid, ok := leaseVehicle(sqlDB, uid, w, r)
		if !ok {
			return
		}
		if err := vehiclelease.NewStore(sqlDB).Delete(r.Context(), uid, vid); err != nil {
			http.Error(w, "lease delete failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleLeaseStatus(sqlDB *sql.DB, now func() time.Time) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		vid, ok := leaseVehicle(sqlDB, uid, w, r)
		if !ok {
			return
		}
		st := vehiclelease.NewStore(sqlDB)
		t, err := st.Get(r.Context(), uid, vid)
		if err != nil {
			http.Error(w, "lease unavailable", http.StatusInternalServerError)
			return
		}
		if t == nil {
			http.Error(w, "no lease", http.StatusNotFound)
			return
		}
		n := now()
		latest, past, err := st.Odometer(r.Context(), uid, vid, n)
		if err != nil {
			http.Error(w, "odometer unavailable", http.StatusInternalServerError)
			return
		}
		out, err := vehiclelease.Compute(*t, n, latest, past)
		if err != nil {
			http.Error(w, "lease unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}
