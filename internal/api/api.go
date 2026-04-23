// Package api wires the HTTP surface for Rivolt. It assembles routes,
// middleware, and handler dependencies into a single chi Mux.
package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/electrafi"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/settings"
)

// Deps is the bag of dependencies the API router needs. Keep this
// small; avoid accumulating a "dependency soup" pattern.
type Deps struct {
	Rivian      rivian.Client
	PushService *push.Service
	PushStore   *push.Store
	SettingsMgr *settings.Manager
	Drives      *drives.Store
	Charges     *charges.Store
	Samples     *samples.Store
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

		// Read-only session/telemetry endpoints. Populated by either the
		// ElectraFi importer or the (future) live Rivian ingester.
		r.Get("/drives", handleDrives(d.Drives))
		r.Get("/charges", handleCharges(d.Charges))
		r.Get("/samples", handleSamples(d.Samples))

		// Accepts a multipart upload of an ElectraFi CSV export. Streams
		// it through the importer so users don't have to drop into a
		// terminal to load data.
		r.Post("/import/electrafi", handleImportElectrafi(d))
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

func handleDrives(store *drives.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := store.ListRecent(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []drives.Drive{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleCharges(store *charges.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := store.ListRecent(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []charges.Charge{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleSamples serves raw vehicle_state rows newer than ?since=<rfc3339>
// (default: 24h ago), capped at ?limit= (default 1000, max 10000).
func handleSamples(store *samples.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		since := time.Now().Add(-24 * time.Hour)
		if s := r.URL.Query().Get("since"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				since = t
			}
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := store.ListSince(r.Context(), since, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []samples.Sample{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleImportElectrafi accepts one or more CSV files in a multipart
// upload under the field name "file" and streams each through the
// ElectraFi importer. Returns per-file results as JSON. The upload is
// rejected if any required store is unavailable; we don't want partial
// imports that silently drop samples or charge sessions.
//
// A 1 GiB cap guards against accidental large uploads; ElectraFi
// exports for a single month are typically 30-50 MiB.
func handleImportElectrafi(d Deps) http.HandlerFunc {
	const maxUpload = 1 << 30 // 1 GiB
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Drives == nil || d.Charges == nil || d.Samples == nil {
			http.Error(w, "import unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "parse upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		files := r.MultipartForm.File["file"]
		if len(files) == 0 {
			http.Error(w, "no files uploaded under field 'file'", http.StatusBadRequest)
			return
		}
		imp := &electrafi.Importer{Drives: d.Drives, Charges: d.Charges, Samples: d.Samples}
		if v := r.FormValue("pack_kwh"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				imp.PackKWh = f
			}
		}
		results := make([]electrafi.Result, 0, len(files))
		for _, fh := range files {
			f, err := fh.Open()
			if err != nil {
				http.Error(w, fh.Filename+": open: "+err.Error(), http.StatusBadRequest)
				return
			}
			res, err := imp.ImportReader(r.Context(), fh.Filename, f)
			f.Close()
			if err != nil {
				http.Error(w, fh.Filename+": import: "+err.Error(), http.StatusBadRequest)
				return
			}
			results = append(results, res)
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": results})
	}
}
