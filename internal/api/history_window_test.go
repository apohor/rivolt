package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/histwin"
	"github.com/apohor/rivolt/internal/launch"
)

type windowClass struct {
	kind string // "windowed", "proOnly", "exempt" or "todo"
	why  string
}

const (
	always = "always free (plan non-negotiable)"
)

// routeClasses is every method and route of the API that is not covered by a prefix rule below. A new route fails TestEveryRouteIsClassified
// until it is listed here (or falls under a prefix rule) with a reason.
var routeClasses = map[string]windowClass{
	// Windowed: behind historyWindowMiddleware; the response is narrowed to the free window.
	"GET /api/drives":                     {"windowed", ""},
	"GET /api/drives/{id}":                {"windowed", ""},
	"GET /api/drives/{id}/efficiency":     {"windowed", ""},
	"POST /api/drives/{id}/efficiency":    {"windowed", ""},
	"GET /api/drives/{id}/weather":        {"windowed", ""},
	"GET /api/drives/{id}/weather/series": {"windowed", ""},
	"GET /api/charges":                    {"windowed", ""},
	"GET /api/charges/{id}":               {"windowed", ""},
	"GET /api/charges/clusters":           {"windowed", ""},
	"GET /api/charges/to-confirm":         {"windowed", ""},
	"GET /api/activity":                   {"windowed", ""},
	"GET /api/charge-split":               {"windowed", ""},
	"GET /api/chart-points":               {"windowed", ""},

	// Not yet windowed: each moves to "windowed" (or to a Pro-only class) in the PR that does it; billing cannot be turned on until this is empty.
	"GET /api/ledger/monthly":                    {"windowed", ""},
	"GET /api/ledger/monthly.csv":                {"windowed", ""},
	"GET /api/ledger/summary":                    {"windowed", ""},
	"GET /api/ledger/trips":                      {"windowed", ""},
	"GET /api/ledger/savings":                    {"windowed", ""},
	"GET /api/statement-email/preview":           {"windowed", "also limited to a free account's primary vehicle, by the same statementScope the email job uses, so the preview is the email"},
	"GET /api/samples":                           {"windowed", ""},
	"GET /api/sleep-activity":                    {"windowed", ""},
	"GET /api/insights/efficiency":               {"windowed", ""},
	"GET /api/milestones":                        {"exempt", "milestones are free (the milestone share card is a free feature): cumulative achievements the user earned, not browsable history"},
	"GET /api/vehicles/{vehicleID}/drain":        {"exempt", "a fixed 30-day range, inside the free 90-day window; computed per request, cached for a few minutes under a key that is the same for every plan"},
	"GET /api/vehicles/{vehicleID}/pack-health":  {"proOnly", ""},
	"GET /api/vehicles/{vehicleID}/sleep-health": {"exempt", "a fixed 37-day range (this week and the 30 days before), inside the free 90-day window"},
	"GET /api/vehicles/{vehicleID}/trends":       {"proOnly", ""},
	"GET /api/vehicles/{vehicleID}/cold-range":   {"proOnly", ""},
	"GET /api/trips/saved/{id}/actual":           {"proOnly", ""},
	"GET /api/trips/saved/{id}/active":           {"proOnly", ""},
	"POST /api/trips/saved/{id}/link":            {"proOnly", ""},
	"POST /api/trips/saved/{id}/unlink":          {"proOnly", ""},
	"POST /api/trips/overnight-candidates":       {"proOnly", ""},
	"POST /api/push/apns/activity":               {"proOnly", ""},
	"POST /api/push/apns/activity/test":          {"proOnly", ""},

	// The free account's delete-by-range (a privacy control for rows outside its window): a gate item for billing.
	"POST /api/billing/stripe/webhook": {"exempt", "Stripe calls it with a signed event: it writes the entitlement table, returns no history"},
	"POST /api/data/delete-range":      {"exempt", "delete everything before a month, for rows outside the free window: " + always},

	// Exempt, with reasons.
	"GET /api/export":                             {"exempt", always + ": the complete account export"},
	"GET /api/data/backup":                        {"exempt", always + ": the complete backup"},
	"POST /api/data/restore":                      {"exempt", always + ": restoring the user's own backup"},
	"DELETE /api/data/account":                    {"exempt", always + ": account deletion"},
	"DELETE /api/data/sessions":                   {"exempt", always + ": privacy control"},
	"DELETE /api/charges/{id}":                    {"exempt", always + ": deleting a row is a privacy control and works on any row"},
	"PATCH /api/charges/{id}/pricing":             {"exempt", "a correction of the user's own row; works on any row"},
	"POST /api/charges/{id}/pricing/confirm":      {"exempt", "a correction of the user's own row; works on any row"},
	"PUT /api/charges/{id}/place":                 {"exempt", "a correction of the user's own row; works on any row"},
	"PUT /api/charges/{id}/location":              {"exempt", "a correction of the user's own row; works on any row"},
	"POST /api/charges/apply-rate":                {"exempt", "a write over the user's own charges"},
	"POST /api/charges/restamp-place":             {"exempt", "a write over the user's own charges"},
	"POST /api/charges/restamp-place/undo":        {"exempt", "undo of a write over the user's own charges"},
	"PATCH /api/drives/{id}/trailer":              {"exempt", "a correction of the user's own row; works on any row"},
	"POST /api/drives/weather/backfill":           {"exempt", "enrichment job over the user's own drives; returns a count"},
	"POST /api/import/electrafi":                  {"exempt", "imports are free (plan)"},
	"GET /api/year-in-rivolt":                     {"exempt", "the year card stays free; an aggregate of the user's own year, computed server-side"},
	"GET /api/ledger/enabled":                     {"exempt", "a feature flag, no history"},
	"POST /api/milestones/dismiss":                {"exempt", "a write on the user's own milestone"},
	"POST /api/statement-email/unsubscribe":       {"exempt", "an email control; works signed out"},
	"GET /api/statement-email/unsubscribe":        {"exempt", "an email control; works signed out"},
	"GET /api/vehicles":                           {"exempt", "the vehicle list: a free account's other vehicles are listed with pro_locked (handler-level), never hidden"},
	"GET /api/vehicles/owned":                     {"exempt", "the vehicle list"},
	"GET /api/vehicles/{vehicleID}/now":           {"exempt", "live state, not history"},
	"GET /api/vehicles/{vehicleID}/lease":         {"exempt", "lease terms, no history"},
	"GET /api/vehicles/{vehicleID}/lease/status":  {"exempt", "lease status, no history"},
	"PUT /api/vehicles/{vehicleID}/lease":         {"exempt", "a write of the user's own settings"},
	"DELETE /api/vehicles/{vehicleID}/lease":      {"exempt", "a write of the user's own settings"},
	"GET /api/vehicles/{vehicleID}/profile":       {"exempt", "the user's own vehicle profile"},
	"PUT /api/vehicles/{vehicleID}/profile":       {"exempt", "a write of the user's own settings"},
	"GET /api/vehicles/{vehicleID}/ota/notes":     {"exempt", "software release notes, no user history"},
	"GET /api/live-drive/{vehicleID}":             {"exempt", "the drive in progress, not history"},
	"GET /api/live-session/{vehicleID}":           {"exempt", "the session in progress, not history"},
	"GET /api/state/{vehicleID}":                  {"exempt", "live state, not history"},
	"GET /api/state/{vehicleID}/debug":            {"exempt", "live state, not history"},
	"GET /api/state/{vehicleID}/fresh":            {"exempt", "live state, not history"},
	"GET /api/battery-temp/{vehicleID}":           {"exempt", "a live probe of the vehicle now"},
	"GET /api/parallax-raw/{vehicleID}":           {"exempt", "a live probe of the vehicle now"},
	"GET /api/charging-schema":                    {"exempt", "a live probe of the vehicle now"},
	"GET /api/charging-field/{field}":             {"exempt", "a live probe of the vehicle now"},
	"GET /api/charging-frames":                    {"exempt", "a live probe of the vehicle now"},
	"GET /api/charging-networks":                  {"exempt", "the public charging-network catalog, no user history"},
	"GET /api/trips/saved":                        {"exempt", "saved trips are capped by count, not by date: the list marks trips past the free cap read_only (handler-level, see pro_gates.go)"},
	"POST /api/trips/saved":                       {"exempt", "saved trips are capped by count, not by date: a free account at the cap gets 402 pro_required (handler-level)"},
	"PUT /api/trips/saved/{id}":                   {"exempt", "saved trips are capped by count, not by date: a trip past the free cap is read-only, 402 pro_required (handler-level)"},
	"DELETE /api/trips/saved/{id}":                {"exempt", always + ": deleting the user's own trip"},
	"POST /api/trips/plan":                        {"exempt", "the single-day planner is free"},
	"POST /api/trips/plan-multiday":               {"exempt", "gated by what the request asks for (more than one day, an overnight, re-plan from here are Pro): multidayProMiddleware"},
	"POST /api/trips/plan/advice":                 {"exempt", "advice on a plan (a single-day plan is free); the AI consent and budget gate it"},
	"GET /api/trips/plan/diag":                    {"exempt", "operator planner diagnostics, no user plan"},
	"GET /api/app/config":                         {"exempt", "server configuration, no user history"},
	"GET /.well-known/apple-app-site-association": {"exempt", "static, not the API"},
}

