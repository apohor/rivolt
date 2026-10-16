package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/demo"
)

// demoSavedTripIDs resolves the demo user's saved-trip names to ids.
type demoSavedTripIDs func(ctx context.Context) (map[string]uuid.UUID, error)

// handleDemoTrips serves GET /api/demo/trips: the preset trip list for
// the demo planner. The list is embedded data (no upstream); a preset
// that carries "saved_trip_name" gains "saved_trip_id", the id of the
// demo user's saved trip of that name, so the page can call
// trips/saved/{id}/actual. Only the demo user's own trips are looked up;
// a name with no saved trip simply gets no id. lookup may be nil.
func handleDemoTrips(lookup demoSavedTripIDs) http.HandlerFunc {
	var (
		mu     sync.Mutex
		cached demo.PresetBlob
		at     time.Time
	)
	return func(w http.ResponseWriter, r *http.Request) {
		base := demo.PresetsIndex()
		if lookup == nil || !bytes.Contains(base.Body, []byte(`"saved_trip_name"`)) {
			serveDemoBlob(w, r, base)
			return
		}
		mu.Lock()
		blob, fresh := cached, !at.IsZero() && time.Since(at) < 5*time.Minute
		mu.Unlock()
		if !fresh {
			blob = resolveDemoIndex(r.Context(), base, lookup)
			mu.Lock()
			cached, at = blob, time.Now()
			mu.Unlock()
		}
		serveDemoBlob(w, r, blob)
	}
}

// resolveDemoIndex adds saved_trip_id to every entry naming a saved trip.
// On any problem it serves the index unresolved.
func resolveDemoIndex(ctx context.Context, base demo.PresetBlob, lookup demoSavedTripIDs) demo.PresetBlob {
	ids, err := lookup(ctx)
	if err != nil {
		return base
	}
	var doc struct {
		Trips []map[string]json.RawMessage `json:"trips"`
	}
	if json.Unmarshal(base.Body, &doc) != nil {
		return base
	}
	for _, t := range doc.Trips {
		var name string
		if json.Unmarshal(t["saved_trip_name"], &name) != nil || name == "" {
			continue
		}
		if id, ok := ids[name]; ok {
			t["saved_trip_id"], _ = json.Marshal(id.String())
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return base
	}
	sum := sha256.Sum256(out)
	return demo.PresetBlob{Body: out, ETag: `"` + hex.EncodeToString(sum[:8]) + `"`}
}

// handleDemoTripPlan serves GET /api/demo/trips/{id}/plan: the stored
// plan response (the planner's own response shape) for a preset id.
func handleDemoTripPlan(w http.ResponseWriter, r *http.Request) {
	b, ok := demo.PresetPlan(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	serveDemoBlob(w, r, b)
}

func serveDemoBlob(w http.ResponseWriter, r *http.Request, b demo.PresetBlob) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "public, max-age=3600")
	h.Set("ETag", b.ETag)
	h.Set(demoHeader, "1")
	if strings.Contains(r.Header.Get("If-None-Match"), b.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(b.Body)
}
