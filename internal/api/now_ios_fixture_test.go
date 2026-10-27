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
	"github.com/apohor/rivolt/internal/offpeak"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/trips"
	"github.com/apohor/rivolt/internal/vehiclelease"
)

// iosFixtureTargets maps each fixture in ios/RivoltTests/Fixtures to the server type it must decode into. The set is the
// server side of the contract: TestEveryIOSFixtureIsChecked fails for a fixture that is in neither this map nor
// fixturesCheckedElsewhere, so a new fixture cannot skip the check.
func iosFixtureTargets() map[string]any {
	return map[string]any{
		"now_v1.json":         &nowResponse{},
		"now_v1_minimal.json": &nowResponse{},
		"state.json":          &rivian.State{},
		// Captures of what prod sent, kept for the app's tolerance tests.
		"now_prod_shape.json":   &nowResponse{},
		"state_prod_shape.json": &rivian.State{},
		"state_full.json":       &rivian.State{}, // every key the Go type can send, zero values: what the web's VehicleState is checked against
		"vehicles.json":         &[]rivian.Vehicle{},
		// Settings → Charging on the phone (settings_fixtures_test.go regenerates these from the same types).
		"settings_offpeak.json":          &offpeakResponse{},
		"settings_offpeak_empty.json":    &offpeakResponse{},
		"error_offpeak_field.json":       &offpeak.FieldError{},
		"settings_networks.json":         &[]settings.ChargingNetwork{},
		"settings_membership_terms.json": &struct{ Terms []membershipTermJSON }{},
		"settings_planner.json":          &settings.PlannerPrefs{},
		// The trip planner on the phone (trip_fixtures_test.go regenerates these from the same types).
		"trip_plan.json":                  &tripPlanResponse{},
		"trip_plan_rivian_shape.json":     &tripPlanResponse{},
		"trip_plan_multiday.json":         &tripPlanMultidayResponse{},
		"trip_plan_multiday_partial.json": &tripPlanMultidayResponse{},
		"trip_saved_list.json":            &[]trips.SavedTrip{},
		"trip_saved_one.json":             &trips.SavedTrip{},
		"trip_plan_actual_linked.json":    &planActualResponse{},
		"trip_plan_actual_none.json":      &planActualResponse{},
		"trip_plan_actual_multi.json":     &planActualMultiResponse{},
		"app_token.json":                  &appTokenResponse{},
		// Sprint 11: who you are (with the Terms state and the account's units), and the answers the clients read as states.
		"me.json":                     &meResponse{},
		"me_unaccepted.json":          &meResponse{},
		"me_signed_out.json":          &meSignedOut{},
		"error_terms_required.json":   &termsRequiredBody{},
		"error_not_connected.json":    &notConnectedBody{},
		"error_app_refused.json":      &errorBody{},
		"settings_units.json":         &settings.Units{},
		"settings_charging.json":      &settings.ChargingConfig{},
		"settings_home_location.json": &homeLocationResponse{},
		// Settings → Charging on the phone: the off-peak schedule, the networks price book, the networks' membership terms and the
		// planner defaults (the gas inputs are settings_charging.json).
		"settings_offpeak.json":  &offpeakResponse{},
		"settings_networks.json": &[]settings.ChargingNetwork{},
		"settings_membership_terms.json": &struct {
			Terms []membershipTermJSON `json:"terms"`
		}{},
		"settings_planner.json": &settings.PlannerPrefs{},
		"app_config.json":       &appConfigResponse{},
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
		// The demo's live answer, trimmed: microsecond timestamps, a null trailer, an AC charge with no network (the shapes the app's decoder met).
		"activity_live_shape.json": &activityResponse{},
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
		"drain.json":       &drainResponse{},
		"pack_health.json": &packHealthResponse{},
		"trends.json":      &vehicleTrends{},
		// The capacity dots' history (#920): estimates with energy_kwh and soc_delta_pct.
		"trends_capacity_spread.json": &vehicleTrends{},
		"cold_range.json":             &coldRange{},
		"lease_status.json":           &vehiclelease.Status{},
		// The Truck tab's "Where you charge" map: the account's charge clusters (synthetic public points).
		"charge_clusters.json": &[]chargeClusterResponse{},
		// Money's "To confirm" list: the open auto-detected networks, without coordinates.
		"charges_to_confirm.json": &[]toConfirmCharge{},
	}
}

// The iOS app decodes these server responses from fixtures in
// ios/RivoltTests/Fixtures (its unit tests run them through the real
// decoder). Decoding the same files strictly into the server's own types
// means a renamed or removed field the app reads fails here, in Go CI, before
// it reaches a phone. Adding a field doesn't: the app ignores unknown keys.
func TestIOSFixturesMatchServerTypes(t *testing.T) {
	for name, target := range iosFixtureTargets() {
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
