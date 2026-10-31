package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/histwin"
	"github.com/apohor/rivolt/internal/ledger"
)

const (
	// tripsDefaultRange is the window /api/ledger/trips covers when the
	// caller doesn't say.
	tripsDefaultRange = 90 * 24 * time.Hour
	// tripsMaxRange bounds one request.
	tripsMaxRange = 366 * 24 * time.Hour
	// tripsPadding is how far beyond [from, to) rows are loaded so a
	// trip that straddles the window's edges is seen whole.
	tripsPadding = 7 * 24 * time.Hour
)

// trips serves GET /api/ledger/trips?from=&to=[&tz][&vehicle_id]: trips
// (outings that went > 150 km from home or used a DC fast charger)
// starting in [from, to), newest first. from/to are YYYY-MM-DD (in tz)
// or RFC 3339; default is the last 90 days.
func (a ledgerAPI) trips(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	p, ok := a.params(w, r, uid)
	if !ok {
		return
	}
	q := r.URL.Query()
	to := a.now()
	if v := strings.TrimSpace(q.Get("to")); v != "" {
		t, err := parseTripBound(v, p.loc)
		if err != nil {
			http.Error(w, "to must be YYYY-MM-DD or RFC 3339", http.StatusBadRequest)
			return
		}
		to = t
	}
	from := to.Add(-tripsDefaultRange)
	if v := strings.TrimSpace(q.Get("from")); v != "" {
		t, err := parseTripBound(v, p.loc)
		if err != nil {
			http.Error(w, "from must be YYYY-MM-DD or RFC 3339", http.StatusBadRequest)
			return
		}
		from = t
	}
	if !from.Before(to) || to.Sub(from) > tripsMaxRange {
		http.Error(w, "from must be before to, at most 366 days apart", http.StatusBadRequest)
		return
	}
	// A free account's range is clamped to its window (never trusted); a range wholly before it is empty.
	from = histwin.Clamp(r.Context(), from)
	if !from.Before(to) {
		from = to
	}
	cfg, err := a.config(r.Context(), uid)
	if err != nil {
		http.Error(w, "ledger: "+err.Error(), http.StatusInternalServerError)
		return
	}
	cs, ds, err := a.load(r.Context(), uid, p.vehicleID, from.Add(-tripsPadding), to.Add(tripsPadding))
	if err != nil {
		http.Error(w, "ledger: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := ledger.BuildTrips(ledger.Input{Charges: cs, Drives: ds, Config: cfg, Now: a.now()}, from.UTC(), to.UTC(), p.loc)
	writeJSON(w, http.StatusOK, out)
}

// parseTripBound reads YYYY-MM-DD (midnight in loc) or RFC 3339.
func parseTripBound(v string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", v, loc)
}
