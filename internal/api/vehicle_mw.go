package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/logging"
)

// vehicleOwnershipCheck is the seam the ownership middleware uses
// to ask "does this user own this Rivian vehicle-id?". The
// production wiring points it at db.OwnsRivianID; tests substitute
// an in-memory stub so they don't need a live Postgres.
type vehicleOwnershipCheck func(ctx context.Context, userID uuid.UUID, rivianID string) (bool, error)

// requireVehicleOwnershipMW returns chi middleware that refuses any
// request whose {vehicleID} URL param doesn't belong to the session
// user. See the package doc on vehicleOwnershipMW for the rationale.
func requireVehicleOwnershipMW(dbPool *sql.DB, logger *slog.Logger) func(http.Handler) http.Handler {
	check := func(ctx context.Context, userID uuid.UUID, rivianID string) (bool, error) {
		return db.OwnsRivianID(ctx, dbPool, userID, rivianID)
	}
	return vehicleOwnershipMW(check, logger)
}

// vehicleOwnershipMW is the ownership gate, factored out so tests
// can hand it a pure-Go check function instead of a *sql.DB.
//
// Behavior:
//
//   - Missing {vehicleID} param: pass through. The middleware is
//     only attached to routes that carry the param; a nested router
//     without it is a caller bug, not an auth decision.
//   - No authenticated user in context: pass through. This mirrors
//     the existing requireUserMW gating — when no auth issuer is
//     configured (no OIDC, no trusted-proxy CIDR, no bypass), the
//     server runs in legacy single-tenant mode where every request
//     is "the operator". Multi-tenant deployments MUST pair this middleware
//     with requireUserMW (which runs earlier in the chain) so the
//     fall-open branch can never be reached in anger.
//   - Check error: 500. Infra failure, not an auth answer; we don't
//     want to silently 404 through a broken Postgres.
//   - Not owned: 404 (not 403). 403 would leak existence —
//     enumerating vehicle-ids would tell an attacker which are
//     registered on the server. 404 matches "we don't know about
//     this URL", which is exactly what an unauthorized caller
//     should see.
func vehicleOwnershipMW(check vehicleOwnershipCheck, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rivianID := chi.URLParam(r, "vehicleID")
			if rivianID == "" {
				next.ServeHTTP(w, r)
				return
			}
			userID, ok := auth.UserFromContext(r.Context())
			if !ok {
				// Legacy single-tenant / auth-disabled mode; stores
				// are bound to the static operator identity and the
				// {vehicleID} is necessarily one of that operator's.
				next.ServeHTTP(w, r)
				return
			}
			owned, err := check(r.Context(), userID, rivianID)
			if err != nil {
				if logger != nil {
					logger.Warn("vehicle ownership check failed",
						"user_id", userID.String(),
						"vehicle_id", rivianID,
						"err", err.Error())
				}
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !owned {
				http.NotFound(w, r)
				return
			}
			// Stamp vehicle_id onto the logging context so every
			// downstream slog line in this request gets it for free.
			r = r.WithContext(logging.WithVehicleID(r.Context(), rivianID))
			next.ServeHTTP(w, r)
		})
	}
}
