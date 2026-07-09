package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// handleLiveSession returns the current charging session snapshot.
// Prefers the cached payload from the StateMonitor (populated by
// both the WebSocket ChargingSession subscription and the REST
// getLiveSessionHistory poller), falling back to a direct REST hit
// if nothing has been cached yet. The monitor cache is what carries
// home AC / L2 telemetry — REST alone returns active:false with a
// zeroed payload for those sessions.
//
// The response is decorated with an estimated_cost field computed
// from the user's configured home $/kWh rate. For sessions Rivian
// reports as free (home AC, L2 on non-RAN chargers) this is the
// only signal of what the charge cost.
func handleLiveSession(c rivian.Client, mon *rivian.StateMonitor, store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		cfg, _ := settings.GetChargingConfig(r.Context(), store)
		if mon != nil {
			if sess := mon.LatestLiveSession(id); sess != nil {
				writeJSON(w, http.StatusOK, decorateLiveSession(sess, cfg))
				return
			}
		}
		sess, err := lc.LiveSession(r.Context(), id)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, decorateLiveSession(sess, cfg))
	}
}

// handleLiveDrive returns a snapshot of the in-flight drive session
// for a vehicle, or 204 when none is active. Analogous to
// handleLiveSession for charges. The monitor is the sole source of
// truth — there's no REST fallback because Rivian exposes no drive
// equivalent of getLiveSessionHistory, and the snapshot is derived
// entirely from locally-observed telemetry frames.
func handleLiveDrive(mon *rivian.StateMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		if mon == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		drive := mon.ActiveDrive(id)
		if drive == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, drive)
	}
}

// liveSessionResponse is the wire shape for /api/live-session/:id —
// the base LiveSession plus locally-computed estimated cost when the
// user has set a home $/kWh rate and the Rivian-reported price
// is absent.
type liveSessionResponse struct {
	*rivian.LiveSession
	EstimatedCost     float64 `json:"estimated_cost,omitempty"`
	EstimatedCurrency string  `json:"estimated_currency,omitempty"`
}

func decorateLiveSession(sess *rivian.LiveSession, cfg settings.ChargingConfig) liveSessionResponse {
	resp := liveSessionResponse{LiveSession: sess}
	if sess == nil {
		return resp
	}
	// Only compute when we have both a configured rate and observed
	// energy. Don't overwrite a Rivian-reported price — those come
	// from RAN / Wall Charger sessions where the real billing rate
	// is authoritative.
	if cfg.HomePricePerKWh > 0 && sess.TotalChargedEnergyKWh > 0 && sess.CurrentPrice == "" {
		resp.EstimatedCost = cfg.HomePricePerKWh * sess.TotalChargedEnergyKWh
		resp.EstimatedCurrency = cfg.HomeCurrency
	}
	return resp
}

// handleChargingSchemaProbe introspects the chrg/user/graphql
// endpoint and returns the list of query fields + their args. Used
// when upstream renames a field (e.g. getLiveSessionData →
// getSessionStatus) to discover the new shape without deploying a
// blind guess.
func handleChargingSchemaProbe(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		data, err := lc.ChargingSchemaProbe(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, data)
	}
}

// handleBatteryTempProbe subscribes to the Parallax
// energy.high_voltage.battery_state topic for the vehicle, decodes the
// first frame's pack cell temperatures, and returns them. Proof-of-
// concept for wiring battery pack temperature into Rivolt; the vehicle
// must be awake for a frame to arrive.
func handleBatteryTempProbe(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		vid := chi.URLParam(r, "vehicleID")
		bt, err := lc.ProbeBatteryTemperature(r.Context(), vid)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, bt)
	}
}

// handleChargingFieldProbe fires a deliberately wrong query for the
// named charging-endpoint field and returns Rivian's validation
// error, which lists the required args and subfields. ?vehicleID=...
// opts into passing a vehicleId argument.
func handleChargingFieldProbe(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		field := chi.URLParam(r, "field")
		vid := r.URL.Query().Get("vehicleID")
		sel := r.URL.Query().Get("sel")
		data, err := lc.ChargingFieldProbeWithSelection(r.Context(), field, vid, sel)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, data)
	}
}

// handleChargingFrames returns the ring buffer of recent raw
// ChargingSession WS frames. Filter with ?vehicleID=... for a
// specific vehicle.
func handleChargingFrames(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, lc.RecentChargingFrames(r.URL.Query().Get("vehicleID")))
	}
}
