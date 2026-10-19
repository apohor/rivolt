package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/analytics"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/ledger"
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
		decorated := decorateCharges(r.Context(), out, settingsStore)
		writeJSON(w, http.StatusOK, decorated)
	}
}

// decorateCharges turns stored charges into their wire shape: the
// classification the Money tab uses plus the legacy local estimate.
func decorateCharges(ctx context.Context, out []charges.Charge, settingsStore *settings.Store) []chargeResponse {
	cfg, _ := settings.GetChargingConfig(ctx, settingsStore)
	home, _ := settings.GetHomeLocation(ctx, settingsStore)
	homeLookup := func() (float64, float64, bool) { return home.Latitude, home.Longitude, home.Set }
	// Without the user's charging settings (home rate, home
	// location) the classification would be wrong (home-rate rows
	// read as billed, every place unknown), so leave it out.
	lcfg, cerr := ledgerConfig(ctx, settingsStore)
	var classes []ledger.Classification
	if cerr == nil {
		rows := make([]ledger.Charge, len(out))
		for i, c := range out {
			rows[i] = ledger.FromCharge(c)
		}
		classes = ledger.ClassifyAll(rows, lcfg)
	}
	decorated := make([]chargeResponse, 0, len(out))
	for i, c := range out {
		resp := decorateCharge(c, cfg, homeLookup)
		if classes != nil {
			cl := classes[i]
			resp.Classification = &cl
		} else {
			resp.ClassificationUnavailable = true
		}
		decorated = append(decorated, resp)
	}
	return decorated
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
	SetNetworkPrice(ctx context.Context, externalID, network, currency string, pricePerKWh float64) (int64, error)
	GetByExternalID(ctx context.Context, externalID string) (*charges.Charge, error)
}

// handlePatchChargePricing accepts {cost?, currency?, price_per_kwh?}
// and overwrites those three columns on the matching charge. Any
// missing/zero field clears its column, letting the API-layer
// fallbacks (recent-charge rate, home rate) take over again on the
// next read. {"free": true[, "currency"]} instead records an explicit
// $0 price (cost_source=user), which a zero cost can't express because
// zero means "clear". Returns 204/404/400/500.
func handlePatchChargePricing(store *charges.Store, settingsStore *settings.Store) http.HandlerFunc {
	if store == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "charges disabled", http.StatusServiceUnavailable)
		}
	}
	return patchChargePricing(store, func(ctx context.Context) []dcfcrates.NetworkOverride {
		if settingsStore == nil {
			return nil
		}
		nets, err := settings.GetChargingNetworks(ctx, settingsStore)
		if err != nil {
			return nil
		}
		return settings.AsOverrides(nets)
	})
}

// networkCurrency is the currency network rates are quoted in (the
// DCFC rate table is USD; same as the close-time pricer).
const networkCurrency = "USD"

// chargeStaleAfter is how long an open-looking live row may go without a
// sample before it counts as ended. Same rule as the web's isActiveCharge:
// a row left open by a crash must stay editable.
const chargeStaleAfter = 30 * time.Minute

// chargeInProgress reports whether c is a live session still being
// recorded: an open final_state, from the live recorder, whose last
// update is recent.
func chargeInProgress(c charges.Charge, now time.Time) bool {
	return charges.IsOpenLiveFinalState(c.FinalState) && c.Source == "live" &&
		now.Sub(c.EndedAt) <= chargeStaleAfter
}

