package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

// liveDestinationRoutePrecision is the polyline precision of the route the endpoint returns. The sim sends 6; the first real drive's
// frames confirm it (the design's polyline_precision check), and this is the one place that changes if they say 5.
const liveDestinationRoutePrecision = 6

const metresPerMile = 1609.344

// liveDestinationBody is what a client sees for an active trip: the place, the arrival and the way. It is in memory on the owner pod and
// nowhere else; this struct is also the only thing a forwarded reply carries (sealed).
type liveDestinationBody struct {
	Name           string   `json:"name"`
	Lat            float64  `json:"lat"`
	Lon            float64  `json:"lon"`
	ETA            string   `json:"eta"`
	MilesLeft      *float64 `json:"miles_left,omitempty"`
	Route          string   `json:"route,omitempty"`
	RoutePrecision int      `json:"route_precision,omitempty"`
}

// liveDestinationWire is the owner's answer to a forwarded live_destination. Trip is null when there is no active trip, so "no trip" and
// "a trip" have the same shape and both travel sealed.
type liveDestinationWire struct {
	Trip *liveDestinationBody `json:"trip"`
}

func liveDestinationOf(t rivian.NavTrip, ok bool) liveDestinationWire {
	if !ok {
		return liveDestinationWire{}
	}
	b := &liveDestinationBody{Name: t.Name, Lat: t.Lat, Lon: t.Lon, ETA: t.ETA.UTC().Format(time.RFC3339), Route: t.Route}
	if t.Route != "" {
		b.RoutePrecision = liveDestinationRoutePrecision
	}
	if t.HasDist { // without a leg distance MetresLeft is the later legs only: leave the miles out
		mi := math.Round(t.MetresLeft/metresPerMile*10) / 10
		b.MilesLeft = &mi
	}
	return liveDestinationWire{Trip: b}
}

// ServeLiveDestination is the owner's side of a forwarded live_destination: its own monitor's in-memory trip for the vehicle, and no Rivian
// call. The monitor answers only while the user's live_destination flag is on (its decode gate), so flag-off reads as no trip. The body
// is plain here; the forwarder seals it before it reaches Redis.
func ServeLiveDestination(mon *rivian.StateMonitor, vehicleID string) forward.Result {
	if mon == nil {
		return forward.Result{Status: http.StatusServiceUnavailable, Class: forward.ClassUnavailable, Body: json.RawMessage(`{}`)}
	}
	raw, err := json.Marshal(liveDestinationOf(mon.NavDestination(vehicleID)))
	if err != nil {
		return forward.Result{Status: http.StatusBadGateway, Class: forward.ClassInternal}
	}
	return forward.Result{Status: http.StatusOK, Body: raw}
}

// forwardLiveDestination reads the trip from the vehicle's group owner when this pod holds no tokens for the account, as forwardLiveSession
// does. local is what this pod answers when it serves the call itself (it holds the group, or the forward fell soft; no Redis then).
func (sf *stateForward) forwardLiveDestination(ctx context.Context, vehicleID string, local func() liveDestinationWire) (liveDestinationWire, error) {
	group, ok := sf.groupOf(vehicleID)
	if !ok {
		if !sf.fw.NoGroup(forward.KindLiveDestination) {
			return liveDestinationWire{}, errOwnerUnavailable
		}
		return local(), nil
	}
	var (
		w   liveDestinationWire
		ran bool
	)
	res, _, err := sf.fw.Do(ctx, forward.Call{Kind: forward.KindLiveDestination, User: sf.user.String(), Group: group, Args: forward.LiveDestinationArgs{VehicleID: vehicleID}},
		func(context.Context) (forward.Result, error) {
			ran = true
			w = local()
			return forward.Result{Status: http.StatusOK}, nil
		})
	if ran {
		return w, nil
	}
	if err != nil {
		return liveDestinationWire{}, err
	}
	if res.Status != http.StatusOK {
		return liveDestinationWire{}, errOwnerUnavailable
	}
	var out liveDestinationWire
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return liveDestinationWire{}, errOwnerUnavailable
	}
	return out, nil
}

// requireLiveDestinationMW answers 404 unless the signed-in user's live_destination flag is on. It is the same 404 the router gives an
// unknown path and the ownership gate gives another user's vehicle, so the endpoint's existence is not a probe.
func requireLiveDestinationMW(on func(uuid.UUID) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := auth.UserFromContext(r.Context())
			if !ok || on == nil || !on(uid) {
				w.Header().Set("Cache-Control", "private, no-store")
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// handleLiveDestination serves GET /api/live-destination/{vehicleID}: 204 with no active trip, 200 with the trip. Never cached. The
// vehicle belongs to the user (the ownership gate ran) and the user's flag is on (the flag gate ran) before it is reached.
func handleLiveDestination(c rivian.Client, mon *rivian.StateMonitor, sf *stateForward) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		id := chi.URLParam(r, "vehicleID")
		if id == "" {
			http.Error(w, "vehicleID required", http.StatusBadRequest)
			return
		}
		local := func() liveDestinationWire {
			if mon == nil {
				return liveDestinationWire{}
			}
			return liveDestinationOf(mon.NavDestination(id))
		}
		var wire liveDestinationWire
		if rivian.IsUnheld(c) && sf != nil && sf.fw != nil && sf.groupOf != nil {
			// No tokens here: the group's owner holds the trip (this pod answers itself only when it is the owner).
			var err error
			if wire, err = sf.forwardLiveDestination(r.Context(), id, local); err != nil {
				writeUnheld(w)
				return
			}
		} else {
			wire = local()
		}
		if wire.Trip == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, wire.Trip)
	}
}
