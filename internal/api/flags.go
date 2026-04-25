package api

import (
	"encoding/json"
	"net/http"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/flags"
)

// handleFlagsGet returns the current kill-switch state as JSON. No
// secrets on this payload; the value is identical to what operators
// can read directly from `SELECT * FROM flags` in psql.
func handleFlagsGet(store *flags.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if store == nil {
			http.Error(w, "flags store unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kill_switch": store.KillSwitch(),
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
