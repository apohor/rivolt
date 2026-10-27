// Contract check C: the web's TypeScript types against the server responses the iPhone app's fixtures capture.
//
// Each fixture (ios/RivoltTests/Fixtures, also decoded strictly by the Go tests) is assigned to the type this app uses for that
// endpoint. `tsc` (the typecheck CI job) then fails when a type requires a field the server no longer sends, or types it
// differently. Nothing runs: the file only has to compile. Fixtures the web has no type for are listed in iosOnlyFixtures with why.
import chargeClusters from "../../../ios/RivoltTests/Fixtures/charge_clusters.json";
import chargesLast from "../../../ios/RivoltTests/Fixtures/charges_last.json";
import coldRange from "../../../ios/RivoltTests/Fixtures/cold_range.json";
import drain from "../../../ios/RivoltTests/Fixtures/drain.json";
import drivesLast from "../../../ios/RivoltTests/Fixtures/drives_last.json";
import leaseStatus from "../../../ios/RivoltTests/Fixtures/lease_status.json";
import leaseTerms from "../../../ios/RivoltTests/Fixtures/lease_terms.json";
import liveSession from "../../../ios/RivoltTests/Fixtures/live_session.json";
import me from "../../../ios/RivoltTests/Fixtures/me.json";
import milestones from "../../../ios/RivoltTests/Fixtures/milestones.json";
import nowV1 from "../../../ios/RivoltTests/Fixtures/now_v1.json";
import settingsCharging from "../../../ios/RivoltTests/Fixtures/settings_charging.json";
import settingsHomeLocation from "../../../ios/RivoltTests/Fixtures/settings_home_location.json";
import settingsUnits from "../../../ios/RivoltTests/Fixtures/settings_units.json";
import sleepHealth from "../../../ios/RivoltTests/Fixtures/sleep_health.json";
import stateFull from "../../../ios/RivoltTests/Fixtures/state_full.json";
import statementEmail from "../../../ios/RivoltTests/Fixtures/statement_email.json";
import trends from "../../../ios/RivoltTests/Fixtures/trends.json";
import vehicles from "../../../ios/RivoltTests/Fixtures/vehicles.json";
import chargeOne from "../../../ios/RivoltTests/Fixtures/charge_one.json";
import chargeOneAc from "../../../ios/RivoltTests/Fixtures/charge_one_ac.json";
import efficiency from "../../../ios/RivoltTests/Fixtures/efficiency.json";
import ledgerMonthly from "../../../ios/RivoltTests/Fixtures/ledger_monthly.json";
import ledgerSummary from "../../../ios/RivoltTests/Fixtures/ledger_summary.json";
import ledgerSavings from "../../../ios/RivoltTests/Fixtures/ledger_savings.json";
import ledgerSavingsFlat from "../../../ios/RivoltTests/Fixtures/ledger_savings_flat.json";
import ledgerSavingsNeeds from "../../../ios/RivoltTests/Fixtures/ledger_savings_needs.json";
import liveDrive from "../../../ios/RivoltTests/Fixtures/live_drive.json";
import liveSessionHome from "../../../ios/RivoltTests/Fixtures/live_session_home.json";
import meUnaccepted from "../../../ios/RivoltTests/Fixtures/me_unaccepted.json";
import nowMinimal from "../../../ios/RivoltTests/Fixtures/now_v1_minimal.json";
import packHealth from "../../../ios/RivoltTests/Fixtures/pack_health.json";
import sleepActivity from "../../../ios/RivoltTests/Fixtures/sleep_activity.json";
import trendsSpread from "../../../ios/RivoltTests/Fixtures/trends_capacity_spread.json";
import type {
  DayActivity, EfficiencyInsights, LiveDrive, PackHealthResponse,
  AccountUnits, AuthUser, Charge, ChargeCluster, ChargingSettings, ColdRange, DrainSnapshot, Drive, HomeLocation,
  LeaseStatus, LeaseTerms, LiveSession, MilestoneItem, NowSnapshot, SleepHealth, StatementEmailSettings, TruckTrends, Vehicle,
  VehicleState,
} from "../lib/api";
import type { LedgerMonth, LedgerSummary } from "../lib/ledger";
import type { SavingsResponse } from "../lib/savings";

