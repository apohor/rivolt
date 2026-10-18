// Package adminhealth builds the read-only "is everything OK?" summary for
// the admin home: firing alerts (Alertmanager), the kill switch, Rivian
// call health over the last 24 h, streaming vs leased vehicles, disk, backup
// age and version.
//
// Every section is independent and degrades to {"status":"unavailable"}
// with a short reason; nothing here can turn the endpoint into a 500. The two
// outside sources (Alertmanager, Prometheus) are reached only at URLs from the
// environment, with fixed paths and fixed queries (no caller input reaches
// them), a short timeout and a capped response size.
package adminhealth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/flags"
)

const (
	sectionTimeout = 3 * time.Second
	maxBody        = 1 << 20
	cacheTTL       = 15 * time.Second
)

// Config wires the sources. Empty URLs mean "not configured": the sections
// that need them read unavailable.
type Config struct {
	DB              *sql.DB
	Flags           *flags.Store
	Version         string
	AlertmanagerURL string // e.g. http://alertmanager-operated.monitoring:9093
	PrometheusURL   string // e.g. http://prometheus-operated.monitoring:9090
	// Namespace scopes the database disk and backup queries to this
	// release's cluster (default "rivolt").
	Namespace string
	Client    *http.Client // nil: a client with a short timeout
}

// Service computes summaries, caching one for a few seconds so a refreshing
// admin page cannot hammer Alertmanager or Prometheus.
type Service struct {
	cfg Config
	now func() time.Time

	mu     sync.Mutex
	cached *Summary
	at     time.Time
}

// New builds the Service.
func New(cfg Config) *Service {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: sectionTimeout}
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "rivolt"
	}
	cfg.AlertmanagerURL = strings.TrimRight(strings.TrimSpace(cfg.AlertmanagerURL), "/")
	cfg.PrometheusURL = strings.TrimRight(strings.TrimSpace(cfg.PrometheusURL), "/")
	return &Service{cfg: cfg, now: time.Now}
}

// Status of one section.
const (
	OK          = "ok"
	Unavailable = "unavailable"
)

// Section is the part every section shares.
type Section struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func unavailable(reason string) Section { return Section{Status: Unavailable, Reason: reason} }

type Alert struct {
	Name     string    `json:"name"`
	Severity string    `json:"severity"`
	Since    time.Time `json:"since"`
}

type Alerts struct {
	Section
	Items []Alert `json:"items"`
}

type KillSwitch struct {
	Section
	Paused bool `json:"paused"`
}

type Rivian struct {
	Section
	Window string `json:"window"`
	// Results counts Rivian calls by outcome class over the window.
	Results map[string]float64 `json:"results"`
	// ErrorRate is the share of calls that were not "success" (0..1); null
	// when there were no calls.
	ErrorRate *float64 `json:"error_rate"`
	// BreakerState: 0 closed, 1 half open, 2 open (worst across pods).
	BreakerState *float64           `json:"breaker_state"`
	BreakerTrips map[string]float64 `json:"breaker_trips"`
	RateLimited  map[string]float64 `json:"rate_limited"`
}

type Vehicles struct {
	Section
	// Leased: vehicles with an unexpired subscription lease. Streaming:
	// vehicles that wrote a state row in the last 10 minutes.
	Leased    int `json:"leased"`
	Streaming int `json:"streaming"`
}

type Disk struct {
	Section
	UsedPct *float64 `json:"used_pct"`
}

type Backup struct {
	Section
	AgeSeconds *float64 `json:"age_seconds"`
}

// Summary is the response body.
type Summary struct {
	GeneratedAt time.Time  `json:"generated_at"`
	Version     string     `json:"version"`
	Alerts      Alerts     `json:"alerts"`
	KillSwitch  KillSwitch `json:"kill_switch"`
	Rivian      Rivian     `json:"rivian"`
	Vehicles    Vehicles   `json:"vehicles"`
	Disk        Disk       `json:"disk"`
	Backup      Backup     `json:"backup"`
}

