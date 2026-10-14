package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/flags"
)

// Public read-only demo (B7b). When demo.Enabled (RIVOLT_DEMO=true on a
// replay install), a signed-out visitor's GET to a small allowlist of
// read endpoints is served as the demo user, and only for the demo
// vehicle. Nothing else changes: writes, settings, admin and every
// other path stay 401/404 for signed-out requests, a signed-in user is
// never switched, and with the env off the middleware is not installed.

const demoRatePerMin = 120

// demoHeader marks responses served to the anonymous demo visitor.
const demoHeader = "X-Rivolt-Demo"

// demoExactPaths are the allowlisted read endpoints (GET/HEAD only).
// They are exact matches: sub-paths such as /api/charges/clusters or
// /api/ledger/export are NOT included.
var demoExactPaths = map[string]bool{
	"/api/vehicles":               true,
	"/api/ledger/enabled":         true,
	"/api/ledger/monthly":         true,
	"/api/ledger/summary":         true,
	"/api/charges":                true,
	"/api/drives":                 true,
	"/api/trips":                  true,
	"/api/sleep-activity":         true,
	"/api/charges/clusters":       true,
	"/api/settings/charging":      true, // GET only: home rate for the demo cost labels
	"/api/settings/home-location": true,
}

// demoAllowed reports whether method+path may be served to an
// anonymous visitor as the demo user. vehicleID is the demo vehicle's
// Rivian id: per-vehicle routes are allowed for that id only.
func demoAllowed(method, path, vehicleID string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	p := strings.TrimSuffix(path, "/")
	if demoExactPaths[p] {
		return true
	}
	return p == "/api/state/"+vehicleID ||
		p == "/api/vehicles/"+vehicleID+"/now" ||
		p == "/api/vehicles/"+vehicleID+"/pack-health"
}

// demoAccessMW serves allowlisted anonymous requests as the demo user.
// It runs after authentication resolves the real identity and before
// impersonation, so a real session always wins and impersonation sees
// only the (non-admin) demo user.
func demoAccessMW(uid uuid.UUID, vehicleID string, limiter *ipLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, signedIn := auth.UserFromContext(r.Context()); signedIn || !demoAllowed(r.Method, r.URL.Path, vehicleID) {
				next.ServeHTTP(w, r)
				return
			}
			limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(demoHeader, "1")
				next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), uid)))
			})).ServeHTTP(w, r)
		})
	}
}

// handleDemoInfo serves GET /api/demo: where the SPA finds the demo
// vehicle. Registered only when the demo is enabled (404 otherwise).
func handleDemoInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    true,
		"vehicle_id": demo.VehicleID(),
		"home_label": demo.HomeLabel,
	})
}

// moneyTabEnabledOrDemo is moneyTabEnabled plus the demo user while
// the demo is on, so the anonymous demo sees the Money tab without a
// per-user flag write.
func moneyTabEnabledOrDemo(store *flags.Store) func(string) bool {
	base := moneyTabEnabled(store)
	return func(userID string) bool {
		if demo.Enabled() && userID == demo.UserID().String() {
			return true
		}
		return base(userID)
	}
}

func newDemoLimiter() *ipLimiter { return newIPLimiter(demoRatePerMin, time.Minute, time.Hour) }
