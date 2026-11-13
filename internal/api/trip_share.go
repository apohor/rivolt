package api

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/trips"
	"github.com/apohor/rivolt/internal/tripshare"
)

// Shared trip links (Sprint 23, design in _shared/share-trip-design.md): the owner makes a link for a saved trip, anyone holding it reads a
// frozen, sanitized copy of the plan with no account. Behind RIVOLT_TRIP_SHARE (default off): with the flag off none of these routes exist.

// publicSharePath is the one public route. It is served outside the session chain (see skipAuthFor) so a cookie or bearer token sent with it
// is neither read nor answered with a Set-Cookie.
const publicSharePath = "/api/public/trips/share"

// shareLookupSlots caps the public lookups in flight on one pod; past it a request is answered 503 at once, so a flood cannot hold the pool.
// It is at most half the system pool (the pool /readyz pings too), at least 1, so anonymous lookups can never take every connection.
func shareLookupSlots(poolSize int) int { return max(1, poolSize/2) }

// shareLookupTimeout bounds the one indexed query behind a lookup.
const shareLookupTimeout = 3 * time.Second

// shareMaxTitle is the longest title the owner may give a link.
const shareMaxTitle = 80

type tripShareAPI struct {
	store    *tripshare.Store
	trips    *trips.Factory
	settings *settings.Factory
	slots    chan struct{}
	count    func(event string)
	log      *slog.Logger
	now      func() time.Time
}

func newTripShareAPI(d Deps) *tripShareAPI {
	poolN, _ := db.SystemPoolSize(os.Getenv)
	a := &tripShareAPI{
		store:    &tripshare.Store{Req: d.DB, Sys: d.sysDB()},
		trips:    d.Trips,
		settings: d.Settings,
		slots:    make(chan struct{}, shareLookupSlots(poolN)),
		count:    func(string) {},
		log:      d.Logger,
		now:      time.Now,
	}
	if d.Metrics != nil {
		a.count = func(ev string) { d.Metrics.TripShareTotal.WithLabelValues(ev).Inc() }
	}
	return a
}

// skipAuthFor runs the session middleware on every request but those for which skip is true.
func skipAuthFor(skip func(*http.Request) bool, auth func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		with := auth(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			with.ServeHTTP(w, r)
		})
	}
}

func isPublicSharePath(r *http.Request) bool { return r.URL.Path == publicSharePath }

type createShareRequest struct {
	Title string `json:"title"`
	// ExpiresInDays is 1, 7 or 30, or absent for a link that stays until it is revoked.
	ExpiresInDays *int `json:"expires_in_days"`
}

type createShareResponse struct {
	tripshare.Link
	// Token is shown once, here; only its hash is stored. The page address is /s#<token>.
	Token string `json:"token"`
	Path  string `json:"path"`
}

