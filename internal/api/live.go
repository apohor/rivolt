package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

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
	return handleLiveSessionForwarded(c, mon, store, nil)
}

// handleLiveSessionForwarded is handleLiveSession with the read routed to the group owner's monitor when this pod holds no tokens for the
// account (RIVOLT_NO_STANDING_TOKENS): every api pod is such a pod, and without the forward the live panel's session totals would be blank.
func handleLiveSessionForwarded(c rivian.Client, mon *rivian.StateMonitor, store *settings.Store, sf *stateForward) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		unheld := rivian.IsUnheld(c) // single-owner M4-5: no tokens here, so only this pod's monitor can answer, as before the tokens were dropped
		if (!ok || lc == nil) && !unheld {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		cfg, _ := settings.GetChargingConfig(r.Context(), store)
		if unheld && sf != nil {
			wire, err := sf.forwardLiveSession(r.Context(), c, id, func() (liveSessionWire, error) {
				if mon == nil {
					return liveSessionWire{}, errOwnerUnavailable
				}
				sess, now := mon.LiveSessionForCharge(id)
				return wireOf(sess, now), nil
			})
			if err != nil {
				writeUnheld(w)
				return
			}
			writeJSON(w, http.StatusOK, wire.response(id, cfg))
			return
		}
		if mon != nil {
			// Only a session that belongs to the open charge, and the charge's own recorded energy for the estimate: the
			// cache can hold the previous plug-in's session, whose total would price a charge that has just begun at a
			// whole earlier charge's cost (and whose power would beat the truck's own).
			sess, now := mon.LiveSessionForCharge(id)
			writeJSON(w, http.StatusOK, decorateLiveSessionFor(id, sess, now, cfg))
			return
		}
		if unheld {
			writeUnheld(w)
			return
		}
		sess, err := lc.LiveSession(r.Context(), id)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, decorateLiveSession(sess, fallbackPeaks.note(sess), cfg))
	}
}

// handleLiveDrive returns a snapshot of the in-flight drive session
// for a vehicle, or 204 when none is active. Analogous to
// handleLiveSession for charges. The monitor is the sole source of
// truth — there's no REST fallback because Rivian exposes no drive
// equivalent of getLiveSessionHistory, and the snapshot is derived
// entirely from locally-observed telemetry frames.
func handleLiveDrive(c rivian.Client, mon *rivian.StateMonitor, sf *stateForward) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		var drive *rivian.LiveDrive
		if rivian.IsUnheld(c) && sf != nil && sf.fw != nil && sf.groupOf != nil {
			// No tokens and so no stream here: the group's owner holds the drive (this pod answers itself only when it is the owner).
			wire, err := sf.forwardLiveDrive(r.Context(), id)
			if err != nil {
				writeUnheld(w)
				return
			}
			drive = wire.Drive
		} else if mon != nil {
			drive = mon.ActiveDrive(id)
		}
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

// liveSessionIsFree is true when Rivian's own active session says the charge costs nothing and we can believe it: the truck's charger is a Rivian one,
// or the charge is one the home rate does not fit (a fast charge, or AC away from home). Rivian marks a home or third-party charge it does not bill as
// "free" too, and that is not a free charge: the home rate prices it.
func liveSessionIsFree(sess *rivian.LiveSession, now rivian.ChargeNow) bool {
	return sess.Active && sess.IsFreeSession && (sess.IsRivianCharger || !now.HomeRateApplies())
}

// liveUnpriced is true when the session has no price of its own to show: not a free session (that has its answer, and no estimate is made for
// it), and a price that is empty or not above zero (the feed sends "0" before it has priced a session).
func liveUnpriced(sess *rivian.LiveSession, now rivian.ChargeNow) bool {
	if liveSessionIsFree(sess, now) {
		return false
	}
	p, err := strconv.ParseFloat(strings.TrimSpace(sess.CurrentPrice), 64)
	return err != nil || p <= 0
}

// decorateLiveSessionFor is the response for the open charge: the session when it is the charge's, else the zeroed
// payload Rivian itself returns for a home charge (so a client falls back to the truck's own power and shows no session
// totals), and an estimated cost from the charge's recorded energy when Rivian reported no price and the home rate is a
// fair price for it (an AC charge not known to be away from home): a fast charge on another network has a price of its own.
func decorateLiveSessionFor(vehicleID string, sess *rivian.LiveSession, now rivian.ChargeNow, cfg settings.ChargingConfig) liveSessionResponse {
	if sess == nil {
		sess = &rivian.LiveSession{VehicleID: vehicleID}
	}
	resp := liveSessionResponse{LiveSession: sess}
	if cfg.HomePricePerKWh > 0 && now.EnergyKWh > 0 && liveUnpriced(sess, now) && now.HomeRateApplies() {
		resp.EstimatedCost = cfg.HomePricePerKWh * now.EnergyKWh
		resp.EstimatedCurrency = cfg.HomeCurrency
	}
	return resp
}

