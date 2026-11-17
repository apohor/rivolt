package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

// Made-up place and point; nothing real.
const destSentinel = "Zzyzx Test Plaza"

func destBody(withMiles bool) json.RawMessage {
	b := &liveDestinationBody{Name: destSentinel, Lat: 41.2718, Lon: -96.3314, ETA: "2030-01-02T03:04:05Z", Route: "abc", RoutePrecision: 6}
	if withMiles {
		mi := 12.3
		b.MilesLeft = &mi
	}
	raw, _ := json.Marshal(liveDestinationWire{Trip: b})
	return raw
}

// destRouter is the route's real middleware order: flag gate, ownership gate, handler.
func destRouter(flagOn bool, owns bool, sf *stateForward, c rivian.Client, mon *rivian.StateMonitor) http.Handler {
	r := chi.NewRouter()
	own := vehicleOwnershipMW(func(context.Context, uuid.UUID, string) (bool, error) { return owns, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.With(requireLiveDestinationMW(func(uuid.UUID) bool { return flagOn }), own).
		Get("/api/live-destination/{vehicleID}", handleLiveDestination(c, mon, sf))
	return r
}

func destGet(h http.Handler, signedIn bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/live-destination/"+fwdVehicle, nil)
	if signedIn {
		req = req.WithContext(auth.WithUser(req.Context(), uuid.MustParse("00000000-0000-0000-0000-000000000001")))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// C3: flag off, another user's vehicle and no session are the same 404 (status, body, caching), and none reaches the forwarder.
func TestLiveDestinationRefusalsAreOneIndistinguishable404(t *testing.T) {
	fw := &fakeStateForwarder{res: forward.Result{Status: 200, Body: destBody(true)}}
	sf := newStateForward(fw)
	cases := map[string]*httptest.ResponseRecorder{
		"flag off":          destGet(destRouter(false, true, sf, rivian.NewUnheldClient(), nil), true),
		"not my vehicle":    destGet(destRouter(true, false, sf, rivian.NewUnheldClient(), nil), true),
		"no signed-in user": destGet(destRouter(true, true, sf, rivian.NewUnheldClient(), nil), false),
	}
	unknown := httptest.NewRecorder()
	http.NotFound(unknown, httptest.NewRequest(http.MethodGet, "/x", nil))
	for name, rec := range cases {
		if rec.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() {
			t.Errorf("%s: %d %q, want the plain 404 %q", name, rec.Code, rec.Body.String(), unknown.Body.String())
		}
		if strings.Contains(rec.Body.String(), "Zzyzx") {
			t.Errorf("%s: the refusal shows a destination", name)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
			t.Errorf("%s: Cache-Control = %q", name, cc)
		}
	}
	if len(fw.calls) != 0 {
		t.Errorf("a refused request reached the forwarder: %+v", fw.calls)
	}
}

func TestLiveDestinationOnATokenlessPodAsksTheOwnerAndNeverCaches(t *testing.T) {
	fw := &fakeStateForwarder{res: forward.Result{Status: 200, Body: destBody(true)}}
	rec := destGet(destRouter(true, true, newStateForward(fw), rivian.NewUnheldClient(), nil), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != destSentinel || got["miles_left"] != 12.3 || got["route"] != "abc" || got["route_precision"] != float64(6) || got["eta"] == nil {
		t.Errorf("body %s", rec.Body)
	}
	if _, has := got["trip"]; has {
		t.Errorf("the wire wrapper leaked into the response: %s", rec.Body)
	}
	if len(fw.calls) != 1 || fw.calls[0].Kind != forward.KindLiveDestination || fw.calls[0].Group != fwdGroup {
		t.Fatalf("calls %+v, want one live_destination to the vehicle's group", fw.calls)
	}
	if a, ok := fw.calls[0].Args.(forward.LiveDestinationArgs); !ok || a.VehicleID != fwdVehicle {
		t.Errorf("args %+v, want the vehicle id only", fw.calls[0].Args)
	}
}

func TestLiveDestinationNoTripIs204AndMilesAreHiddenWithoutADistance(t *testing.T) {
	none, _ := json.Marshal(liveDestinationWire{})
	rec := destGet(destRouter(true, true, newStateForward(&fakeStateForwarder{res: forward.Result{Status: 200, Body: none}}), rivian.NewUnheldClient(), nil), true)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("no trip: %d %q cc=%q, want an empty uncached 204", rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
	}
	rec = destGet(destRouter(true, true, newStateForward(&fakeStateForwarder{res: forward.Result{Status: 200, Body: destBody(false)}}), rivian.NewUnheldClient(), nil), true)
	if strings.Contains(rec.Body.String(), "miles_left") {
		t.Errorf("miles shown without a distance: %s", rec.Body)
	}
}

func TestLiveDestinationOwnerUnreachableIsARetryable503(t *testing.T) {
	for name, fw := range map[string]*fakeStateForwarder{
		"unavailable": {res: forward.UnavailableResult()},
		"bad reply":   {res: forward.Result{Status: 200, Body: json.RawMessage(`not json`)}},
	} {
		rec := destGet(destRouter(true, true, newStateForward(fw), rivian.NewUnheldClient(), nil), true)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s: %d retry-after %q", name, rec.Code, rec.Header().Get("Retry-After"))
		}
		if rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("%s: cacheable", name)
		}
	}
}

// Redis down, or no group: the forwarder runs the call on this tokenless pod. It has no trip, so "no trip" would be a guess: 503, not 204.
func TestLiveDestinationSoftLocalOnATokenlessPodIs503NotNoTrip(t *testing.T) {
	fw := &fakeStateForwarder{run: true}
	rec := destGet(destRouter(true, true, newStateForward(fw), rivian.NewUnheldClient(), nil), true)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("soft local: %d retry-after %q cc %q", rec.Code, rec.Header().Get("Retry-After"), rec.Header().Get("Cache-Control"))
	}
}

// A pod that is itself the owner answers from its own monitor with no forward at all.
func TestLiveDestinationOnTheOwnerPodMakesNoForward(t *testing.T) {
	fw := &fakeStateForwarder{res: forward.Result{Status: 200, Body: destBody(true)}}
	rec := destGet(destRouter(true, true, newStateForward(fw), rivian.NewLive(), rivian.NewStateMonitor(nil, nil)), true)
	if rec.Code != http.StatusNoContent || len(fw.calls) != 0 {
		t.Errorf("owner pod: %d with %d forwards, want a local 204 and none", rec.Code, len(fw.calls))
	}
}

func TestLiveDestinationOfShapesTheTrip(t *testing.T) {
	eta := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	w := liveDestinationOf(rivian.NavTrip{Name: "N", Lat: 1.5, Lon: 2.5, ETA: eta, MetresLeft: 16093.44, HasDist: true, Route: "r"}, true)
	if w.Trip == nil || w.Trip.ETA != "2030-01-02T02:04:05Z" || w.Trip.MilesLeft == nil || *w.Trip.MilesLeft != 10 || w.Trip.RoutePrecision != 6 {
		t.Fatalf("trip %+v", w.Trip)
	}
	if w := liveDestinationOf(rivian.NavTrip{Name: "N", MetresLeft: 99999}, true); w.Trip.MilesLeft != nil || w.Trip.RoutePrecision != 0 {
		t.Errorf("without HasDist or a route: %+v", w.Trip)
	}
	if liveDestinationOf(rivian.NavTrip{}, false).Trip != nil {
		t.Error("no trip must be a nil trip")
	}
}

func TestServeLiveDestinationAnswersFromTheMonitor(t *testing.T) {
	if r := ServeLiveDestination(nil, fwdVehicle); r.Status != http.StatusServiceUnavailable {
		t.Errorf("no monitor = %d, want 503", r.Status)
	}
	r := ServeLiveDestination(rivian.NewStateMonitor(nil, nil), fwdVehicle)
	if r.Status != http.StatusOK || string(r.Body) != `{"trip":null}` {
		t.Errorf("empty monitor = %d %s, want 200 and a null trip", r.Status, r.Body)
	}
}

// The mounted route, not just the handler: with no flags store (flag off for everyone) the real router answers 404 for a signed-in user and
// never reaches the forwarder, and the answer is not cacheable.
func TestLiveDestinationRouteIsOffWithoutTheFlag(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	fw := &fakeStateForwarder{res: forward.Result{Status: 200, Body: destBody(true)}}
	router := New(Deps{Auth: svc, AuthEnforced: true, Accounts: gatedRegistry(), StateForward: fw,
		GroupOfVehicle: func(string) (string, bool) { return fwdGroup, true }})
	req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/live-destination/"+fwdVehicle, nil)
	req.RemoteAddr = "203.0.113.9:4000"
	ctx := auth.WithUser(req.Context(), uuid.New())
	ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: "web"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Zzyzx") || len(fw.calls) != 0 {
		t.Fatalf("route with the flag off = %d %q, %d forwards; want a plain 404", rec.Code, rec.Body, len(fw.calls))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
}

// An anonymous visitor can carry the nil user id; with the flag on for everyone (All) it must still get the plain 404, not the 401 a
// handler would give, which would tell a probe the endpoint exists. The 401 also loses the no-store header (http.Error drops it).
func TestLiveDestinationNilUserIsThePlain404EvenWhenFlagIsOnForAll(t *testing.T) {
	fw := &fakeStateForwarder{res: forward.Result{Status: 200, Body: destBody(true)}}
	h := destRouter(true, true, newStateForward(fw), rivian.NewUnheldClient(), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/live-destination/"+fwdVehicle, nil)
	req = req.WithContext(auth.WithUser(req.Context(), uuid.Nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	unknown := httptest.NewRecorder()
	http.NotFound(unknown, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("nil user: %d %q cc=%q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	if len(fw.calls) != 0 {
		t.Errorf("reached the forwarder: %+v", fw.calls)
	}
}

// A parked vehicle's trip_progress frames must never show as a destination: a frame stream with no route is no trip, and a trip whose last
// trip_progress is older than the stale floor reads as no trip at the mounted route (204, never cached), even though nothing cleared it.
func TestLiveDestinationOfAParkedVehicleWithStaleProgressIs204(t *testing.T) {
	mon := rivian.NewStateMonitor(nil, nil)
	mon.SetNavDecodeGate(func() bool { return true })
	now := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
	mon.SetNavClockForTest(func() time.Time { return now })
	h := destRouter(true, true, nil, rivian.NewMock(), mon)
	progress := &rivian.NavFrame{Progress: &rivian.NavProgress{ETA: now.Add(time.Hour), NextStop: 0, LegMetres: 900, HasLegDist: true}}

	// Overnight progress frames with no trip_info (the floor measured on prod): no trip.
	mon.ApplyNavFrameForTest(fwdVehicle, progress)
	if rec := destGet(h, true); rec.Code != http.StatusNoContent {
		t.Fatalf("progress with no route: %d %s, want 204", rec.Code, rec.Body)
	}

	// A trip with a fresh progress frame shows (the setup is real) ...
	mon.ApplyNavFrameForTest(fwdVehicle, &rivian.NavFrame{Info: &rivian.NavInfo{Name: destSentinel, Lat: 41.2718, Lon: -96.3314, Stops: 1, LegMetres: []float64{900}}})
	mon.ApplyNavFrameForTest(fwdVehicle, progress)
	if rec := destGet(h, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), destSentinel) {
		t.Fatalf("fresh trip: %d %s, want 200 with the trip", rec.Code, rec.Body)
	}
	// ... and the vehicle parks: no new progress frame, so past the floor it reads as no trip, and the answer is not cacheable.
	now = now.Add(4 * time.Minute)
	rec := destGet(h, true)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("stale progress: %d %q cc=%q, want an empty uncached 204", rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
	}
	if strings.Contains(rec.Body.String(), destSentinel) {
		t.Error("a stale trip still named its destination")
	}
}
