package adminhealth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func promBody(rows ...string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rows, ",") + `]}}`
}

func row(labels, v string) string {
	return `{"metric":{` + labels + `},"value":[1792349302.6,"` + v + `"]}`
}

func TestSummary(t *testing.T) {
	var queries []string
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		switch {
		case strings.Contains(q, "rivolt_rivian_results_total"):
			_, _ = w.Write([]byte(promBody(row(`"class":"success"`, "90"), row(`"class":"outage"`, "6"), row(`"class":"rate_limited"`, "4"))))
		case strings.Contains(q, "breaker_state"):
			_, _ = w.Write([]byte(promBody(row("", "2"))))
		case strings.Contains(q, "breaker_trips"):
			_, _ = w.Write([]byte(promBody(row(`"reason":"outage"`, "3"))))
		case strings.Contains(q, "ratelimit_blocked"):
			_, _ = w.Write([]byte(promBody(row(`"class":"poll"`, "7"))))
		case strings.Contains(q, "kubelet_volume_stats"):
			_, _ = w.Write([]byte(promBody(row("", "61.5"))))
		case strings.Contains(q, "cnpg_collector_last_available_backup_timestamp"):
			_, _ = w.Write([]byte(promBody(row("", "58592"))))
		default:
			http.Error(w, "unexpected", 500)
		}
	}))
	defer prom.Close()
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
		 {"labels":{"alertname":"Watchdog","severity":"none"},"startsAt":"2026-10-01T00:00:00Z","status":{"state":"active"}},
		 {"labels":{"alertname":"RivoltPgDiskFilling","severity":"warning","secret":"x"},"startsAt":"2026-10-18T10:00:00Z","status":{"state":"active"}},
		 {"labels":{"alertname":"RivoltDown","severity":"critical"},"startsAt":"2026-10-18T11:00:00Z","status":{"state":"active"}},
		 {"labels":{"alertname":"Suppressed","severity":"critical"},"startsAt":"2026-10-18T11:00:00Z","status":{"state":"suppressed"}}]`))
	}))
	defer am.Close()

	s := New(Config{Version: "v1.2.3", AlertmanagerURL: am.URL + "/", PrometheusURL: prom.URL, Namespace: "rivolt"})
	sum := s.Summary(t.Context())

	if sum.Version != "v1.2.3" {
		t.Errorf("version = %q", sum.Version)
	}
	// Alerts: Watchdog and non-active dropped, critical first, names/severity/since only.
	if sum.Alerts.Status != OK || len(sum.Alerts.Items) != 2 || sum.Alerts.Items[0].Name != "RivoltDown" || sum.Alerts.Items[1].Name != "RivoltPgDiskFilling" {
		t.Errorf("alerts = %+v", sum.Alerts)
	}
	raw, _ := json.Marshal(sum.Alerts)
	if strings.Contains(string(raw), "secret") {
		t.Errorf("alert labels leaked: %s", raw)
	}
	r := sum.Rivian
	if r.Status != OK || r.Results["success"] != 90 || r.ErrorRate == nil || *r.ErrorRate < 0.0999 || *r.ErrorRate > 0.1001 ||
		r.BreakerState == nil || *r.BreakerState != 2 || r.BreakerTrips["outage"] != 3 || r.RateLimited["poll"] != 7 {
		t.Errorf("rivian = %+v", r)
	}
	if sum.Disk.Status != OK || sum.Disk.UsedPct == nil || *sum.Disk.UsedPct != 61.5 {
		t.Errorf("disk = %+v", sum.Disk)
	}
	if sum.Backup.Status != OK || sum.Backup.AgeSeconds == nil || *sum.Backup.AgeSeconds != 58592 {
		t.Errorf("backup = %+v", sum.Backup)
	}
	// Without a database and flags store those sections are unavailable, not errors.
	if sum.Vehicles.Status != Unavailable || sum.KillSwitch.Status != Unavailable {
		t.Errorf("vehicles %+v, kill switch %+v want unavailable", sum.Vehicles, sum.KillSwitch)
	}
	// The queries are scoped to the configured namespace.
	var scoped int
	for _, q := range queries {
		if strings.Contains(q, `namespace="rivolt"`) {
			scoped++
		}
	}
	if scoped != 2 {
		t.Errorf("namespace-scoped queries = %d, want 2 (disk, backup)", scoped)
	}
}

func TestSourcesDownDegradeNeverFail(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 503) }))
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer down.Close()
	defer garbage.Close()
	for name, s := range map[string]*Service{
		"not configured": New(Config{}),
		"503":            New(Config{AlertmanagerURL: down.URL, PrometheusURL: down.URL}),
		"garbage":        New(Config{AlertmanagerURL: garbage.URL, PrometheusURL: garbage.URL}),
		"refused":        New(Config{AlertmanagerURL: "http://127.0.0.1:1", PrometheusURL: "http://127.0.0.1:1"}),
		"nil service":    nil,
	} {
		sum := s.Summary(t.Context())
		for sec, st := range map[string]string{"alerts": sum.Alerts.Status, "rivian": sum.Rivian.Status, "disk": sum.Disk.Status, "backup": sum.Backup.Status} {
			if st != Unavailable {
				t.Errorf("%s: %s = %q, want unavailable", name, sec, st)
			}
		}
		if sum.Alerts.Items == nil && s != nil {
			t.Errorf("%s: alerts.items is null, want []", name)
		}
	}
}

func TestSlowSourceIsBoundedAndCached(t *testing.T) {
	var hits atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	s := New(Config{PrometheusURL: slow.URL})
	start := time.Now()
	sum := s.Summary(t.Context())
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a hung Prometheus held the summary for %v, want about the 3 s section timeout", d)
	}
	if sum.Rivian.Status != Unavailable {
		t.Errorf("rivian = %+v", sum.Rivian)
	}
	n := hits.Load()
	_ = s.Summary(t.Context()) // inside the cache window: no new upstream calls
	if hits.Load() != n {
		t.Errorf("second summary re-queried the source (%d -> %d), want cached", n, hits.Load())
	}
}
