package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
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
		out, err := store.ListRecent(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Drop ignition-cycle / gear-bounce rows so the SPA list
		// shows only real travel. The rows still exist in DB.
		out = drives.FilterReparking(out)
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
	rows, err := store.ListAll(ctx)
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
// Optional ?until=<rfc3339> bounds the upper end so callers like the
// drive detail page don't pull every post-drive sample through now.
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
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := store.ListBetween(r.Context(), since, until, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []samples.Sample{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleSleepActivity returns per-day asleep/awake hours derived from the
// persisted power_state column (migration 0038). Query params: `since`
// (RFC3339, default 30d ago), `until` (optional), `vehicle` (optional
// Rivian vehicle id to scope to one car). The series only has data from
// the deploy that started recording power_state forward.
func handleSleepActivity(store *samples.Store) http.HandlerFunc {
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
		out, err := store.SleepActivity(r.Context(), r.URL.Query().Get("vehicle"), since, until)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []samples.DayActivity{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}