func patchChargePricing(store chargePricer, overrides func(context.Context) []dcfcrates.NetworkOverride) http.HandlerFunc {
	type body struct {
		// NetworkSlug re-prices at the user's rate for that network
		// (price book override, member rate if active, else guest).
		NetworkSlug *string  `json:"network_slug"`
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
		// A session still in progress has no final energy: a price set now
		// would be frozen at the energy so far (the recorder keeps a
		// user's price). Wait for it to end.
		cur0, err := store.GetByExternalID(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cur0 == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if chargeInProgress(*cur0, time.Now()) {
			http.Error(w, "charge still in progress", http.StatusConflict)
			return
		}
		if b.NetworkSlug != nil {
			if b.Cost != nil || b.PricePerKWh != nil || (b.Free != nil && *b.Free) {
				http.Error(w, "network_slug cannot be combined with cost, price_per_kwh or free", http.StatusBadRequest)
				return
			}
			var ov []dcfcrates.NetworkOverride
			if overrides != nil {
				ov = overrides(r.Context())
			}
			rr, ok := dcfcrates.ResolveRateBySlug(strings.TrimSpace(*b.NetworkSlug), ov)
			if !ok || rr.UserRate <= 0 {
				http.Error(w, "unknown network or no rate for it", http.StatusBadRequest)
				return
			}
			n, err := store.SetNetworkPrice(r.Context(), id, rr.DisplayName, networkCurrency, rr.UserRate)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if n == 0 {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
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

// networkConfirmer is the slice of *charges.Store the confirm handler
// uses.
type networkConfirmer interface {
	ConfirmNetwork(ctx context.Context, externalID string) (int64, error)
	GetByExternalID(ctx context.Context, externalID string) (*charges.Charge, error)
}

// handleConfirmChargeNetwork serves POST /api/charges/{id}/pricing/confirm:
// the user agrees with the auto-detected network. 200 with the charge;
// idempotent (an already-confirmed estimate answers 200 again); 409 when
// there is no estimate to confirm (user-priced, Rivian-billed, or nothing
// matched); 404 for an unknown charge.
func handleConfirmChargeNetwork(store networkConfirmer, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		n, err := store.ConfirmNetwork(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		c, err := store.GetByExternalID(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if c == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if n == 0 && c.NetworkState != "confirmed" {
			http.Error(w, "no auto-detected network estimate to confirm", http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, decorateCharges(r.Context(), []charges.Charge{*c}, settingsStore)[0])
	}
}

// handleGetCharge serves GET /api/charges/{id}.
func handleGetCharge(store networkConfirmer, settingsStore *settings.Store, reduced reducedLookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := store.GetByExternalID(r.Context(), strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if c == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		resp := decorateCharges(r.Context(), []charges.Charge{*c}, settingsStore)[0]
		resp.SamplesReduced = reduced.covers(r.Context(), c.VehicleID, c.StartedAt, c.EndedAt)
		writeJSON(w, http.StatusOK, resp)
	}
}

// chargeResponse is the wire shape for /api/charges: the stored
// charge row plus a locally-computed estimated cost when the
// user has set a home $/kWh rate. Cost is only attached when
// both the rate and the observed energy are non-zero.
type chargeResponse struct {
	charges.Charge
	// SamplesReduced: the charge's month of raw telemetry is shown from reduced
	// samples (thinned). Absent when not.
	SamplesReduced bool `json:"samples_reduced,omitempty"`
	// The ledger's classification (cost_source_effective, kind, place,
	// ledger_flags, ...): the same rules as the Money tab, so the
	// Charges list and charge page don't re-derive them.
	// nil (fields omitted) when the settings couldn't be read; see
	// ClassificationUnavailable.
	*ledger.Classification
	ClassificationUnavailable bool    `json:"classification_unavailable,omitempty"`
	EstimatedCost             float64 `json:"estimated_cost,omitempty"`
	EstimatedCurrency         string  `json:"estimated_currency,omitempty"`
	// DC fast charge auto-detection's verify record (contract v1):
	// null unless the charge was auto-detected. Snake-case twins of
	// the embedded Charge's NetworkState/-Confidence/-Evidence.
	NetworkStateOut      *string         `json:"network_state"`
	NetworkConfidenceOut *float64        `json:"network_confidence"`
	NetworkEvidenceOut   json.RawMessage `json:"network_evidence"`
}

func decorateCharge(c charges.Charge, cfg settings.ChargingConfig, home rivian.HomeLookup) chargeResponse {
	resp := chargeResponse{Charge: c, NetworkEvidenceOut: json.RawMessage("null")}
	if c.NetworkState != "" {
		st := c.NetworkState
		resp.NetworkStateOut = &st
		resp.NetworkConfidenceOut = c.NetworkConfidence
		if len(c.NetworkEvidence) > 0 {
			resp.NetworkEvidenceOut = c.NetworkEvidence
		}
	}
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
