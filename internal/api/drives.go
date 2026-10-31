package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/histwin"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/settings"
)

func handleDrives(store *drives.Store, chargesStore *charges.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		vehicleID, ok := vehicleFilter(w, r)
		if !ok {
			return
		}
		// ?slim=1 leaves out the route polyline (the lists draw no maps);
		// GET /api/drives/{id} has it for one drive.
		slim := r.URL.Query().Get("slim") == "1"
		list := store.ListRecent
		if slim {
			list = store.ListRecentSlim
		}
		if vehicleID != "" {
			list = func(ctx context.Context, limit int) ([]drives.Drive, error) {
				return store.ListRecentForVehicle(ctx, vehicleID, limit, !slim)
			}
		}
		out, err := list(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Drop ignition-cycle / gear-bounce rows so the SPA list
		// shows only real travel. The rows still exist in DB.
		out = drives.FilterReparking(out)
		// The free window narrows what is returned; the cost rates below are derived from every charge, so a free account's drive costs are
		// the costs a Pro account sees for the same drives.
		out = windowRows(r.Context(), out, func(d drives.Drive) time.Time { return d.StartedAt })
		cfg, _ := settings.GetChargingConfig(r.Context(), settingsStore)
		// Pull every charge once and sort ascending by EndedAt so we
		// can binary-search for the most recent charge that closed
		// before each drive started. Drive cost is then billed at
		// that charge's rate — a drive after fast-charging gets the
		// fast-charge rate, a drive after a home top-up gets the
		// home rate. Falls back to a blended rate for drives that
		// happened before the first known charge.
		priced := loadPricedCharges(r.Context(), chargesStore, cfg)
		fallbackRate, fallbackCur := computeBlendedRate(priced, cfg)
		decorated := make([]driveResponse, 0, len(out))
		for _, d := range out {
			rate, cur := rateForDrive(d, priced, fallbackRate, fallbackCur)
			decorated = append(decorated, decorateDrive(d, rate, cur))
		}
		writeJSON(w, http.StatusOK, decorated)
	}
}

// handleDriveGet serves one drive with its route polyline and the same
// cost estimate the list carries. 404 when the user has no such drive.
func handleDriveGet(store *drives.Store, chargesStore *charges.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		d, err := store.Get(r.Context(), strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if d == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if !histwin.Allows(r.Context(), d.StartedAt) {
			writeOutsideFreeWindow(w)
			return
		}
		cfg, _ := settings.GetChargingConfig(r.Context(), settingsStore)
		priced := loadPricedCharges(r.Context(), chargesStore, cfg)
		fallbackRate, fallbackCur := computeBlendedRate(priced, cfg)
		rate, cur := rateForDrive(*d, priced, fallbackRate, fallbackCur)
		writeJSON(w, http.StatusOK, decorateDrive(*d, rate, cur))
	}
}

// driveResponse is the wire shape for /api/drives: the stored drive
// row plus a locally-computed cost estimate based on the most recent
// charge that ended before the drive started (with a blended-rate
// fallback for drives that predate the first known charge).
type driveResponse struct {
	drives.Drive
	EstimatedCost     float64 `json:"estimated_cost,omitempty"`
	EstimatedCurrency string  `json:"estimated_currency,omitempty"`
	// EstimatedPricePerKWh is the rate used to compute EstimatedCost
	// — sourced from the most recent prior charge (or a blended
	// fallback for drives that predate the first known charge).
	// Surfaced so the UI can render "~$5.23 at $0.14/kWh" instead
	// of treating the cost as a hard number.
	EstimatedPricePerKWh float64 `json:"estimated_price_per_kwh,omitempty"`
}

func decorateDrive(d drives.Drive, rate float64, cur string) driveResponse {
	resp := driveResponse{Drive: d}
	if rate > 0 && d.EnergyUsedKWh > 0 {
		resp.EstimatedCost = rate * d.EnergyUsedKWh
		resp.EstimatedCurrency = cur
		resp.EstimatedPricePerKWh = rate
	}
	return resp
}

// pricedCharge is a normalized view of a charge row used for drive
// cost lookup: ended-at + a usable per-kWh rate + currency. Rows
// without a usable rate are skipped at load time.
type pricedCharge struct {
	endedAt time.Time
	rate    float64
	cur     string
}

// loadPricedCharges fetches every charge for the user, derives a
// per-kWh rate (persisted PricePerKWh, or persisted Cost / Energy,
// or the configured home rate as fallback), and returns the slice
// sorted ascending by EndedAt. Empty slice on store errors.
func loadPricedCharges(ctx context.Context, store *charges.Store, cfg settings.ChargingConfig) []pricedCharge {
	if store == nil {
		return nil
	}
	rows, err := store.ListAllListed(ctx)
	if err != nil {
		return nil
	}
	out := make([]pricedCharge, 0, len(rows))
	for _, c := range rows {
		if c.EnergyAddedKWh <= 0 {
			continue
		}
		rate, cur := chargeRate(c, cfg)
		if rate <= 0 {
			continue
		}
		out = append(out, pricedCharge{endedAt: c.EndedAt, rate: rate, cur: cur})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].endedAt.Before(out[j].endedAt) })
	return out
}