// prefixClasses cover whole areas that never return stored history. The longest matching prefix wins; an explicit entry above wins over
// all of them.
var prefixClasses = []struct{ prefix, why string }{
	{"/api/settings/", "the user's own settings: " + always},
	{"/api/auth/", "sign-in and account: " + always},
	{"/api/admin/", "operator-only routes, on the admin host (not user history)"},
	{"/api/maps/", "map services, no stored history"},
	{"/api/push/", "notification subscriptions and switches: " + always},
	{"/api/signup", "sign-up, no history"},
	{"/api/demo", "the public demo, no user history"},
	{"/api/changelog", "public changelog"},
	{"/api/onboarding", "onboarding, no history"},
	{"/api/rivian-expiry-email/", "an email control; works signed out"},
	{"/api/r2-arrival/", "an email control; works signed out"},
	{"/api/weekly-recap/", "an email control; works signed out"},
	{"/api/onboarding-email/", "an email control; works signed out"},
	{"/api/terms", "terms, no history"},
	{"/api/config", "server configuration"},
	{"/api/events", "analytics intake"},
	{"/api/geocode", "geocoding, no stored history"},
	{"/api/health", "health check"},
	{"/api/supported-features", "a live probe of the vehicle now"},
}

// optionalRoutes are mounted only where a feature is configured; the test router may not have them, and they are classified here for the
// installs that do.
var optionalRoutes = map[string]bool{"GET /api/trips/plan/diag": true, "POST /api/billing/stripe/webhook": true}