// sessionPeaks remembers the highest power each vehicle's session has shown, for the path with no state monitor: the home rate is a fair price
// for an AC charge, and one reading at the end of a fast charge's taper (or at a pause) looks like AC. A new session starts over: a different
// non-empty StartTime (an empty one is a frame without it, the same session, as the monitor reads it), the energy falling below what it was, or
// the session going inactive. The peak lives in this process's memory: self-hosted runs one, and a restart falls back to the current power.
type sessionPeaks struct {
	mu sync.Mutex
	m  map[string]sessionPeak
}

type sessionPeak struct {
	start  string
	kw     float64
	energy float64
}

var fallbackPeaks sessionPeaks

// note records the session's power and returns the peak so far for its session (0 for no session).
func (p *sessionPeaks) note(sess *rivian.LiveSession) float64 {
	if sess == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]sessionPeak{}
	}
	cur := p.m[sess.VehicleID]
	if (sess.StartTime != "" && cur.start != "" && sess.StartTime != cur.start) || sess.TotalChargedEnergyKWh < cur.energy || !sess.Active && cur.kw > 0 && sess.PowerKW == 0 && sess.TotalChargedEnergyKWh == 0 {
		cur = sessionPeak{}
	}
	if sess.StartTime != "" {
		cur.start = sess.StartTime
	}
	cur.energy = sess.TotalChargedEnergyKWh
	if sess.PowerKW > cur.kw {
		cur.kw = sess.PowerKW
	}
	p.m[sess.VehicleID] = cur
	return cur.kw
}

// decorateLiveSession is the response on the path with no state monitor; peakKW is the session's highest power seen so far (see sessionPeaks).
func decorateLiveSession(sess *rivian.LiveSession, peakKW float64, cfg settings.ChargingConfig) liveSessionResponse {
	resp := liveSessionResponse{LiveSession: sess}
	if sess == nil {
		return resp
	}
	// Only compute when we have both a configured rate and observed
	// energy, and the session's power is an AC charge's (a fast charge on
	// another network has a price of its own; this path has no place). Don't overwrite a Rivian-reported price — those come
	// from RAN / Wall Charger sessions where the real billing rate
	// is authoritative.
	if cfg.HomePricePerKWh > 0 && sess.TotalChargedEnergyKWh > 0 && liveUnpriced(sess, rivian.ChargeNow{PeakKW: max(peakKW, sess.PowerKW)}) && rivian.HomeRateFitsPower(max(peakKW, sess.PowerKW)) {
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

// handleParallaxRawProbe captures raw Parallax frames for the given RVM
// topics, for reverse-engineering a topic's protobuf payload from live
// data. ?rvms=a,b,c (default dynamics.vehicle.gnss); ?max=N frames.
func handleParallaxRawProbe(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		vid := chi.URLParam(r, "vehicleID")
		rvms := []string{"dynamics.vehicle.gnss"}
		if q := strings.TrimSpace(r.URL.Query().Get("rvms")); q != "" {
			rvms = strings.Split(q, ",")
		}
		maxFrames := 5
		if q := r.URL.Query().Get("max"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 50 {
				maxFrames = n
			}
		}
		frames, err := lc.ProbeParallaxRaw(r.Context(), vid, rvms, maxFrames)
		if errors.Is(err, rivian.ErrNavProbeRefused) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the navigation topics cannot be probed"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"frames": frames})
	}
}

// handleSupportedFeaturesProbe returns the per-vehicle capability map
// (feature name -> status) from Rivian's SupportedFeatures query. Used
// to confirm which vehicles report PX_STATE_ALL=AVAILABLE, i.e. are
// eligible for the Parallax GPS feed.
func handleSupportedFeaturesProbe(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lc, ok := c.(*rivian.LiveClient)
		if !ok || lc == nil {
			http.Error(w, "no live rivian client configured", http.StatusNotFound)
			return
		}
		feats, err := lc.SupportedFeatures(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, feats)
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