// chargeRate picks the best $/kWh for a single charge row. Persisted
// PricePerKWh (set when Rivian or the user's configured home rate
// stamped the row at close time) wins. If only Cost is set, derive
// rate from Cost/Energy. Otherwise fall back to the current home
// rate so legacy / unpriced rows still contribute a sensible value.
func chargeRate(c charges.Charge, cfg settings.ChargingConfig) (float64, string) {
	if c.PricePerKWh > 0 {
		return c.PricePerKWh, c.Currency
	}
	if c.Cost > 0 && c.EnergyAddedKWh > 0 {
		return c.Cost / c.EnergyAddedKWh, c.Currency
	}
	if cfg.HomePricePerKWh > 0 {
		return cfg.HomePricePerKWh, cfg.HomeCurrency
	}
	return 0, ""
}

// rateForDrive looks up the most recent charge that ended at or
// before d.StartedAt. Returns the fallback when the drive predates
// every known charge.
func rateForDrive(d drives.Drive, priced []pricedCharge, fallbackRate float64, fallbackCur string) (float64, string) {
	if len(priced) == 0 {
		return fallbackRate, fallbackCur
	}
	// sort.Search returns the smallest index where endedAt > drive
	// start; the most recent charge that ended before is at idx-1.
	start := d.StartedAt
	idx := sort.Search(len(priced), func(i int) bool {
		return priced[i].endedAt.After(start)
	})
	if idx == 0 {
		return fallbackRate, fallbackCur
	}
	pc := priced[idx-1]
	return pc.rate, pc.cur
}

// computeBlendedRate returns Σ(cost) / Σ(energy) across every priced
// charge plus the dominant currency. Used as the fallback rate for
// drives that predate the first known charge.
func computeBlendedRate(priced []pricedCharge, cfg settings.ChargingConfig) (float64, string) {
	if len(priced) == 0 {
		return cfg.HomePricePerKWh, cfg.HomeCurrency
	}
	var totalCost, totalEnergy float64
	currencies := map[string]float64{}
	// We only have rate + endedAt here, not energy, so weight every
	// session equally. That's fine — this is just the fallback for
	// pre-first-charge drives.
	for _, pc := range priced {
		totalCost += pc.rate
		totalEnergy += 1
		currencies[pc.cur]++
	}
	if totalEnergy <= 0 {
		return cfg.HomePricePerKWh, cfg.HomeCurrency
	}
	dominant := cfg.HomeCurrency
	var top float64
	for cur, n := range currencies {
		if n > top {
			top = n
			dominant = cur
		}
	}
	return totalCost / totalEnergy, dominant
}