func classify(method, route string) (windowClass, bool) {
	if c, ok := routeClasses[method+" "+route]; ok {
		return c, true
	}
	best := ""
	var why string
	for _, p := range prefixClasses {
		if strings.HasPrefix(route, p.prefix) && len(p.prefix) > len(best) {
			best, why = p.prefix, p.why
		}
	}
	if best != "" {
		return windowClass{"exempt", why}, true
	}
	return windowClass{}, false
}

func hasMiddlewareNamed(mws []func(http.Handler) http.Handler, name string) bool {
	for _, m := range mws {
		if f := runtime.FuncForPC(reflect.ValueOf(m).Pointer()); f != nil && strings.Contains(f.Name(), name) {
			return true
		}
	}
	return false
}

type routeInfo struct {
	method, route string
	windowed      bool // behind historyWindowMiddleware
	proOnly       bool // behind proOnlyMiddleware
}

func apiRoutes(t *testing.T) []routeInfo {
	t.Helper()
	var out []routeInfo
	err := chi.Walk(contractRouter(t), func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		if route == "/api" || strings.HasPrefix(route, "/api/") || strings.HasPrefix(route, "/.well-known/") {
			out = append(out, routeInfo{method, strings.TrimSuffix(route, "/"), hasMiddlewareNamed(mws, "historyWindowMiddleware"), hasMiddlewareNamed(mws, "proOnlyMiddleware")})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Every route of the API is classified. A route that returns history must be windowed (behind the middleware), Pro-only, or exempt with a
// reason; one that is not listed fails here, so a new route cannot ship unclassified. Exempt routes must NOT carry the window middleware: it
// would silently truncate a privacy path the day a handler reads the context.
func TestEveryRouteIsClassified(t *testing.T) {
	routes := apiRoutes(t)
	if len(routes) < 200 {
		t.Fatalf("only %d routes found: the walk is not seeing the router", len(routes))
	}
	seen := map[string]bool{}
	for _, r := range routes {
		key := r.method + " " + r.route
		seen[key] = true
		c, ok := classify(r.method, r.route)
		if !ok {
			t.Errorf("%s is not classified: add it to routeClasses in history_window_test.go (windowed, exempt with a reason, or todo)", key)
			continue
		}
		switch c.kind {
		case "windowed":
			if !r.windowed {
				t.Errorf("%s is classified windowed but is not registered behind historyWindowMiddleware", key)
			}
		case "proOnly":
			if !r.proOnly {
				t.Errorf("%s is classified proOnly but is not registered behind proOnlyMiddleware", key)
			}
		case "exempt":
			if c.why == "" {
				t.Errorf("%s is exempt without a reason", key)
			}
			if r.windowed || r.proOnly {
				t.Errorf("%s is exempt but carries the window or Pro-only middleware", key)
			}
		case "todo":
			if r.windowed {
				t.Errorf("%s is todo but already windowed: move it to windowed", key)
			}
		}
	}
	for key := range routeClasses {
		if !seen[key] && !optionalRoutes[key] && !strings.HasPrefix(key, "GET /.well-known") {
			t.Errorf("routeClasses lists %s, which is not a route of the API", key)
		}
	}
}

// The promise most likely to break silently: the export, the backup and every privacy path are exempt with the plan's reason, whatever
// else changes in the table.
func TestPrivacyAndExportRoutesAreAlwaysFree(t *testing.T) {
	for _, key := range []string{
		"GET /api/export", "GET /api/data/backup", "DELETE /api/data/account", "DELETE /api/data/sessions", "POST /api/data/delete-range", "DELETE /api/charges/{id}",
	} {
		c, ok := routeClasses[key]
		if !ok || c.kind != "exempt" || !strings.Contains(c.why, always) {
			t.Errorf("%s must be exempt with the reason %q, got %+v", key, always, c)
		}
	}
	for _, rt := range apiRoutes(t) {
		if (rt.route == "/api/export" || strings.HasPrefix(rt.route, "/api/data/")) && rt.windowed {
			t.Errorf("%s %s carries the window middleware", rt.method, rt.route)
		}
	}
	for _, p := range []string{"/api/settings/", "/api/push/", "/api/auth/"} {
		if c, ok := classify("GET", p+"anything"); !ok || c.kind != "exempt" || c.why == "" {
			t.Errorf("%s must fall under an exempt prefix rule: %+v", p, c)
		}
	}
}

// The launch item "history-window" is cleared exactly when no route is todo; they cannot disagree.
func TestWindowCompleteMatchesTheTodoList(t *testing.T) {
	var todo []string
	for key, c := range routeClasses {
		if c.kind == "todo" {
			todo = append(todo, key)
		}
	}
	sort.Strings(todo)
	cleared := launch.Cleared("history-window")
	if cleared && len(todo) > 0 {
		t.Errorf("launch item history-window is cleared but %d routes are still todo: %v", len(todo), todo)
	}
	if !cleared && len(todo) == 0 {
		t.Error("no route is todo: clear the launch item history-window")
	}
}

func TestHistoryWindowMiddleware(t *testing.T) {
	uid := uuid.New()
	var has bool
	h := func(e *entitlements.Service) http.Handler {
		return historyWindowMiddleware(e)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, has = histwin.From(r.Context())
		}))
	}
	req := httptest.NewRequest(http.MethodGet, "/api/drives", nil).WithContext(auth.WithUser(context.Background(), uid))

	// Billing off: Pro for everyone, no window. No service: no window.
	h(entitlements.New(nil, entitlements.Config{})).ServeHTTP(httptest.NewRecorder(), req)
	if has {
		t.Error("billing off: a window was stamped")
	}
	h(nil).ServeHTTP(httptest.NewRecorder(), req)
	if has {
		t.Error("no service: a window was stamped")
	}
}