// Summary returns the summary, from cache when it is fresh. A nil Service
// answers with every section unavailable.
func (s *Service) Summary(ctx context.Context) Summary {
	if s == nil {
		return Summary{
			GeneratedAt: time.Now().UTC(),
			Alerts:      Alerts{Section: unavailable("not configured")}, KillSwitch: KillSwitch{Section: unavailable("not configured")},
			Rivian: Rivian{Section: unavailable("not configured")}, Vehicles: Vehicles{Section: unavailable("not configured")},
			Disk: Disk{Section: unavailable("not configured")}, Backup: Backup{Section: unavailable("not configured")},
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.now().Sub(s.at) < cacheTTL {
		return *s.cached
	}
	sum := s.compute(ctx)
	s.cached, s.at = &sum, s.now()
	return sum
}

func (s *Service) compute(ctx context.Context) Summary {
	sum := Summary{GeneratedAt: s.now().UTC(), Version: s.cfg.Version}
	var wg sync.WaitGroup
	run := func(f func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A panic in one section must not take the page down.
			defer func() { _ = recover() }()
			c, cancel := context.WithTimeout(ctx, sectionTimeout)
			defer cancel()
			f(c)
		}()
	}
	sum.Alerts = Alerts{Section: unavailable("not computed"), Items: []Alert{}}
	sum.KillSwitch = KillSwitch{Section: unavailable("not computed")}
	sum.Rivian = Rivian{Section: unavailable("not computed"), Window: "24h"}
	sum.Vehicles = Vehicles{Section: unavailable("not computed")}
	sum.Disk = Disk{Section: unavailable("not computed")}
	sum.Backup = Backup{Section: unavailable("not computed")}

	run(func(c context.Context) { sum.Alerts = s.alerts(c) })
	run(func(c context.Context) { sum.KillSwitch = s.killSwitch() })
	run(func(c context.Context) { sum.Rivian = s.rivian(c) })
	run(func(c context.Context) { sum.Vehicles = s.vehicles(c) })
	run(func(c context.Context) { sum.Disk = s.disk(c) })
	run(func(c context.Context) { sum.Backup = s.backup(c) })
	wg.Wait()
	return sum
}

// ---- sections --------------------------------------------------------------

func (s *Service) killSwitch() KillSwitch {
	if s.cfg.Flags == nil {
		return KillSwitch{Section: unavailable("flags store not available")}
	}
	return KillSwitch{Section: Section{Status: OK}, Paused: s.cfg.Flags.KillSwitch().Paused}
}

func (s *Service) vehicles(ctx context.Context) Vehicles {
	if s.cfg.DB == nil {
		return Vehicles{Section: unavailable("database not available")}
	}
	var v Vehicles
	// Leased: distinct vehicles of known users holding a live lease.
	if err := s.cfg.DB.QueryRowContext(ctx, `
		SELECT count(DISTINCT l.vehicle_id) FROM subscription_leases l
		 WHERE l.expires_at > now() AND l.vehicle_id <> '' AND l.vehicle_id NOT LIKE 'electrafi-%'`).Scan(&v.Leased); err != nil {
		return Vehicles{Section: unavailable("lease query failed")}
	}
	// Streaming: newest state row per vehicle (one primary-key probe each).
	if err := s.cfg.DB.QueryRowContext(ctx, `
		SELECT count(*) FROM vehicles v
		 WHERE v.rivian_vehicle_id <> '' AND v.rivian_vehicle_id NOT LIKE 'electrafi-%'
		   AND (SELECT at FROM vehicle_state WHERE vehicle_id = v.id ORDER BY at DESC LIMIT 1) > now() - interval '10 minutes'`).Scan(&v.Streaming); err != nil {
		return Vehicles{Section: unavailable("state query failed")}
	}
	v.Section = Section{Status: OK}
	return v
}

func (s *Service) alerts(ctx context.Context) Alerts {
	out := Alerts{Items: []Alert{}}
	if s.cfg.AlertmanagerURL == "" {
		out.Section = unavailable("alertmanager not configured")
		return out
	}
	u := s.cfg.AlertmanagerURL + "/api/v2/alerts?" + url.Values{
		"active": {"true"}, "silenced": {"false"}, "inhibited": {"false"},
	}.Encode()
	var raw []struct {
		Labels   map[string]string `json:"labels"`
		StartsAt time.Time         `json:"startsAt"`
		Status   struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := s.getJSON(ctx, u, &raw); err != nil {
		out.Section = unavailable("alertmanager unreachable")
		return out
	}
	for _, a := range raw {
		name := a.Labels["alertname"]
		// Watchdog is a heartbeat that always fires; it is not news.
		if name == "" || name == "Watchdog" || name == "InfoInhibitor" || (a.Status.State != "" && a.Status.State != "active") {
			continue
		}
		out.Items = append(out.Items, Alert{Name: name, Severity: a.Labels["severity"], Since: a.StartsAt.UTC()})
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if ri, rj := severityRank(out.Items[i].Severity), severityRank(out.Items[j].Severity); ri != rj {
			return ri < rj
		}
		return out.Items[i].Since.Before(out.Items[j].Since)
	})
	out.Section = Section{Status: OK}
	return out
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

func (s *Service) rivian(ctx context.Context) Rivian {
	out := Rivian{Window: "24h", Results: map[string]float64{}, BreakerTrips: map[string]float64{}, RateLimited: map[string]float64{}}
	if s.cfg.PrometheusURL == "" {
		out.Section = unavailable("prometheus not configured")
		return out
	}
	res, err := s.query(ctx, `sum by (class) (increase(rivolt_rivian_results_total[24h]))`)
	if err != nil {
		out.Section = unavailable("prometheus unreachable")
		return out
	}
	var total, ok float64
	for _, r := range res {
		out.Results[r.Labels["class"]] = round(r.Value)
		total += r.Value
		if r.Labels["class"] == "success" {
			ok += r.Value
		}
	}
	if total > 0 {
		e := (total - ok) / total
		out.ErrorRate = &e
	}
	// The rest are best effort: a miss leaves the field empty, the section stays ok.
	if r, err := s.query(ctx, `max(rivolt_rivian_breaker_state)`); err == nil && len(r) == 1 {
		v := r[0].Value
		out.BreakerState = &v
	}
	if r, err := s.query(ctx, `sum by (reason) (increase(rivolt_rivian_breaker_trips_total[24h]))`); err == nil {
		for _, x := range r {
			out.BreakerTrips[x.Labels["reason"]] = round(x.Value)
		}
	}
	if r, err := s.query(ctx, `sum by (class) (increase(rivolt_rivian_ratelimit_blocked_total[24h]))`); err == nil {
		for _, x := range r {
			out.RateLimited[x.Labels["class"]] = round(x.Value)
		}
	}
	out.Section = Section{Status: OK}
	return out
}

func (s *Service) disk(ctx context.Context) Disk {
	if s.cfg.PrometheusURL == "" {
		return Disk{Section: unavailable("prometheus not configured")}
	}
	// The database volume: the busiest PVC of this release's namespace.
	q := fmt.Sprintf(`max(kubelet_volume_stats_used_bytes{namespace=%q,persistentvolumeclaim=~".*-pg-[0-9]+"} / kubelet_volume_stats_capacity_bytes{namespace=%q,persistentvolumeclaim=~".*-pg-[0-9]+"}) * 100`, s.cfg.Namespace, s.cfg.Namespace)
	r, err := s.query(ctx, q)
	if err != nil {
		return Disk{Section: unavailable("prometheus unreachable")}
	}
	if len(r) != 1 {
		return Disk{Section: unavailable("no volume metrics")}
	}
	v := r[0].Value
	return Disk{Section: Section{Status: OK}, UsedPct: &v}
}

func (s *Service) backup(ctx context.Context) Backup {
	if s.cfg.PrometheusURL == "" {
		return Backup{Section: unavailable("prometheus not configured")}
	}
	// The newest backup's own timestamp; the age is worked out here. A
	// cluster that never took one reports 0, which must not read as a backup
	// from 1970.
	q := fmt.Sprintf(`max(cnpg_collector_last_available_backup_timestamp{namespace=%q})`, s.cfg.Namespace)
	r, err := s.query(ctx, q)
	if err != nil {
		return Backup{Section: unavailable("prometheus unreachable")}
	}
	if len(r) != 1 {
		return Backup{Section: unavailable("no backup metrics")}
	}
	if r[0].Value <= 0 {
		return Backup{Section: unavailable("no backup recorded")}
	}
	age := float64(s.now().Unix()) - r[0].Value
	if age < 0 {
		age = 0 // clock skew; a backup cannot be from the future
	}
	return Backup{Section: Section{Status: OK}, AgeSeconds: &age}
}

// ---- outside sources -------------------------------------------------------

type sample struct {
	Labels map[string]string
	Value  float64
}

// query runs an instant PromQL query. The query text is always one of the
// constants above; nothing from a request is ever interpolated.
func (s *Service) query(ctx context.Context, promql string) ([]sample, error) {
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := s.getJSON(ctx, s.cfg.PrometheusURL+"/api/v1/query?"+url.Values{"query": {promql}}.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, errors.New("prometheus: " + body.Status)
	}
	out := make([]sample, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		str, _ := r.Value[1].(string)
		f, err := strconv.ParseFloat(str, 64)
		if err != nil || f != f { // NaN
			continue
		}
		out = append(out, sample{Labels: r.Metric, Value: f})
	}
	return out, nil
}

func (s *Service) getJSON(ctx context.Context, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(into)
}

func round(f float64) float64 { return float64(int64(f + 0.5)) }
