package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

// Discovery states the endpoint reports. reauth comes first: a user who must sign in again sees the sign-in prompt, never "looking".
const (
	discoveryUnlinked = "unlinked" // no Rivian session stored
	discoveryReauth   = "reauth"   // the stored session is flagged: sign in again
	discoveryFound    = "found"    // the account has vehicles
	discoveryLooking  = "looking"  // the owner has not found out yet (or the last lookup failed and is retried)
	discoveryNone     = "none"     // Rivian answered and the account has no vehicle yet
)

// vehiclesAccountState reads the user's Rivian account state from the users row (not this pod's memory): linked when a session is stored,
// needsReauth when it is flagged for sign-in again, ok false when it cannot be read here.
func (d Deps) vehiclesAccountState(uid uuid.UUID) func(context.Context) (linked, needsReauth, ok bool) {
	return func(ctx context.Context) (linked, needsReauth, ok bool) {
		st, ok := freshAccountState(auth.WithUser(ctx, uid), d.Accounts, uid)
		if !ok {
			return false, false, false
		}
		return !st.SessionVersion.IsZero(), st.NeedsReauth, true
	}
}

// handleVehicleDiscovery answers GET /api/vehicles/discovery with {"state": ...}. Precedence: unlinked, reauth, found (a vehicle is stored), then
// what the owner's lookup recorded: none, or looking when nothing is recorded or the last lookup failed (it is retried).
func handleVehicleDiscovery(account func(context.Context) (linked, needsReauth, ok bool), sqlDB *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			http.Error(w, "sign in required", http.StatusUnauthorized)
			return
		}
		linked, reauth, known := account(r.Context())
		switch {
		case !known || !linked:
			writeJSON(w, http.StatusOK, map[string]string{"state": discoveryUnlinked})
			return
		case reauth:
			writeJSON(w, http.StatusOK, map[string]string{"state": discoveryReauth})
			return
		}
		rows, err := db.ListUserVehiclesForAPI(r.Context(), sqlDB, uid)
		if err != nil {
			http.Error(w, "could not read your vehicles", http.StatusInternalServerError)
			return
		}
		if len(rows) > 0 {
			writeJSON(w, http.StatusOK, map[string]string{"state": discoveryFound})
			return
		}
		state, have, err := db.GetDiscovery(r.Context(), sqlDB, uid)
		if err != nil {
			http.Error(w, "could not read your vehicles", http.StatusInternalServerError)
			return
		}
		if have && state == db.DiscoveryNone {
			writeJSON(w, http.StatusOK, map[string]string{"state": discoveryNone})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"state": discoveryLooking})
	}
}
