package api

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/settings"
)

// handleHealthz is the kubelet liveness endpoint: synchronous, no
// I/O, no dependency checks. The only failure mode is "the HTTP
// stack itself wedged" — which kubelet should respond to by
// restarting the container. Anything more (DB ping, etc.) belongs
// on /readyz so a transient downstream outage doesn't restart-loop
// every pod.
func handleHealthz() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}
}

// handleReadyz is the kubelet readiness endpoint. Pings Postgres
// with a 500ms timeout — every request touches it, so a pod with no
// DB cannot meaningfully serve. Failing here pulls the pod from the
// Service's endpoints (no traffic) but does NOT trigger a restart;
// the pod recovers as soon as Postgres responds again. Keep the
// dep set MINIMAL: each thing added here is a vector for compound
// failure (one flaky dep evicts every pod simultaneously). Today
// it's just Postgres. Redis / Hydra / Rivian gateway are
// intentionally NOT checked — those are degrade-but-still-serve
// dependencies.
func handleReadyz(pool *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			// No DB wired (single-tenant docker-compose UX). Treat
			// as ready — the binary works without persistence.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready (no db)\n"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()
		if err := pool.PingContext(ctx); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("postgres ping failed: " + err.Error() + "\n"))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	}
}

func handleHealth(version string) http.HandlerFunc {
	// The retag flow attaches vX.Y.Z to a main-built image without
	// rebuilding, so the binary's compile-time VERSION stamp still
	// reflects the main commit a retag was based on. The Helm chart
	// projects the actual deployed tag (.Values.image.tag) into the
	// pod via the downward API at /etc/podinfo/image-tag. Read it
	// once and prefer it when present; fall back to the build-time
	// stamp for local dev / raw docker run where there's no chart
	// in the loop.
	effective := version
	if b, err := os.ReadFile("/etc/podinfo/image-tag"); err == nil {
		if tag := strings.TrimSpace(string(b)); tag != "" {
			effective = tag
		}
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"version": effective,
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// handleConfig advertises optional runtime knobs to the SPA. Today
// it returns whether the same-origin map proxies are mounted; the
// SPA uses the paths it returns as base URLs for Valhalla
// (/trace_route, /route) and PMTiles (drive map basemap), falling
// back to a raw chord polyline when no snapping engine is wired.
// Public so the SPA can fetch it without a session.
func handleConfig(valhallaEnabled, tilesEnabled, aiEnabled, impersonationEnabled bool, flagsStore *flags.Store, settingsMgr *settings.Manager) http.HandlerFunc {
	type tilesCfg struct {
		// URL is the full same-origin URL of the served basemap
		// .pmtiles file (empty when not configured). protomaps-leaflet
		// fetches this with byte-range reads.
		URL string `json:"url,omitempty"`
		// ChargersURL is the same-origin URL of the chargers POI
		// .pmtiles archive (built from Geofabrik North America +
		// osmium + tippecanoe -- see apps/tiles/manifests/
		// chargers.yaml in rivolt-infra). Empty when the chargers
		// archive isn't deployed; the SPA then falls back to the
		// basemap pois layer for nearest-charger lookup, which is
		// less accurate (planet build strips POI tags down to
		// name/kind/min_zoom).
		ChargersURL string `json:"chargers_url,omitempty"`
	}
	type aiCfg struct {
		// Enabled is true when the install has at least one
		// AI provider configured with a working key+model. The
		// SPA reads it to gate AI-powered features (trip recap,
		// future weekly digest, etc.) so we don't render dead
		// buttons. Snapshot at /api/config request time -- a
		// follow-up Settings save flips this on the next page
		// reload.
		Enabled bool `json:"enabled"`
	}
	type featuresCfg struct {
		// TripPlannerEnabled gates the Plan nav link and route on
		// the SPA. Polled value, so flipping the admin toggle
		// takes effect on next page load (or whenever the SPA
		// re-fetches /api/config).
		TripPlannerEnabled bool `json:"trip_planner_enabled"`
		// ImpersonationEnabled gates the admin "View as user" control.
		// False when RIVOLT_IMPERSONATION_DISABLED is set, so the SPA
		// hides the button on installs that turned the feature off.
		ImpersonationEnabled bool `json:"impersonation_enabled"`
	}
	type valhallaCfg struct {
		// Path is the same-origin URL prefix for Valhalla's HTTP
		// API. Empty means the proxy isn't wired and Valhalla
		// shouldn't be offered as an engine option.
		Path string `json:"path,omitempty"`
	}
	type grafanaCfg struct {
		// BaseURL is the operator's Grafana origin (e.g.
		// "https://grafana.rivolt.dev"). Surfaced so the admin user
		// drawer can deep-link to Explore queries scoped to a
		// user_id + time range. Empty hides the links.
		BaseURL string `json:"base_url,omitempty"`
	}
	type gpsCfg struct {
		// MissingPct, StaleSec, JumpCount drive the "Low GPS accuracy"
		// pill on the drive detail page. Surfaced here so the SPA
		// reads them once on boot instead of re-querying per drive.
		MissingPct float64 `json:"missing_pct"`
		StaleSec   int     `json:"stale_sec"`
		JumpCount  int     `json:"jump_count"`
	}
	type bookingCfg struct {
		// AffiliateID is the operator's Booking.com partner ID
		// (aid query param). Empty means we still deep-link to
		// Booking.com search but don't capture any commission.
		// Sourced from RIVOLT_BOOKING_AFFILIATE_ID env.
		AffiliateID string `json:"affiliate_id,omitempty"`
	}
	type cfg struct {
		Valhalla valhallaCfg `json:"valhalla"`
		Tiles    tilesCfg    `json:"tiles"`
		AI       aiCfg       `json:"ai"`
		Features featuresCfg `json:"features"`
		GPS      gpsCfg      `json:"gps"`
		Grafana  grafanaCfg  `json:"grafana"`
		Booking  bookingCfg  `json:"booking"`
	}
	base := cfg{
		Grafana: grafanaCfg{BaseURL: strings.TrimRight(os.Getenv("RIVOLT_GRAFANA_BASE_URL"), "/")},
		Booking: bookingCfg{AffiliateID: strings.TrimSpace(os.Getenv("RIVOLT_BOOKING_AFFILIATE_ID"))},
	}
	if valhallaEnabled {
		base.Valhalla.Path = "/api/maps/valhalla"
	}
	if tilesEnabled {
		base.Tiles.URL = "/api/maps/tiles/us.pmtiles"
		// chargers.pmtiles lives next to us.pmtiles on the same
		// PVC and is served by the same nginx, so its presence is
		// gated on the same flag. If the chargers Job hasn't run
		// yet, the URL still resolves; the SPA's PMTiles client
		// will see a 404 on the file root and gracefully fall back.
		base.Tiles.ChargersURL = "/api/maps/tiles/chargers.pmtiles"
	}
	base.AI.Enabled = aiEnabled
	base.Features.ImpersonationEnabled = impersonationEnabled
	return func(w http.ResponseWriter, _ *http.Request) {
		c := base
		if flagsStore != nil {
			c.Features.TripPlannerEnabled = flagsStore.TripPlanner().Enabled
		}
		if settingsMgr != nil {
			g := settingsMgr.GPSPublic()
			c.GPS.MissingPct = g.MissingPct
			c.GPS.StaleSec = g.StaleSec
			c.GPS.JumpCount = g.JumpCount
		} else {
			c.GPS.MissingPct = settings.DefaultGPSMissingPct
			c.GPS.StaleSec = settings.DefaultGPSStaleSec
			c.GPS.JumpCount = settings.DefaultGPSJumpCount
		}
		writeJSON(w, http.StatusOK, c)
	}
}
