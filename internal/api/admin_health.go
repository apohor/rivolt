package api

import (
	"net/http"

	"github.com/apohor/rivolt/internal/adminhealth"
)

// handleAdminHealthSummary — GET /api/admin/health-summary (admin, read-only).
// The admin home's "is everything OK?" data. Each section degrades to
// {"status":"unavailable"} on its own; the endpoint itself always answers 200.
func handleAdminHealthSummary(s *adminhealth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, s.Summary(r.Context()))
	}
}