// handleSamples serves raw vehicle_state rows newer than ?since=<rfc3339>
// (default: 24h ago), capped at ?limit= (default 1000, max 10000).
// A window that is bounded and at most 7 days wide is bucketed when it exceeds the
// cap (X-Rivolt-Downsampled); a wider or unbounded one returns the oldest rows and
// says so with X-Rivolt-Samples-Truncated.
// Optional ?until=<rfc3339> bounds the upper end so callers like the
// drive detail page don't pull every post-drive sample through now.
// Optional ?vehicle=<rivian vehicle id> scopes to one truck (the one the
// drive or charge belongs to); without it every vehicle of the user is
// returned, as before. An id the user doesn't own yields an empty list.
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
		var until time.Time
		if s := r.URL.Query().Get("until"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				until = t
			}
		}
		// A free account's range is clamped to its window, whatever the client asked for.
		since = histwin.Clamp(r.Context(), since)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		// A window over the cap is bucketed across its whole span (peaks
		// kept) rather than truncated to the oldest rows. The body stays
		// a plain array; the flag is in headers so existing clients keep
		// working.
		vehicle, ok := limitFreeVehicle(w, r.Context(), strings.TrimSpace(r.URL.Query().Get("vehicle")))
		if !ok {
			return
		}
		res, err := store.ListBetweenDownsampledVehicle(r.Context(), vehicle, since, until, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := res.Samples
		if out == nil {
			out = []samples.Sample{}
		}
		if res.Reduced {
			w.Header().Set("X-Rivolt-Downsampled", "true")
			w.Header().Set("X-Rivolt-Samples-Total", strconv.Itoa(res.Total))
		}
		if res.Truncated {
			// An unbounded or over-7-day window is not bucketed: these are the
			// oldest rows up to the cap, and the newer ones are missing.
			w.Header().Set("X-Rivolt-Samples-Truncated", "true")
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// sleepActivityTTL is how long a computed sleep-activity series is
// reused. The query window-scans up to 30 days of vehicle_state (prod
// p95 was several seconds), and the series only changes as new samples
// arrive, so a few minutes of staleness is invisible.
const sleepActivityTTL = 5 * time.Minute

var sleepActivityCache = newTTLCache[[]samples.DayActivity](sleepActivityTTL, 256)

// sleepActivityKey is the cache key for one request: the window is
// rounded down to the hour (since) and 5 minutes (until).
func sleepActivityKey(uid uuid.UUID, vehicle string, since, until time.Time) string {
	return strings.Join([]string{uid.String(), vehicle,
		since.UTC().Truncate(time.Hour).Format(time.RFC3339),
		until.UTC().Truncate(5 * time.Minute).Format(time.RFC3339)}, "|")
}

// handleSleepActivity returns per-day asleep/awake hours derived from the
// persisted power_state column (migration 0038). Query params: `since`
// (RFC3339, default 30d ago), `until` (optional), `vehicle` (optional
// Rivian vehicle id to scope to one car). The series only has data from
// the deploy that started recording power_state forward.
//
// Results are cached per (user, vehicle, window) for sleepActivityTTL,
// per process. The window is part of the key rounded down to the hour
// (since) and 5 minutes (until), because clients send "now minus N days"
// and so never repeat an exact timestamp; the first caller in a bucket
// computes with its exact bounds.
func handleSleepActivity(uid uuid.UUID, store *samples.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		since := time.Now().Add(-30 * 24 * time.Hour)
		if s := r.URL.Query().Get("since"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				since = t
			}
		}
		until := time.Now()
		if s := r.URL.Query().Get("until"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				until = t
			}
		}
		vehicle, ok := limitFreeVehicle(w, r.Context(), r.URL.Query().Get("vehicle"))
		if !ok {
			return
		}
		// Clamped to a free account's window BEFORE the key is made: the cache holds a series for exactly the range in its key, so a
		// windowed request and a Pro one can never be served each other's series.
		since = histwin.Clamp(r.Context(), since)
		key := sleepActivityKey(uid, vehicle, since, until)
		out, err := sleepActivityCache.get(key, func() ([]samples.DayActivity, error) {
			// Shared with concurrent callers, so it must not die with
			// this request's context.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 25*time.Second)
			defer cancel()
			res, err := store.SleepActivity(ctx, vehicle, since, until)
			if res == nil {
				res = []samples.DayActivity{}
			}
			return res, err
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// maxThumbIDs is the most drives one thumbnail request names.
const maxThumbIDs = 60

// handleRouteThumbs serves the drive list's route thumbnails: for each of up to maxThumbIDs of the caller's own drives (?ids=a,b,c), its
// stored route cut to a few points as a polyline, so the list can draw each drive's shape as a tiny SVG with no map and no tiles. An id the
// caller does not have, that has no route, or that lies outside their plan's history window or vehicle is simply absent from the answer.
// The points are not logged, and nothing is stored: the route is read, simplified and sent.
func handleRouteThumbs(store *drives.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vehicleID, ok := vehicleFilter(w, r)
		if !ok {
			return
		}
		var ids []string
		seen := map[string]bool{}
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			if !validDriveID(id) {
				http.Error(w, "ids has an unexpected id", http.StatusBadRequest)
				return
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if len(ids) > maxThumbIDs {
			http.Error(w, "too many ids", http.StatusBadRequest)
			return
		}
		if store == nil || len(ids) == 0 {
			writeJSON(w, http.StatusOK, map[string]string{})
			return
		}
		stubs, err := store.RoutesByIDs(r.Context(), ids, vehicleID)
		if err != nil {
			http.Error(w, "could not read the routes", http.StatusInternalServerError)
			return
		}
		stubs = windowRows(r.Context(), stubs, func(s drives.RouteStub) time.Time { return s.StartedAt })
		out := make(map[string]string, len(stubs))
		for _, s := range stubs {
			if t := drives.ThumbPolyline(s.RoutePolyline); t != "" {
				out[s.ID] = t
			}
		}
		// The routes change only when a drive is edited; a few minutes of private caching saves the list its re-asks.
		w.Header().Set("Cache-Control", "private, max-age=300")
		writeJSON(w, http.StatusOK, out)
	}
}

// validDriveID: a drive's external id is short and plain (a uuid, or "live_" and the like); anything else is refused before it reaches a query.
func validDriveID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c > unicode.MaxASCII || !(unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_' || c == ':' || c == '.') {
			return false
		}
	}
	return true
}