func (a *tripShareAPI) create(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var req createShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid body")
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Shared trip"
	}
	if utf8.RuneCountInString(title) > shareMaxTitle {
		badRequest(w, "title is too long")
		return
	}
	var expires *time.Time
	if req.ExpiresInDays != nil {
		switch *req.ExpiresInDays {
		case 1, 7, 30:
			t := a.now().Add(time.Duration(*req.ExpiresInDays) * 24 * time.Hour)
			expires = &t
		default:
			badRequest(w, "expires_in_days must be 1, 7 or 30")
			return
		}
	}
	saved, err := a.trips.For(uid).Get(r.Context(), id)
	if errors.Is(err, trips.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, "load saved trip", err)
		return
	}
	if len(saved.Plan) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this trip has no plan to share"})
		return
	}
	var home *tripshare.Circle
	if a.settings != nil {
		if h, err := settings.GetHomeLocation(r.Context(), a.settings.For(uid)); err == nil && h.Set {
			home = &tripshare.Circle{Lat: h.Latitude, Lon: h.Longitude, RadiusM: rivian.HomeRadiusM}
		}
	}
	cut := tripshare.RandomCut(randFloat())
	var departAt struct {
		DepartAt string `json:"depart_at"`
	}
	_ = json.Unmarshal(saved.Inputs, &departAt)
	snap, err := tripshare.Build(tripshare.Input{
		Title: title, Plan: saved.Plan, DepartAt: departAt.DepartAt, PlannedAt: saved.UpdatedAt, Home: home, CutM: cut,
	})
	if errors.Is(err, tripshare.ErrUnsupported) || errors.Is(err, tripshare.ErrTooShort) {
		a.count("refused")
		msg := "this trip cannot be shared yet (only single-day trips with a route line can)"
		if errors.Is(err, tripshare.ErrTooShort) {
			msg = "this trip is too short to share without showing where it starts"
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": msg, "code": "too_short"})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": msg, "code": "unsupported"})
		return
	}
	if err != nil {
		a.fail(w, "build snapshot", err)
		return
	}
	body, err := json.Marshal(snap)
	if err != nil {
		a.fail(w, "marshal snapshot", err)
		return
	}
	token, hash, err := tripshare.NewToken()
	if err != nil {
		a.fail(w, "token", err)
		return
	}
	link, err := a.store.Create(r.Context(), uid, id, title, hash, cut, body, expires)
	switch {
	case errors.Is(err, tripshare.ErrCap):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "too many active links; revoke one first", "code": "too_many_links"})
		return
	case errors.Is(err, tripshare.ErrRate):
		w.Header().Set("Retry-After", "600")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many links created; try again later", "code": "rate_limited"})
		return
	case err != nil:
		a.fail(w, "create", err)
		return
	}
	a.count("created")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, createShareResponse{Link: link, Token: token, Path: "/s#" + token})
}

func (a *tripShareAPI) list(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	links, err := a.store.List(r.Context(), uid, id)
	if err != nil {
		a.fail(w, "list", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

func (a *tripShareAPI) revoke(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch err := a.store.Revoke(r.Context(), uid, id); {
	case errors.Is(err, tripshare.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		a.fail(w, "revoke", err)
	default:
		a.count("revoked")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *tripShareAPI) fail(w http.ResponseWriter, what string, err error) {
	if a.log != nil {
		a.log.Warn("trip share failed", "step", what, "err", err.Error())
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
}

// notFound is the one answer for an unknown, expired and revoked token (and for a flag that is off): the same status and body whichever it
// is, so the response says nothing about which tokens ever existed.
func shareNotFound(w http.ResponseWriter) {
	shareHeaders(w)
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not found"}` + "\n"))
}

func shareHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
}

// view serves POST /api/public/trips/share {"token"}: the snapshot of a live link. The token travels in the body, not the URL, so it is in no
// access log and no Referer; the page address carries it in the fragment, which a browser never sends.
func (a *tripShareAPI) view(w http.ResponseWriter, r *http.Request) {
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	default:
		a.count("busy")
		shareHeaders(w)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"busy"}` + "\n"))
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !tripshare.ValidToken(body.Token) {
		a.count("miss")
		shareNotFound(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), shareLookupTimeout)
	defer cancel()
	snap, err := a.store.Lookup(ctx, tripshare.HashToken(body.Token))
	switch {
	case errors.Is(err, tripshare.ErrNotFound):
		a.count("miss")
		shareNotFound(w)
	case err != nil:
		a.fail(w, "lookup", err)
	default:
		a.count("viewed")
		shareHeaders(w)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(snap)
	}
}

// startTripShareSweep removes links that ended more than 30 days ago, once a day. Safe on every replica: the delete is idempotent.
func startTripShareSweep(a *tripShareAPI) {
	go func() {
		t := time.NewTimer(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			if n, err := a.store.Sweep(ctx); err != nil {
				slog.Warn("trip share sweep failed", "err", err.Error())
			} else if n > 0 {
				slog.Info("trip share sweep", "removed", n)
			}
			cancel()
			t.Reset(24 * time.Hour)
		}
	}()
}

// randFloat is a uniform float in [0,1) from crypto/rand: the cut radius must not be predictable.
func randFloat() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
}
