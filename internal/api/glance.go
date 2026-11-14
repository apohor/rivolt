package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// glanceLookback bounds how old the newest row may be and still be shown; older than this the vehicle has no widget state.
const glanceLookback = 7 * 24 * time.Hour

// glanceResponse is what the iPhone widget and any other glance client draw, from the vehicle's newest stored row. Field names follow /api/state where they exist
// there (distance_to_empty in km, charger_state, charge_target_pct), so a client can read either. No location is returned.
type glanceResponse struct {
	Version         int       `json:"version"`
	At              time.Time `json:"at"`
	BatteryLevelPct *float64  `json:"battery_level_pct"`
	DistanceToEmpty *float64  `json:"distance_to_empty"`
	ChargeTargetPct *float64  `json:"charge_target_pct"`
	Gear            string    `json:"gear,omitempty"`
	PowerState      string    `json:"power_state,omitempty"`
	ChargerState    string    `json:"charger_state,omitempty"`
	ChargerPowerKW  *float64  `json:"charger_power_kw"`
	State           string    `json:"state"`
	Connection      string    `json:"connection"`
}

// glanceSQL reads the newest row for the vehicle: one LIMIT 1 index probe, nothing else.
var glanceSQL = fmt.Sprintf(`
WITH v AS (
	SELECT id, (upper(COALESCE(model, '')) = 'R2') AS r2 FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2
)
SELECT vs.at, vs.battery_level_pct, vs.range_mi, vs.charge_limit_pct, COALESCE(vs.shift_state, ''), COALESCE(vs.power_state, ''),
       COALESCE(vs.charging_state, ''), vs.charger_power_kw, `+nowStateCase+`
FROM vehicle_state vs
WHERE vs.user_id = $1 AND vs.vehicle_id = (SELECT id FROM v) AND vs.at >= $3::timestamptz
ORDER BY vs.at DESC LIMIT 1`, "vs")

// glanceAPI serves GET /api/vehicles/{vehicleID}/glance. It reads Postgres only: it holds no Rivian client and no monitor, so a widget
// refresh can never become a Rivian call, whichever pod answers.
type glanceAPI struct {
	row        func(ctx context.Context, uid uuid.UUID, rivianID string, since time.Time) (*glanceResponse, error)
	connection func(uid uuid.UUID) string
	now        func() time.Time
}

func newGlanceRow(db *sql.DB) func(context.Context, uuid.UUID, string, time.Time) (*glanceResponse, error) {
	return func(ctx context.Context, uid uuid.UUID, rivianID string, since time.Time) (*glanceResponse, error) {
		var out glanceResponse
		var soc, rng, target, kw sql.NullFloat64
		err := db.QueryRowContext(ctx, glanceSQL, uid, rivianID, since).Scan(
			&out.At, &soc, &rng, &target, &out.Gear, &out.PowerState, &out.ChargerState, &kw, &out.State)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if soc.Valid {
			out.BatteryLevelPct = &soc.Float64
		}
		if rng.Valid {
			km := rng.Float64 * kmPerMile
			out.DistanceToEmpty = &km
		}
		if target.Valid {
			out.ChargeTargetPct = &target.Float64
		}
		if kw.Valid {
			out.ChargerPowerKW = &kw.Float64
		}
		return &out, nil
	}
}

func (a glanceAPI) handle(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "vehicleID"))
	if id == "" {
		http.Error(w, "vehicleID required", http.StatusBadRequest)
		return
	}
	out, err := a.row(r.Context(), uid, id, a.now().Add(-glanceLookback))
	if err != nil {
		http.Error(w, "glance: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if out == nil {
		http.Error(w, "no state yet", http.StatusNotFound)
		return
	}
	out.Version = 1
	out.Connection = a.connection(uid)
	w.Header().Set("Cache-Control", "private, max-age=30")
	writeJSON(w, http.StatusOK, out)
}
