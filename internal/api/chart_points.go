package api

import (
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/histwin"
)

// maxChartWindow is the widest span one /api/chart-points answer covers.
const maxChartWindow = 400 * 24 * time.Hour

// handleChartPoints: GET /api/chart-points?since=RFC3339&until=RFC3339&vehicle_id=...
// returns what the iPhone's Home charts plot, as two compact lists, oldest first:
// drives (started_at, ended_at, distance_mi, start_soc_pct, end_soc_pct) and charges
// (started_at, ended_at, start_soc_pct, end_soc_pct). The app buckets by its own local
// day, like the web does from the full lists. No coordinates, ids, costs or places.
// The rows are the list rows (at least a minute; no re-parking drives), the caller's
// own, optionally one truck (vehicle_id, as on the lists). since defaults to 30 days
// ago and until to now; a window wider than maxChartWindow is a 400; at most
// MaxChartRows of each, the newest if there are more.
// chartWindow reads the window and truck the iPhone's aggregate endpoints share:
// since (default 30 days ago), until (default now), both RFC 3339, at most
// maxChartWindow apart, and the optional vehicle_id (as on the lists). Rows count when
// they start in [since, until): the end is exclusive, like the web's period filter.
// Writes a 400 and returns false for anything else.
func chartWindow(w http.ResponseWriter, r *http.Request) (since, until time.Time, vehicleID string, ok bool) {
	vehicleID, ok = vehicleFilter(w, r)
	if !ok {
		return
	}
	now := time.Now()
	since, until = now.Add(-30*24*time.Hour), now
	for name, dst := range map[string]*time.Time{"since": &since, "until": &until} {
		if s := r.URL.Query().Get(name); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				http.Error(w, name+" must be an RFC 3339 time", http.StatusBadRequest)
				return since, until, "", false
			}
			*dst = t
		}
	}
	if until.Before(since) || until.Sub(since) > maxChartWindow {
		http.Error(w, "the window must be between 0 and 400 days", http.StatusBadRequest)
		return since, until, "", false
	}
	// A free account's window: the requested range is clamped to it, whatever the client asked for.
	since = histwin.Clamp(r.Context(), since)
	return since, until, vehicleID, true
}

func handleChartPoints(ds *drives.Store, cs *charges.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		since, until, vehicleID, ok := chartWindow(w, r)
		if !ok {
			return
		}
		out := struct {
			Drives  []drives.ChartDrive   `json:"drives"`
			Charges []charges.ChartCharge `json:"charges"`
		}{Drives: []drives.ChartDrive{}, Charges: []charges.ChartCharge{}}
		if ds != nil {
			rows, err := ds.ListChartPoints(r.Context(), since, until, vehicleID)
			if err != nil {
				http.Error(w, "could not read drives", http.StatusInternalServerError)
				return
			}
			if rows != nil {
				out.Drives = rows
			}
		}
		if cs != nil {
			rows, err := cs.ListChartPoints(r.Context(), since, until, vehicleID)
			if err != nil {
				http.Error(w, "could not read charges", http.StatusInternalServerError)
				return
			}
			if rows != nil {
				out.Charges = rows
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}