// Fixtures are JSON, so every string is `string` and a union such as "mi" | "km" would never match. Wide<T> compares what the
// contract is about: which fields exist, which are required, and whether each is a string, number, boolean, array or object.
// The values themselves are pinned on the Go side.
type Wide<T> = T extends string
  ? string
  : T extends number
    ? number
    : T extends boolean
      ? boolean
      : T extends (infer U)[]
        ? Wide<U>[]
        : T extends object
          ? { [K in keyof T]: Wide<T[K]> }
          : T;

export const _chargeClusters: Wide<ChargeCluster[]> = chargeClusters;
export const _charges: Wide<Charge[]> = chargesLast;
export const _coldRange: Wide<ColdRange> = coldRange;
export const _drain: Wide<DrainSnapshot> = drain;
export const _drives: Wide<Drive[]> = drivesLast;
export const _leaseStatus: Wide<LeaseStatus> = leaseStatus;
export const _leaseTerms: Wide<LeaseTerms> = leaseTerms;
export const _liveSession: Wide<LiveSession> = liveSession;
export const _me: Wide<AuthUser> = me;
export const _milestones: Wide<{ milestones: MilestoneItem[] }> = milestones;
export const _now: Wide<NowSnapshot> = nowV1;
export const _settingsCharging: Wide<ChargingSettings> = settingsCharging;
export const _settingsHome: Wide<HomeLocation> = settingsHomeLocation;
export const _settingsUnits: Wide<AccountUnits> = settingsUnits;
export const _sleepHealth: Wide<SleepHealth> = sleepHealth;
export const _state: Wide<VehicleState> = stateFull;
export const _statementEmail: Wide<StatementEmailSettings> = statementEmail;
export const _trends: Wide<TruckTrends> = trends;
export const _vehicles: Wide<Vehicle[]> = vehicles;
export const _chargeOne: Wide<Charge> = chargeOne;
export const _chargeOneAc: Wide<Charge> = chargeOneAc;
export const _efficiency: Wide<EfficiencyInsights> = efficiency;
export const _ledgerMonthly: Wide<LedgerMonth> = ledgerMonthly;
export const _ledgerSummary: Wide<LedgerSummary> = ledgerSummary;
export const _ledgerSavings: Wide<SavingsResponse> = ledgerSavings;
export const _ledgerSavingsFlat: Wide<SavingsResponse> = ledgerSavingsFlat;
export const _ledgerSavingsNeeds: Wide<SavingsResponse> = ledgerSavingsNeeds;
export const _liveDrive: Wide<LiveDrive> = liveDrive;
export const _liveSessionHome: Wide<LiveSession> = liveSessionHome;
export const _meUnaccepted: Wide<AuthUser> = meUnaccepted;
export const _nowMinimal: Wide<NowSnapshot> = nowMinimal;
export const _packHealth: Wide<PackHealthResponse> = packHealth;
export const _sleepActivity: Wide<DayActivity[]> = sleepActivity;
export const _trendsSpread: Wide<TruckTrends> = trendsSpread;

