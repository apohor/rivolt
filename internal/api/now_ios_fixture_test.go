package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/insights"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/vehiclelease"
)

// The iOS app decodes these server responses from fixtures in
// ios/RivoltTests/Fixtures (its unit tests run them through the real
// decoder). Decoding the same files strictly into the server's own types
// means a renamed or removed field the app reads fails here, in Go CI, before
// it reaches a phone. Adding a field doesn't: the app ignores unknown keys.
func TestIOSFixturesMatchServerTypes(t *testing.T) {
	for name, target := range map[string]any{
		"now_v1.json":         &nowResponse{},
		"now_v1_minimal.json": &nowResponse{},
		"state.json":          &rivian.State{},
		"vehicles.json":       &[]rivian.Vehicle{},
		"app_token.json":      &appTokenResponse{},
		// Slice B (the Home charts): the idle-awake bars, as the web's tile reads them.
		"sleep_activity.json": &[]samples.DayActivity{},
		// The miles-per-day and charge-level charts. The handler's response is an
		// anonymous struct; its two keys and row types are pinned here.
		// Activity → Efficiency: the breakdowns by temperature, speed and drive mode (aggregates only).
		"efficiency.json": &insights.Efficiency{},
		"chart_points.json": &struct {
			Drives  []drives.ChartDrive   `json:"drives"`
			Charges []charges.ChartCharge `json:"charges"`
		}{},
		// Slice C: milestones and the "Three things" card (lease set? statement
		// email offered and on?).
		"milestones.json": &struct {
			Milestones []milestoneCard `json:"milestones"`
		}{},
		"lease_terms.json":     &vehiclelease.Terms{},
		"statement_email.json": &statementSettings{},
		// Slice D: where you charged, Home / Away / Fast, without coordinates.
		"charge_split.json": &chargeSplit{},
		// Money: the month's statement (receipts keep billed vs estimated:
		// cost_source), the months summary, and the off-peak savings card
		// with and without a figure.
		"ledger_monthly.json":       &ledger.Monthly{},
		"ledger_summary.json":       &ledgerSummaryResponse{},
		"ledger_savings.json":       &savingsResponse{},
		"ledger_savings_needs.json": &savingsResponse{},
		// Activity (item E): drives (one merged round trip) and charges for a
		// window, with no coordinates.
		"activity.json": &activityResponse{},
		// The Now card's detail (Sprint 7 slice A): the same calls as the web
		// Home page's LivePanel.
		"live_session.json":      &liveSessionResponse{},
		"live_session_home.json": &liveSessionResponse{},
		"live_drive.json":        &rivian.LiveDrive{},
		"drives_last.json":       &[]driveResponse{},
		"charges_last.json":      &[]chargeResponse{},
		// GET /api/charges/{id}: one charge with its figures (the Session card): a DC fast
		// charge with a thermal figure and a limit, and a home AC charge with neither.
		"charge_one.json":    &chargeResponse{},
		"charge_one_ac.json": &chargeResponse{},
		// The Truck tab (item G): parked drain, pack health, the monthly trends, the
		// cold-weather range and the lease status.
		"drain.json":        &drainResponse{},
		"pack_health.json":  &packHealthResponse{},
		"trends.json":       &vehicleTrends{},
		"cold_range.json":   &coldRange{},
		"lease_status.json": &vehiclelease.Status{},
		// The Truck tab's "Where you charge" map: the account's charge clusters (synthetic public points).
		"charge_clusters.json": &[]chargeClusterResponse{},
		// Money's "To confirm" list: the open auto-detected networks, without coordinates.
		"charges_to_confirm.json": &[]toConfirmCharge{},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "ios", "RivoltTests", "Fixtures", name))
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.DisallowUnknownFields()
			if err := dec.Decode(target); err != nil {
				t.Fatalf("fixture %s no longer matches the server type: %v", name, err)
			}
		})
	}
}

// A capacity sample carries exactly these keys: the estimate, and the two figures it came from (the chart's tooltip).
func TestTrendCapacitySampleKeys(t *testing.T) {
	b, err := json.Marshal(trendCapacitySample{At: time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC), KWhEffect: 121.5, EnergyKWh: 66.8, SoCDeltaPct: 55})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if got := strings.Join(keys, ","); got != "at,energy_kwh,kwh_effective,soc_delta_pct" {
		t.Errorf("capacity sample keys = %s", got)
	}
	if m["energy_kwh"] != 66.8 || m["soc_delta_pct"] != float64(55) {
		t.Errorf("values = %v", m)
	}
}
