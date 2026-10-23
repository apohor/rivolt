package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// GET /api/vehicles/{vehicleID}/ota/notes?which=current|available: a fresh link to a software release's notes.
//
// Rivian's link is a pre-signed S3 URL that is valid for one hour and carries temporary credentials (X-Amz-Security-Token),
// so a stored copy is dead long before it is tapped and is a credential at rest. It is therefore never stored or served
// with the vehicle list (only "has_notes" is). When the user taps "Release notes", this asks Rivian for the release details
// with that user's own session (the same read the daily sync makes; no vehicle command, no wake), checks the link, and
// hands it over once: a 302 for the browser (its cookie carries the session), or {"url": ...} when the caller asks for
// JSON (the app fetches it with its bearer token and then opens the URL; Safari would not carry the token).
//
// The link never goes into a log, an error, a span or a cache other than the short in-memory one below. A failing
// upstream is reported as a fixed class, never as its own text (which can carry the URL).

const (
	// otaNotesTTL is how long a fetched link is reused: under its one-hour life with room to open it.
	otaNotesTTL = 50 * time.Minute
	// otaNotesGap is the shortest time between two reads of Rivian for one user and vehicle.
	otaNotesGap = 5 * time.Minute
	// otaNotesMax bounds the in-memory table.
	otaNotesMax     = 2048
	otaNotesTimeout = 10 * time.Second
)

// otaNotesHostOK: the link must be https on a host Rivian publishes its documents from (the S3 bucket today) or on
// rivian.com. Anything else is not followed, so this is never an open redirect.
func otaNotesHostOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "rivian.com" || strings.HasSuffix(h, ".rivian.com") || strings.HasSuffix(h, ".amazonaws.com")
}

type otaNotesEntry struct {
	current, available string
	fetched            time.Time // when the links were fetched
	tried              time.Time // when Rivian was last asked
}

// otaNotesCache holds the fetched links in memory for otaNotesTTL and spaces the reads of Rivian. Nothing is persisted.
type otaNotesCache struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]*otaNotesEntry
}

func newOTANotesCache() *otaNotesCache {
	return &otaNotesCache{now: time.Now, m: map[string]*otaNotesEntry{}}
}

func (c *otaNotesCache) key(uid uuid.UUID, vehicleID string) string {
	return uid.String() + "|" + vehicleID
}

// get returns the cached link for which and whether the cache is fresh (a fresh entry with no link means there is none).
func (c *otaNotesCache) get(uid uuid.UUID, vehicleID, which string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[c.key(uid, vehicleID)]
	if e == nil || c.now().Sub(e.fetched) > otaNotesTTL {
		return "", false
	}
	if which == "available" {
		return e.available, true
	}
	return e.current, true
}

// begin records an attempt to read Rivian. It reports false, with how long to wait, when the last attempt was too recent.
func (c *otaNotesCache) begin(uid uuid.UUID, vehicleID string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	k := c.key(uid, vehicleID)
	e := c.m[k]
	if e != nil && now.Sub(e.tried) < otaNotesGap {
		return otaNotesGap - now.Sub(e.tried), false
	}
	if e == nil {
		if len(c.m) >= otaNotesMax {
			for old, v := range c.m { // drop the stalest
				if now.Sub(v.tried) > otaNotesGap {
					delete(c.m, old)
					break
				}
			}
			if len(c.m) >= otaNotesMax {
				return otaNotesGap, false
			}
		}
		e = &otaNotesEntry{}
		c.m[k] = e
	}
	e.tried = now
	return 0, true
}

func (c *otaNotesCache) put(uid uuid.UUID, vehicleID, current, available string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.m[c.key(uid, vehicleID)]; e != nil {
		e.current, e.available, e.fetched = current, available, c.now()
	}
}

// otaNotesClass maps an upstream failure to a status and a fixed message. The error's own text is never used.
func otaNotesClass(err error) (int, string) {
	switch {
	case errors.Is(err, rivian.ErrUpstreamPaused):
		return http.StatusServiceUnavailable, "release notes unavailable right now"
	case errors.Is(err, rivian.ErrNeedsReauth), errors.Is(err, rivian.ErrNotAuthenticated):
		return http.StatusUnauthorized, "reconnect Rivian to open release notes"
	default:
		return http.StatusBadGateway, "release notes unavailable right now"
	}
}

func handleOTANotes(source func(uuid.UUID) rivian.OTASource, cache *otaNotesCache, logger *slog.Logger) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		vehicleID := chi.URLParam(r, "vehicleID")
		which := r.URL.Query().Get("which")
		if which == "" {
			which = "current"
		}
		if which != "current" && which != "available" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "which must be current or available"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		link, fresh := cache.get(uid, vehicleID, which)
		if fresh && link == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no release notes"})
			return
		}
		if !fresh {
			wait, allowed := cache.begin(uid, vehicleID)
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "try again in a few minutes"})
				return
			}
			src := source(uid)
			if src == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "no release notes"})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), otaNotesTimeout)
			defer cancel()
			info, err := src.OTAUpdateDetails(ctx, vehicleID)
			if err != nil {
				status, msg := otaNotesClass(err)
				if logger != nil {
					logger.Warn("ota notes: upstream read failed", "user_id", uid.String(), "status", status)
				}
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}
			var cur, avail string
			if info != nil && info.Current != nil && otaNotesHostOK(info.Current.ReleaseNotesURL) {
				cur = info.Current.ReleaseNotesURL
			}
			if info != nil && info.Available != nil && otaNotesHostOK(info.Available.ReleaseNotesURL) {
				avail = info.Available.ReleaseNotesURL
			}
			cache.put(uid, vehicleID, cur, avail)
			link = cur
			if which == "available" {
				link = avail
			}
			if link == "" {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "no release notes"})
				return
			}
		}
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			writeJSON(w, http.StatusOK, map[string]string{"url": link})
			return
		}
		http.Redirect(w, r, link, http.StatusFound)
	}
}
