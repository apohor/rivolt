package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/demo"
)

// handleDemoTrips serves GET /api/demo/trips: the preset trip list for
// the demo planner. Static embedded data: no database, no upstream.
func handleDemoTrips(w http.ResponseWriter, r *http.Request) {
	serveDemoBlob(w, r, demo.PresetsIndex())
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
