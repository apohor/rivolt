package api

import (
	"encoding/json"
	"net/http"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/flags"
)

// requireTripPlannerEnabledMW 404s requests when the trip-planner
// feature flag is off. Returns 404 (not 403) so a disabled planner
// looks identical to a deploy that never had the route — matches
// what the SPA expects when it stops rendering the link.
func requireTripPlannerEnabledMW(store *flags.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil || !store.TripPlanner().Enabled {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// handleFlagsGet returns every operational flag as JSON. Admin-only
// — exposes audit fields (actor, reason). The non-admin SPA reads
// the user-visible subset via /api/flags instead.
func handleFlagsGet(store *flags.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if store == nil {
			http.Error(w, "flags store unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kill_switch":  store.KillSwitch(),
			"trip_planner": store.TripPlanner(),
		})
	}
}

// flagsKillRequest is the PUT body for /api/admin/kill-switch.
// Reason is optional but strongly encouraged — the value lands in
// flags.value and is the only signal future operators have about
// why Rivolt was paused. Actor is taken from the session, not the
// body, so an operator can't impersonate another admin by setting
// it client-side.
type flagsKillRequest struct {
	Paused bool   `json:"paused"`
	Reason string `json:"reason,omitempty"`
}

// handleFlagsKillPut flips the Rivian-upstream kill switch. Used
// from the Settings UI's \"Pause upstream\" button and any CLI that
// wants to close the circuit without a deploy. The immediate local
// refresh in Store.SetKillSwitch means the caller's own pod sees
// the flip before the HTTP response returns; remote pods catch up
// on their next poll interval (~10s).
func handleFlagsKillPut(store *flags.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "flags store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req flagsKillRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Actor defaults to "admin" when auth is disabled (the
		// single-tenant self-host mode). With auth on, we stamp
		// the session user's UUID so "who paused us?" has an
		// answer in the flags row.
		actor := "admin"
		if uid, ok := auth.UserFromContext(r.Context()); ok {
			actor = uid.String()
		}
		if err := store.SetKillSwitch(r.Context(), req.Paused, req.Reason, actor); err != nil {
			http.Error(w, "set flag: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kill_switch": store.KillSwitch(),
		})
	}
}

// flagsTripPlannerRequest is the PUT body for the trip-planner
// admin toggle. Single boolean; actor is derived from the session.
type flagsTripPlannerRequest struct {
	Enabled bool `json:"enabled"`
}

// handleFlagsTripPlannerPut flips the trip-planner feature flag.
// 404s the planner UI/endpoints when disabled; immediate effect
// on the writer's pod, ~10s on peers.
func handleFlagsTripPlannerPut(store *flags.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "flags store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req flagsTripPlannerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		actor := "admin"
		if uid, ok := auth.UserFromContext(r.Context()); ok {
			actor = uid.String()
		}
		if err := store.SetTripPlanner(r.Context(), req.Enabled, actor); err != nil {
			http.Error(w, "set flag: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"trip_planner": store.TripPlanner(),
		})
	}
}
