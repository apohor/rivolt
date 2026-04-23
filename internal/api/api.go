// Package api wires the HTTP surface for Rivolt. It assembles routes,
// middleware, and handler dependencies into a single chi Mux.
package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// Deps is the bag of dependencies the API router needs. Keep this
// small; avoid accumulating a "dependency soup" pattern.
type Deps struct {
	Rivian      rivian.Client
	PushService *push.Service
	PushStore   *push.Store
	SettingsMgr *settings.Manager
	WebFS       fs.FS
	Version     string
}

// New builds the root mux with all routes mounted.
func New(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		// Self-hosted: we don't know the LAN hostname in advance, and
		// tightening CORS here doesn't add real security because this
		// server isn't exposed to the public internet by default. The
		// operator can put a reverse proxy in front for stricter rules.
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type", "Authorization"},
		MaxAge:         300,
	}))

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", handleHealth(d.Version))

		r.Route("/push", func(r chi.Router) {
			r.Get("/vapid-key", handlePushVAPIDKey(d.PushService))
			r.Post("/subscribe", handlePushSubscribe(d.PushStore))
			r.Post("/unsubscribe", handlePushUnsubscribe(d.PushStore))
		})

		// Stub vehicle routes — return empty arrays until the Rivian
		// client is wired. Lets the web UI render a graceful empty state.
		r.Get("/vehicles", handleVehicles(d.Rivian))
	})

	// Everything else falls through to the embedded SPA.
	r.Handle("/*", spaHandler(d.WebFS))

	return r
}

func handleHealth(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"version": version,
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func handleVehicles(c rivian.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeJSON(w, http.StatusOK, []rivian.Vehicle{})
			return
		}
		vs, err := c.Vehicles(r.Context())
		if err != nil {
			// ErrNotImplemented + any transient failure land here. For
			// the v0 UX we treat both as "no vehicles yet"; a richer
			// diagnostic lives on the Settings page.
			writeJSON(w, http.StatusOK, []rivian.Vehicle{})
			return
		}
		writeJSON(w, http.StatusOK, vs)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
