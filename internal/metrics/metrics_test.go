package metrics

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/launchnotice"
)

// TestHTTPMiddlewareUsesRoutePattern is the load-bearing assertion
// for the metrics design: the `route` label must be the chi route
// pattern (low cardinality), NEVER the raw URL (unbounded). If chi
// upgrades break that assumption this test fails loudly before the
// metric pipeline DOSes itself in prod.
func TestHTTPMiddlewareUsesRoutePattern(t *testing.T) {
	m := New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware)
	r.Get("/state/{vehicleID}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/state/abc-123", nil)
	r.ServeHTTP(rec, req.WithContext(context.Background()))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Scrape the registry through the same handler the scraper sees.
	scrape := httptest.NewRecorder()
	m.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := scrape.Body.String()

	// Must contain the pattern, not the raw vehicle ID.
	if !strings.Contains(body, `route="/state/{vehicleID}"`) {
		t.Errorf("expected route pattern label in metrics, got body:\n%s", body)
	}
	if strings.Contains(body, "abc-123") {
		t.Errorf("raw URL leaked into metric labels (cardinality bomb): %s", body)
	}
}

func TestHTTPMiddlewareUnmatchedRoute(t *testing.T) {
	m := New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware)
	// One real route so chi's middleware chain runs even for misses
	// — without any registered route chi short-circuits before the
	// chain executes, which is fine in practice (the real app
	// always has routes mounted) but not what we're testing here.
	r.Get("/known", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope/whatever", nil))

	scrape := httptest.NewRecorder()
	m.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := scrape.Body.String()

	if !strings.Contains(body, `route="unmatched"`) {
		t.Errorf("expected unmatched route bucket, got body:\n%s", body)
	}
}

func TestRegisterDBPoolExposesSaturation(t *testing.T) {
	var m *Metrics
	m.RegisterDBPool("system", nil) // a nil Metrics or pool is a no-op
	m = New()
	m.RegisterDBPool("system", nil)
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "rivolt_db_pool_") {
			t.Fatalf("a nil pool registered %s", f.GetName())
		}
	}
}

func TestRegisterDBPoolReportsTheLimit(t *testing.T) {
	d, err := sql.Open("pgx", "postgres://nobody@127.0.0.1:1/none") // lazy: nothing connects
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetMaxOpenConns(7)
	m := New()
	m.RegisterDBPool("system", d)
	got := map[string]float64{}
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), "rivolt_db_pool_") {
			continue
		}
		for _, mm := range f.GetMetric() {
			if len(mm.GetLabel()) != 1 || mm.GetLabel()[0].GetValue() != "system" {
				t.Fatalf("%s labels = %v, want only pool=system", f.GetName(), mm.GetLabel())
			}
			if mm.Gauge != nil {
				got[f.GetName()] = mm.Gauge.GetValue()
			} else {
				got[f.GetName()] = mm.Counter.GetValue()
			}
		}
	}
	if got["rivolt_db_pool_max_open"] != 7 {
		t.Fatalf("max_open = %v, want 7 (%v)", got["rivolt_db_pool_max_open"], got)
	}
	for _, n := range []string{"rivolt_db_pool_in_use", "rivolt_db_pool_wait_total", "rivolt_db_pool_wait_seconds_total"} {
		if _, ok := got[n]; !ok {
			t.Errorf("%s not exposed", n)
		}
	}
}

// The lease counters exist at 0 from boot: an alert on increase() needs the series to be there before
// its first increment.
func TestLeaseCountersExistFromBoot(t *testing.T) {
	m := New()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, kind := range LeaseFencedKinds {
		if !strings.Contains(body, `rivolt_lease_fenced_total{kind="`+kind+`"} 0`) {
			t.Errorf("rivolt_lease_fenced_total{kind=%q} is not exported at 0 from boot", kind)
		}
	}
	for _, outcome := range []string{"ok", "retried_ok", "dropped", "error"} {
		if !strings.Contains(body, `rivolt_rivian_schedule_read_total{outcome="`+outcome+`"} 0`) {
			t.Errorf("rivolt_rivian_schedule_read_total{outcome=%q} is not exported at 0 from boot", outcome)
		}
	}
	for _, state := range []string{"active", "idle"} {
		if !strings.Contains(body, `rivolt_lease_acquire_to_frame_seconds_count{state="`+state+`"} 0`) {
			t.Errorf("rivolt_lease_acquire_to_frame_seconds{state=%q} is not exported at 0 from boot", state)
		}
	}
	for _, source := range []string{"db", "local"} {
		if !strings.Contains(body, `rivolt_lease_fence_would_reject_total{source="`+source+`"} 0`) {
			t.Errorf("rivolt_lease_fence_would_reject_total{source=%q} is not exported at 0 from boot", source)
		}
	}
	for _, choice := range []string{"idle", "awake", "deferred"} {
		if !strings.Contains(body, `rivolt_lease_shed_total{choice="`+choice+`"} 0`) {
			t.Errorf("rivolt_lease_shed_total{choice=%q} is not exported at 0 from boot", choice)
		}
	}
}

// rivolt_dated_notices_total is exported at 0 for every (kind, outcome) the dated jobs can report, before any send: a series that first
// appears at 1 has increase() == 0, and the alert on a late founding-year notice would miss the very event it is for.
func TestDatedNoticesSeriesExistAtZero(t *testing.T) {
	m := New()
	fams, err := m.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range fams {
		if f.GetName() != "rivolt_dated_notices_total" {
			continue
		}
		for _, mm := range f.GetMetric() {
			var kind, outcome string
			for _, l := range mm.GetLabel() {
				switch l.GetName() {
				case "kind":
					kind = l.GetValue()
				case "outcome":
					outcome = l.GetValue()
				}
			}
			got[kind+"/"+outcome] = mm.GetCounter().GetValue()
		}
	}
	for _, kind := range launchnotice.MetricKinds {
		for _, outcome := range launchnotice.Outcomes {
			v, ok := got[kind+"/"+outcome]
			if !ok || v != 0 {
				t.Errorf("rivolt_dated_notices_total{kind=%q,outcome=%q} = %v (present %v), want 0 before any send", kind, outcome, v, ok)
			}
		}
	}
	if len(got) != len(launchnotice.MetricKinds)*len(launchnotice.Outcomes) {
		t.Errorf("%d series, want %d", len(got), len(launchnotice.MetricKinds)*len(launchnotice.Outcomes))
	}
	for _, want := range []string{"founding_end/late", "annual_renewal/late"} {
		if _, ok := got[want]; !ok {
			t.Errorf("the alerted series %s is missing", want)
		}
	}
}