// Fixtures with no web type to check, each with why. The guard in fixtures.contract.test.ts fails for a fixture that is neither
// imported above nor listed here, so a new fixture cannot skip this check.
export const iosOnlyFixtures: Record<string, string> = {
  "demo_trips.json": "the demo's sample trip list, the embedded bytes verbatim (demo_trip_fixtures_test.go); the iPhone's demo Plan tab opens them",
  "demo_trip_plan_single.json": "a demo sample trip's stored plan, the embedded bytes verbatim (demo_trip_fixtures_test.go)",
  "demo_trip_plan_two_days.json": "a demo sample trip's stored two-day plan, the embedded bytes verbatim (demo_trip_fixtures_test.go)",
  "activity.json": "the activity feed is an iPhone screen; the web has no consumer",
  "settings_offpeak.json": "the iPhone's Settings → Charging cards (generated from the server types, settings_fixtures_test.go); the web reads these through its own types in lib/api.ts and lib/offpeak.ts",
  "settings_offpeak_empty.json": "the iPhone's Settings → Charging cards (generated from the server types, settings_fixtures_test.go); the web reads these through its own types in lib/api.ts and lib/offpeak.ts",
  "error_offpeak_field.json": "the iPhone's Settings → Charging cards (generated from the server types, settings_fixtures_test.go); the web reads these through its own types in lib/api.ts and lib/offpeak.ts",
  "settings_networks.json": "the iPhone's Settings → Charging cards (generated from the server types, settings_fixtures_test.go); the web reads these through its own types in lib/api.ts and lib/offpeak.ts",
  "settings_membership_terms.json": "the iPhone's Settings → Charging cards (generated from the server types, settings_fixtures_test.go); the web reads these through its own types in lib/api.ts and lib/offpeak.ts",
  "settings_planner.json": "the iPhone's Settings → Charging cards (generated from the server types, settings_fixtures_test.go); the web reads these through its own types in lib/api.ts and lib/offpeak.ts",
  "activity_live_shape.json": "the demo's live activity answer, trimmed (microsecond timestamps, a null trailer): the iPhone's decoder is tested against it",
  "trip_plan.json": "the trip planner's iPhone fixture, generated from the server type (trip_fixtures_test.go); the web's TripPlan/SavedTrip types are checked against it in a follow-up",
  "trip_plan_rivian_shape.json": "a long plan in the words Rivian really sends (upper-case waypoint types, DC_CHARGE_STATION stops), generated from the server type (trip_fixtures_test.go); the iPhone's stop finding is tested against it",
  "trip_plan_multiday.json": "the trip planner's iPhone fixture, generated from the server type (trip_fixtures_test.go); the web's TripPlan/SavedTrip types are checked against it in a follow-up",
  "trip_plan_multiday_partial.json": "the trip planner's iPhone fixture, generated from the server type (trip_fixtures_test.go); the web's TripPlan/SavedTrip types are checked against it in a follow-up",
  "trip_saved_list.json": "the trip planner's iPhone fixture, generated from the server type (trip_fixtures_test.go); the web's TripPlan/SavedTrip types are checked against it in a follow-up",
  "trip_saved_one.json": "the trip planner's iPhone fixture, generated from the server type (trip_fixtures_test.go); the web's TripPlan/SavedTrip types are checked against it in a follow-up",
  "trip_plan_actual_linked.json": "the trip planner's iPhone fixture, generated from the server type (trip_fixtures_test.go); the web's TripPlan/SavedTrip types are checked against it in a follow-up",
  "trip_plan_actual_none.json": "the trip planner's iPhone fixture, generated from the server type (trip_fixtures_test.go); the web's TripPlan/SavedTrip types are checked against it in a follow-up",
  "trip_plan_actual_multi.json": "the trip planner's iPhone fixture, generated from the server type (trip_fixtures_test.go); the web's TripPlan/SavedTrip types are checked against it in a follow-up",
  "err_plan_quota.json": "an error answer ({status, retry_after_header, body}) for the app's typed errors; the web reads errors through ApiError",
  "err_upstream_user_action.json": "an error answer ({status, retry_after_header, body}) for the app's typed errors; the web reads errors through ApiError",
  "err_upstream_transient.json": "an error answer ({status, retry_after_header, body}) for the app's typed errors; the web reads errors through ApiError",
  "err_upstream_rate_limited.json": "an error answer ({status, retry_after_header, body}) for the app's typed errors; the web reads errors through ApiError",
  "err_no_position.json": "an error answer ({status, retry_after_header, body}) for the app's typed errors; the web reads errors through ApiError",
  "app_config.json": "GET /api/app/config is read by the app only",
  "app_token.json": "the app's sign-in token exchange; the web signs in with a cookie",
  "charge_eta_vectors.json": "shared test vectors for the ETA arithmetic, not a response",
  "charge_split.json": "GET /api/charge-split is read by the app only",
  "charges_to_confirm.json": "the app's slice of a charge (a few fields of Charge); the full shape is checked through charge_one*.json",
  "chart_points.json": "the Charts screen's series; the web draws its charts from other endpoints",
  "error_app_refused.json": "the body the server gives an app token for a route it may not use",
  "error_not_connected.json": "409 not_connected, which the app reads as a state; the web reads errors through ApiError",
  "error_terms_required.json": "403 terms_required, which the app reads as a state; the web reads errors through ApiError",
  "me_signed_out.json": "the signed-out /api/auth/me body; the web reads it as AuthUser with authenticated: false (landingGate)",
  "now_prod_shape.json": "a deliberately trimmed capture for the app's tolerance tests; now_v1.json and now_v1_minimal.json carry the contract",
  "state.json": "the app's subset of /api/state; state_full.json carries every key",
  "state_prod_shape.json": "a deliberately trimmed capture for the app's tolerance tests; state_full.json carries every key",
};
