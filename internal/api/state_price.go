package api

import (
	"net/http"
	"strings"

	"github.com/apohor/rivolt/internal/stateprice"
)

// handleStatePrice answers GET /api/settings/charging/state-price?country=US&state=CA with the state's average residential price and where
// the number comes from (the setup's one-tap default for the home rate). The client sends the state it got from the home search; no
// coordinates or address reach this route and nothing is stored. Outside the US, or for an unknown state, there is no default: 404.
func handleStatePrice(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !strings.EqualFold(strings.TrimSpace(q.Get("country")), "US") || len(q.Get("state")) > 40 {
		http.Error(w, "no default for this place", http.StatusNotFound)
		return
	}
	p, ok := stateprice.Lookup(q.Get("state"))
	if !ok {
		http.Error(w, "no default for this place", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, http.StatusOK, p)
}
