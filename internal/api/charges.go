package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/analytics"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

func handleCharges(store *charges.Store, settingsStore *settings.Store) http.HandlerFunc {
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
		cfg, _ := settings.GetChargingConfig(r.Context(), settingsStore)
		home, _ := settings.GetHomeLocation(r.Context(), settingsStore)
		homeLookup := func() (float64, float64, bool) { return home.Latitude, home.Longitude, home.Set }
		decorated := make([]chargeResponse, 0, len(out))
		for _, c := range out {
			decorated = append(decorated, decorateCharge(c, cfg, homeLookup))
		}
		writeJSON(w, http.StatusOK, decorated)
	}
}

// handleDeleteCharge removes a single charge row by external ID,
// scoped to the authenticated user. 204 on success, 404 if no row
// matched, 500 on a DB error. The store filters by user_id so a
// caller can't reach into another user's data.
func handleDeleteCharge(store *charges.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "charges disabled", http.StatusServiceUnavailable)
			return
		}
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		n, err := store.DeleteByExternalID(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n == 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// chargePricer is the slice of *charges.Store the pricing handler
// uses, so it can be tested without Postgres.
type chargePricer interface {
	UpdatePricing(ctx context.Context, externalID string, cost float64, currency string, pricePerKWh float64) (int64, error)
	MarkFree(ctx context.Context, externalID, currency string) (int64, error)
}

// handlePatchChargePricing accepts {cost?, currency?, price_per_kwh?}
// and overwrites those three columns on the matching charge. Any
// missing/zero field clears its column, letting the API-layer
// fallbacks (recent-charge rate, home rate) take over again on the
// next read. {"free": true[, "currency"]} instead records an explicit
// $0 price (cost_source=user), which a zero cost can't express because
// zero means "clear". Returns 204/404/400/500.
func handlePatchChargePricing(store *charges.Store) http.HandlerFunc {
	if store == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "charges disabled", http.StatusServiceUnavailable)
		}
	}
	return patchChargePricing(store)
}

func patchChargePricing(store chargePricer) http.HandlerFunc {
	type body struct {
		Cost        *float64 `json:"cost"`
		Currency    *string  `json:"currency"`
		PricePerKWh *float64 `json:"price_per_kwh"`
		Free        *bool    `json:"free"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		var b body
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		var cost, ppk float64
		var cur string
		if b.Cost != nil {
			cost = *b.Cost
		}
		if b.PricePerKWh != nil {
			ppk = *b.PricePerKWh
		}
		if b.Currency != nil {
			cur = strings.ToUpper(strings.TrimSpace(*b.Currency))
		}
		// Reject negatives — the column is unsigned in spirit even
		// though Postgres NUMERIC is signed.
		if cost < 0 || ppk < 0 {
			http.Error(w, "values must be non-negative", http.StatusBadRequest)
			return
		}
		var n int64
		var err error
		if b.Free != nil && *b.Free {
			if cost != 0 || ppk != 0 {
				http.Error(w, "free cannot be combined with cost or price_per_kwh", http.StatusBadRequest)
				return
			}
			n, err = store.MarkFree(r.Context(), id, cur)
		} else {
			n, err = store.UpdatePricing(r.Context(), id, cost, cur, ppk)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n == 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// chargeResponse is the wire shape for /api/charges: the stored
// charge row plus a locally-computed estimated cost when the
// user has set a home $/kWh rate. Cost is only attached when
// both the rate and the observed energy are non-zero.
type chargeResponse struct {
	charges.Charge
	EstimatedCost     float64 `json:"estimated_cost,omitempty"`
	EstimatedCurrency string  `json:"estimated_currency,omitempty"`
}

func decorateCharge(c charges.Charge, cfg settings.ChargingConfig, home rivian.HomeLookup) chargeResponse {
	resp := chargeResponse{Charge: c}
	// Persisted cost wins: it was snapshotted at the rate in effect
	// when the session closed. Only fall back to the current rate
	// for legacy rows (imports, pre-v0.3.29 live) that have no
	// persisted cost.
	// A user-entered price wins too — including an explicit "free"
	// ($0), which must not pick up a home-rate estimate.
	if c.Cost > 0 || c.CostSource == "user" {
		return resp
	}
	// An unpriced DC fast charge is unknown, not home-priced: that
	// estimate was the bug migration 0040 fixed on the write path.
	if rivian.IsDCFCSession(c.MaxPowerKW, c.EnergyAddedKWh, c.ActiveSeconds, c.EndedAt.Sub(c.StartedAt)) {
		return resp
	}
	// Nor is AC charging away from home: unknown until the user prices
	// it (owner decision 2026-10-13), same as the recorder and ledger.
	if rivian.AwayFromHome(c.Lat, c.Lon, home) {
		return resp
	}
	if cfg.HomePricePerKWh > 0 && c.EnergyAddedKWh > 0 {
		resp.EstimatedCost = cfg.HomePricePerKWh * c.EnergyAddedKWh
		resp.EstimatedCurrency = cfg.HomeCurrency
	}
	return resp
}

type chargeClusterResponse struct {
	Label       string   `json:"label"`
	Lat         float64  `json:"lat"`
	Lon         float64  `json:"lon"`
	Sessions    int      `json:"sessions"`
	EnergyKWh   float64  `json:"energy_kwh"`
	RadiusMeter float64  `json:"radius_m"`
	MemberIDs   []string `json:"member_ids"`
}

func handleChargeClusters(store *charges.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []chargeClusterResponse{})
			return
		}
		// Pull the full usable window — clustering is cheap and the
		// store caps list size anyway. A bigger corpus just gives
		// better Home detection.
		rows, err := store.ListRecent(r.Context(), 5000)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pts := make([]analytics.ChargePoint, 0, len(rows))
		for _, c := range rows {
			pts = append(pts, analytics.ChargePoint{
				ID:             c.ID,
				Lat:            c.Lat,
				Lon:            c.Lon,
				EnergyAddedKWh: c.EnergyAddedKWh,
				// Peak kW drives the Home/Public/Fast split: anything
				// >=50 kW is DCFC regardless of location. Zero means
				// unknown peak and falls through to location clustering.
				MaxPowerKW: c.MaxPowerKW,
			})
		}
		clusters := analytics.ClusterCharges(pts, analytics.DefaultParams())
		out := make([]chargeClusterResponse, 0, len(clusters))
		for _, c := range clusters {
			out = append(out, chargeClusterResponse{
				Label:       string(c.Label),
				Lat:         c.Centroid.Lat,
				Lon:         c.Centroid.Lon,
				Sessions:    c.Sessions,
				EnergyKWh:   c.EnergyKWh,
				RadiusMeter: c.RadiusMeter,
				MemberIDs:   c.MemberIDs,
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// --- AI settings ----------------------------------------------------------
//
// Thin wrappers around settings.Manager so the Settings UI can configure
// which LLM provider Rivolt uses for AI features (weekly digest, trip
// planner, anomaly explanations). The manager enforces the redaction
// contract: API keys are reported as has_key=true/false, never echoed back.
