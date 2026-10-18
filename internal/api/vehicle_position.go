package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// maxPositionAge is how old the truck's last reading may be for "plan from
// where it is". A truck parked overnight stays where it is for hours; a reading
// older than this might be from before it moved.
const maxPositionAge = 12 * time.Hour

// vehiclePos is the truck's last reading: where it is and how charged.
type vehiclePos struct {
	VehicleID string    `json:"vehicle_id"`
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	SoC       float64   `json:"soc"`
	At        time.Time `json:"at"`
}

// currentPosition is the truck's latest position and charge: the per-pod
// monitor cache first (the freshest), then the newest recorded state row when
// another replica holds the lease. ok is false when there is no reading with a
// position, or the newest one is older than maxPositionAge.
func currentPosition(ctx context.Context, mon *rivian.StateMonitor, pool *sql.DB, uid uuid.UUID, vehicleID string, now time.Time) (vehiclePos, bool) {
	if vehicleID == "" {
		return vehiclePos{}, false
	}
	var best vehiclePos
	have := false
	if mon != nil {
		if st, _ := mon.Latest(vehicleID); st != nil && (st.Latitude != 0 || st.Longitude != 0) {
			at := st.LocationFixAt
			if at.IsZero() {
				at = st.At
			}
			best, have = vehiclePos{VehicleID: vehicleID, Lat: st.Latitude, Lon: st.Longitude, SoC: st.BatteryLevelPct, At: at.UTC()}, true
		}
	}
	if !have && pool != nil {
		var lat, lon, soc sql.NullFloat64
		var at time.Time
		err := pool.QueryRowContext(ctx, `
			SELECT lat, lon, battery_level_pct, at
			  FROM vehicle_state
			 WHERE user_id = $1
			   AND vehicle_id = (SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2 LIMIT 1)
			   AND lat IS NOT NULL AND lon IS NOT NULL
			 ORDER BY at DESC
			 LIMIT 1`, uid, vehicleID).Scan(&lat, &lon, &soc, &at)
		if err == nil && lat.Valid && lon.Valid && soc.Valid {
			best, have = vehiclePos{VehicleID: vehicleID, Lat: lat.Float64, Lon: lon.Float64, SoC: soc.Float64, At: at.UTC()}, true
		}
	}
	if !have || best.At.IsZero() || now.Sub(best.At) > maxPositionAge || best.At.After(now.Add(time.Hour)) {
		return vehiclePos{}, false
	}
	if best.Lat < -90 || best.Lat > 90 || best.Lon < -180 || best.Lon > 180 || best.SoC <= 0 || best.SoC > 100 {
		return vehiclePos{}, false
	}
	return best, true
}

// vehiclePositionFor is currentPosition; tests replace it.
var vehiclePositionFor = currentPosition

// noPosition answers 409: planning from the truck's position was asked for and
// there is no recent reading to plan from. The wording is growth's.
func noPosition(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": "Your truck's position isn't available right now. Rivolt needs a recent reading from the truck to re-plan. Try again once it's awake.",
		"class": "no_position",
	})
}

// fromPosition is the reading a plan started from, echoed so the SPA can say
// "from your truck's position, as of ...".
type fromPosition struct {
	Lat float64   `json:"lat"`
	Lon float64   `json:"lon"`
	SoC float64   `json:"soc"`
	At  time.Time `json:"at"`
}

func (p vehiclePos) echo() *fromPosition {
	return &fromPosition{Lat: p.Lat, Lon: p.Lon, SoC: p.SoC, At: p.At}
}