// The client-supplied range is clamped to the window; a range inside it is left alone.
func TestChartWindowIsClampedToTheFreeWindow(t *testing.T) {
	now := time.Now()
	win := histwin.Free(now)
	ctx := histwin.With(context.Background(), win)
	ask := func(c context.Context, q string) time.Time {
		req := httptest.NewRequest(http.MethodGet, "/api/chart-points"+q, nil).WithContext(c)
		since, _, _, ok := chartWindow(httptest.NewRecorder(), req)
		if !ok {
			t.Fatalf("%s refused", q)
		}
		return since
	}
	old := now.Add(-300 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if got := ask(ctx, "?since="+old); !got.Equal(win.Since) {
		t.Errorf("a range starting 300 days ago was served from %v, want the window start %v", got, win.Since)
	}
	recent := now.Add(-10 * 24 * time.Hour).UTC().Truncate(time.Second)
	if got := ask(ctx, "?since="+recent.Format(time.RFC3339)); !got.Equal(recent) {
		t.Errorf("a range inside the window was moved to %v", got)
	}
	if got := ask(context.Background(), "?since="+old); got.After(now.Add(-299 * 24 * time.Hour)) {
		t.Errorf("with no window the range was clamped: %v", got)
	}
}

func TestMoneyWindowStart(t *testing.T) {
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	chi, _ := time.LoadLocation("America/Chicago")
	win := histwin.With(context.Background(), histwin.Free(now))
	if _, on := moneyWindowStart(context.Background(), now, time.UTC); on {
		t.Error("no window in the context, but the Money window is on")
	}
	from, on := moneyWindowStart(win, now, time.UTC)
	if !on || !from.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("UTC: %v %v, want 2026-09-01 (November, October, September)", from, on)
	}
	// The month is cut in the zone the request asked for: just after midnight UTC on 1 December it is still 30 November in Chicago.
	edge := time.Date(2026, 12, 1, 0, 30, 0, 0, time.UTC)
	if from, _ := moneyWindowStart(win, edge, chi); !from.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, chi)) {
		t.Errorf("Chicago at the month edge: %v, want 2026-09-01 local (it is still November there)", from)
	}
	if from, _ := moneyWindowStart(win, edge, time.UTC); !from.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("UTC at the month edge: %v, want 2026-10-01 (December, November, October)", from)
	}
	// January reaches back across the year.
	if from, _ := moneyWindowStart(win, time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC), time.UTC); !from.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("January: %v, want 2026-11-01", from)
	}
}
