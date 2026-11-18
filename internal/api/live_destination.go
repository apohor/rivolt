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

// liveDestinationRouteSlackM is how far the route's last point may be from the destination for a precision to count as the right one.
const liveDestinationRouteSlackM = 2000.0

const metresPerMile = 1609.344

// liveDestinationBody is what a client sees for an active trip: the place, the arrival and the way. It is in memory on the owner pod and
// nowhere else; this struct is also the only thing a forwarded reply carries (sealed).
type liveDestinationBody struct {
	Name           string   `json:"name"`
	Lat            float64  `json:"lat"`
	Lon            float64  `json:"lon"`
	ETA            string   `json:"eta"`
	MilesLeft      *float64 `json:"miles_left,omitempty"` // always miles, whatever the user's unit; the client converts. Omitted when the distance is unknown.
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
	b := &liveDestinationBody{Name: t.Name, Lat: t.Lat, Lon: t.Lon, ETA: t.ETA.UTC().Format(time.RFC3339)}
	if prec := routePrecisionFor(t.Route, t.Lat, t.Lon); prec != 0 {
		b.Route, b.RoutePrecision = t.Route, prec
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

// forwardLiveDestination reads the trip from the vehicle's group owner. It is only asked on a pod that holds no tokens for the account, so
// that pod has no trip of its own: when the forwarder falls soft (no Redis, no group) and runs the call here, the answer "no trip" would be a
// guess, and the caller gets the retryable 503 instead.
func (sf *stateForward) forwardLiveDestination(ctx context.Context, vehicleID string) (liveDestinationWire, error) {
	group, ok := sf.groupOf(vehicleID)
	if !ok {
		return liveDestinationWire{}, errOwnerUnavailable
	}
	res, _, err := sf.fw.Do(ctx, forward.Call{Kind: forward.KindLiveDestination, User: sf.user.String(), Group: group, Args: forward.LiveDestinationArgs{VehicleID: vehicleID}},
		func(context.Context) (forward.Result, error) {
			return forward.Result{}, errOwnerUnavailable
		})
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
			// Before any answer, so the ownership gate's 404 carries the header too: a 404 that differs from the flag-off one is a probe.
			w.Header().Set("Cache-Control", "private, no-store")
			uid, ok := auth.UserFromContext(r.Context())
			if !ok || uid == uuid.Nil || on == nil || !on(uid) {
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
			if wire, err = sf.forwardLiveDestination(r.Context(), id); err != nil {
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

// routePrecisionFor says which polyline precision (5 or 6) the route was encoded at, or 0 when neither fits. Rivian sends precision 5 and the
// sim sends 6, and the wrong one scales every point by 10, so the answer is the precision whose last point lands within
// liveDestinationRouteSlackM of the destination. Neither fitting means the route is not trusted and is left out. Nothing is logged.
func routePrecisionFor(route string, lat, lon float64) int {
	if route == "" {
		return 0
	}
	for _, prec := range []int{5, 6} {
		if rl, ro, ok := polylineLast(route, prec); ok && haversineMetres(rl, ro, lat, lon) <= liveDestinationRouteSlackM {
			return prec
		}
	}
	return 0
}

// polylineLast decodes an encoded polyline and returns its last point. It is false for an empty or malformed string (a truncated value, or
// a byte outside the encoding's range).
func polylineLast(s string, prec int) (lat, lon float64, ok bool) {
	var la, lo, n int
	scale := math.Pow10(prec)
	for i := 0; i < len(s); {
		var v [2]int
		for k := 0; k < 2; k++ {
			shift, res := 0, 0
			for {
				if i >= len(s) || shift > 30 {
					return 0, 0, false
				}
				c := int(s[i]) - 63
				i++
				if c < 0 || c > 63 {
					return 0, 0, false
				}
				res |= (c & 0x1f) << shift
				shift += 5
				if c < 0x20 {
					break
				}
			}
			if res&1 != 0 {
				v[k] = ^(res >> 1)
			} else {
				v[k] = res >> 1
			}
		}
		la += v[0]
		lo += v[1]
		n++
	}
	if n == 0 {
		return 0, 0, false
	}
	return float64(la) / scale, float64(lo) / scale, true
}

func haversineMetres(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}
