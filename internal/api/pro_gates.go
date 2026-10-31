package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/trips"
)

// The free plan's feature gates (Rivolt Pro, Sprint 14, item 18). Every gate asks entitlements.Service.Pro, so billing off (every
// self-hosted install), the demo account and a failed plan lookup all count as Pro: the gates fail open, exactly as the history window
// does. A gate answers 402 {"error":"pro_required"} on every client. Nothing is deleted, and nothing here touches Rivian: the poller and
// the shared budget never look at the plan.

// FreeSavedTrips is how many saved trips a free account can add and edit. Further trips are kept, read-only.
const FreeSavedTrips = 3

// isPro is the plan of uid; a nil service (no plan wired) is Pro.
func isPro(ctx context.Context, e *entitlements.Service, uid uuid.UUID) bool {
	return e == nil || e.Pro(ctx, uid)
}

// writeProRequiredLimit is a 402 pro_required that also names the free limit that was hit.
func writeProRequiredLimit(w http.ResponseWriter, limit int) {
	writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": "pro_required", "limit": limit})
}

// multidayNeedsPro reports whether a plan-multiday request is a Pro plan: more than one day, an overnight (in either request shape), or
// "re-plan from here". A single day with no overnight is the free planner, whichever endpoint carries it.
func multidayNeedsPro(body []byte) bool {
	var req struct {
		OriginFromVehicle bool `json:"origin_from_vehicle"`
		Days              []struct {
			Overnight json.RawMessage `json:"overnight"`
		} `json:"days"`
		Overnights []json.RawMessage `json:"overnights"`
	}
	if json.Unmarshal(body, &req) != nil {
		return false // the handler answers its own 400
	}
	if req.OriginFromVehicle || len(req.Days) > 1 || len(req.Overnights) > 0 {
		return true
	}
	for _, d := range req.Days {
		if s := bytes.TrimSpace(d.Overnight); len(s) > 0 && !bytes.Equal(s, []byte("null")) {
			return true
		}
	}
	return false
}

// multidayProMiddleware answers 402 pro_required to a free account's multi-day plan, before any routing or Rivian call. The body is read
// (it is already size-limited by the group) and put back for the handler.
func multidayProMiddleware(e *entitlements.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if e == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := auth.UserFromContext(r.Context())
			if !ok || isPro(r.Context(), e, uid) {
				next.ServeHTTP(w, r)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				tripBodyError(w, err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if multidayNeedsPro(body) {
				writeProRequired(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// savedTripCreateGate refuses a free account's save once it keeps FreeSavedTrips trips.
func savedTripCreateGate(ctx context.Context, e *entitlements.Service, uid uuid.UUID, store *trips.Store) (allowed bool, err error) {
	if store == nil || isPro(ctx, e, uid) {
		return true, nil
	}
	n, err := store.Count(ctx)
	if err != nil {
		return false, err
	}
	return n < FreeSavedTrips, nil
}

// savedTripEditable reports whether a free account may edit the trip: only the FreeSavedTrips most recently updated are editable, the
// rest are read-only (viewable, deletable). A trip that does not exist is "editable" so the handler's own 404 answers.
func savedTripEditable(ctx context.Context, e *entitlements.Service, uid uuid.UUID, store *trips.Store, id uuid.UUID) (bool, error) {
	if store == nil || isPro(ctx, e, uid) {
		return true, nil
	}
	recent, err := store.Recent(ctx, FreeSavedTrips)
	if err != nil {
		return false, err
	}
	if recent[id] {
		return true, nil
	}
	if _, err := store.Get(ctx, id); errors.Is(err, trips.ErrNotFound) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return false, nil
}

// markReadOnlyTrips flags, for a free account, the trips past the cap. out is newest-updated first, as Store.List returns it.
func markReadOnlyTrips(ctx context.Context, e *entitlements.Service, uid uuid.UUID, out []trips.SavedTrip) {
	if isPro(ctx, e, uid) {
		return
	}
	for i := FreeSavedTrips; i < len(out); i++ {
		out[i].ReadOnly = true
	}
}
