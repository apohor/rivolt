import type { OffpeakSchedule } from "./offpeak";
// Minimal API client for the Rivolt Go backend.
//
// Routes mounted in internal/api/api.go:
//   GET /api/health              → { ok, version, time }
//   GET /api/vehicles            → Vehicle[]
//   GET /api/drives?limit=N      → Drive[] (newest first)
//   GET /api/charges?limit=N     → Charge[] (newest first)
//   GET /api/samples?since&limit → Sample[] (oldest first)
//   GET /api/push/vapid-key      → { public_key }
//   POST /api/push/subscribe     → persists a browser subscription

import { IMPERSONATE_HEADER, impersonationHeaderID } from "./impersonation";

// How many samples a drive or charge detail page asks for. The server buckets a window that goes over the limit across its
// whole span (first, last and the peaks kept), so 2,000 draws the same charts as 20,000 and moves a tenth of the data.
export const DETAIL_SAMPLE_LIMIT = 2_000;

// One sign-in of the signed-in user: a browser ("web") or the iPhone app ("app").
export type AuthSession = {
  id: string;
  kind: "web" | "app";
  // As stored: the device type the app reports ("iPhone"), or a browser and system for a browser; may be empty.
  device_label: string;
  user_agent?: string;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  // True for the sign-in making this request, when it has one.
  current: boolean;
};

export class ApiError extends Error {
  status: number;
  body: unknown;
  constructor(status: number, body: unknown, msg?: string) {
    let message = msg;
    if (!message && body && typeof body === "object" && "error" in body) {
      const e = (body as { error?: unknown }).error;
      if (typeof e === "string" && e.length > 0) message = e;
    }
    if (!message && typeof body === "string" && body.length > 0) {
      // Cloudflare's 5xx error pages and any other edge that
      // intercepts a 5xx response substitute an HTML body. Don't
      // leak that into a UI toast — render a stable status-based
      // message instead.
      const looksHTML = /^\s*<(!doctype|html|head|body)/i.test(body);
      message = looksHTML ? statusFallbackMessage(status) : body;
    }
    super(message ?? statusFallbackMessage(status));
    this.status = status;
    this.body = body;
  }
}

function statusFallbackMessage(status: number): string {
  if (status === 502) return "Bad gateway — upstream isn't responding.";
  if (status === 503) return "Service unavailable — try again shortly.";
  if (status === 504) return "Upstream timed out — try again shortly.";
  if (status >= 500) return `Server error (HTTP ${status}).`;
  return `HTTP ${status}`;
}

async function request<T>(
  method: string,
  url: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  // Admin "View as user": while a target is set in sessionStorage,
  // every request carries it so the server (internal/api/auth_mw.go)
  // renders that user's data instead of the admin's own. The header
  // is inert for anyone who isn't a confirmed admin caller — the
  // server ignores it outright for non-admins.
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  // Never impersonate on the login page. Its whole job is to bootstrap
  // the REAL session (poll /api/auth/me, list OIDC providers); if it
  // carried the header, /api/auth/me would resolve as the impersonated
  // target and the page would think it's already signed in — which,
  // combined with a needs_reauth target, spins a /login <-> app loop.
  const onLogin = window.location.pathname.startsWith("/login");
  const impersonateID = onLogin ? null : impersonationHeaderID();
  if (impersonateID) headers[IMPERSONATE_HEADER] = impersonateID;
  const res = await fetch(url, {
    method,
    headers: Object.keys(headers).length > 0 ? headers : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  });
  const text = await res.text();
  let parsed: unknown = undefined;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = text;
    }
  }
  if (!res.ok) {
    // Global 401 handling: the session expired or was never
    // established. Bounce the whole SPA to /login so every caller
    // doesn't have to reinvent this. We *don't* redirect for
    // /api/auth/me — the login page itself polls that endpoint to
    // bootstrap, and redirecting on its 401 would create a loop.
    // /api/auth/hydra/login returns 401 for "invalid credentials" on
    // the user-supplied form — bouncing to /login would lose the
    // login_challenge in the URL and strand the third-party OIDC
    // flow. Let the page render an inline error instead.
    //
    // Crucially, a *Rivian-upstream* 401 (the user's Rivian session
    // needs re-auth) is NOT a Rivolt-session failure: it carries an
    // upstream-error `class` field and means "reconnect your Rivian,"
    // not "your login expired." Redirecting on it bounced authenticated
    // users with a stale Rivian session into an endless / ⇄ /login loop
    // (e.g. /api/vehicles 401s on needs_reauth). Skip the redirect and
    // let the page render its connect-Rivian prompt.
    const isUpstreamErr =
      parsed != null &&
      typeof parsed === "object" &&
      "class" in (parsed as Record<string, unknown>);
    // An IdP session for an identity with no account on a closed
    // server: the backend stays 401 but names the reason. Send the
    // visitor to the login page with the same error the OIDC callback
    // uses, once (the login page itself polls /api/auth/me).
    const closedSignup =
      res.status === 401 && res.headers.get("X-Rivolt-Auth-Error") === "signups_disabled";
    if (closedSignup && !new URLSearchParams(window.location.search).has("error")) {
      window.location.replace("/login?error=signups_disabled");
    }
    // Exclusive with the generic bounce below, which would otherwise
    // navigate to plain /login afterwards and drop the message.
    if (
      res.status === 401 &&
      !closedSignup &&
      !isUpstreamErr &&
      !url.endsWith("/api/auth/me") &&
      // The whole Hydra sign-in (the password form and the passkey ceremony) runs signed out, keyed by a login_challenge in the URL:
      // a 401 from it is "that sign-in didn't work", for the page to say, and a bounce would drop the challenge and the message.
      !url.startsWith("/api/auth/hydra/") &&
      // The public demo is signed out on purpose; a 401 for something
      // outside its read-only allowlist is not "your login expired".
      !window.location.pathname.startsWith("/demo")
    ) {
      const here = window.location.pathname + window.location.search;
      if (!window.location.pathname.startsWith("/login")) {
        const next = here === "/" ? "" : `?next=${encodeURIComponent(here)}`;
        window.location.assign(`/login${next}`);
      }
    }
    throw new ApiError(res.status, parsed);
  }
  return parsed as T;
}

// inDemo is true on the public sample-truck route, where there is no
// session and the Rivian-account endpoints are skipped entirely.
function inDemo(): boolean {
  return typeof window !== "undefined" && window.location.pathname.startsWith("/demo");
}

export const api = {
  get: <T>(url: string, signal?: AbortSignal) => request<T>("GET", url, undefined, signal),
  post: <T>(url: string, body?: unknown, signal?: AbortSignal) =>
    request<T>("POST", url, body, signal),
  put: <T>(url: string, body?: unknown, signal?: AbortSignal) =>
    request<T>("PUT", url, body, signal),
  patch: <T>(url: string, body?: unknown, signal?: AbortSignal) =>
    request<T>("PATCH", url, body, signal),
  del: <T>(url: string, signal?: AbortSignal) => request<T>("DELETE", url, undefined, signal),
};

// ---------- types (exported JSON field names match Go struct tags) ----------

export type Health = { ok: boolean; version: string; time: string };

export type VehicleImage = {
  vehicle_id: string;
  order_id?: string;
  url: string;
  extension?: string;
  resolution?: string;
  size?: string;
  design?: string;
  placement?: string;
};

export type Vehicle = {
  id: string;
  vin: string;
  name: string;
  model: string;
  // Which live fields to trust on this model: label "estimate", soften "best_effort", hide "unavailable".
  field_support?: Record<string, "reported" | "estimate" | "best_effort" | "unavailable">;
  model_year?: number;
  make?: string;
  trim_id?: string;
  trim_name?: string;
  pack_kwh?: number;
  image_url?: string;
  images?: VehicleImage[];
  // Release details for the installed and the pending update (omitted until fetched).
  ota?: {
    current?: { version?: string; has_notes?: boolean; locale?: string };
    available?: { version?: string; has_notes?: boolean; locale?: string };
    status?: string;
    fetched_at?: string;
  };
};

// OwnedVehicle is the DB-backed shape returned by /api/vehicles/owned.
// Narrower than Vehicle (no trim/image fields) — picker only needs
// enough to render an option label and submit the rivian_vehicle_id.
export type OwnedVehicle = {
  id: string;
  rivian_vehicle_id: string;
  vin?: string;
  display_name?: string;
  model?: string;
  model_year?: number;
  pack_kwh?: number;
};

// LiveSession mirrors internal/rivian.LiveSession — the snapshot
// pulled from Rivian's chrg/user/graphql endpoint during an active
// charging session. All zero/empty when no session is active.
export type LiveSession = {
  at: string;
  vehicle_id: string;
  active: boolean;
  vehicle_charger_state: string;
  start_time: string;
  time_elapsed_seconds: number;
  time_remaining_seconds: number;
  power_kw: number;
  kilometers_charged_per_hour: number;
  range_added_km: number;
  total_charged_energy_kwh: number;
  soc_pct: number;
  current_price: string;
  current_currency: string;
  is_free_session: boolean;
  is_rivian_charger: boolean;
  // estimated_cost is computed locally from the operator-configured
  // home $/kWh rate × total_charged_energy_kwh whenever Rivian reports
  // no price (home AC / L2 sessions are always flagged free upstream).
  estimated_cost?: number;
  estimated_currency?: string;
};

// VehicleState matches internal/rivian.State. Units are SI at the wire:
// battery in percent, distance in km, temps in C. The UI converts as
// needed for display.
import type { ChargingSchedule } from "./chargingSchedule";

export type VehicleState = {
  at: string;
  vehicle_id: string;
  battery_level_pct: number;
  distance_to_empty: number;
  odometer_km: number;
  gear: string;
  drive_mode: string;
  charger_state: string;
  charger_power_kw: number;
  charge_target_pct: number;
  charger_status: string;
  charge_port_state: string;
  remote_charging_available: string;
  latitude: number;
  longitude: number;
  speed_kph: number;
  heading_deg: number;
  altitude_m: number;
  locked: boolean;
  doors_closed: boolean;
  frunk_closed: boolean;
  liftgate_closed: boolean;
  tailgate_closed: boolean;
  tonneau_closed: boolean;
  cabin_temp_c: number;
  outside_temp_c: number;
  // High-voltage battery pack cell temperatures (°C), from the Parallax
  // battery_state topic. 0 when the car hasn't pushed a reading (asleep).
  pack_temp_avg_c?: number;
  pack_temp_max_c?: number;
  pack_temp_min_c?: number;
  cabin_preconditioning_status: string;
  power_state: string;
  alarm_sound_status: string;
  twelve_volt_battery_health: string;
  wiper_fluid_state: string;
  ota_current_version: string;
  ota_available_version: string;
  ota_status: string;
  ota_install_progress: number;
  // The truck's scheduled charging windows (omitted until read).
  charging_schedules?: ChargingSchedule[];
  tire_pressure_fl_bar: number;
  tire_pressure_fr_bar: number;
  tire_pressure_rl_bar: number;
  tire_pressure_rr_bar: number;
  tire_pressure_status_fl: string;
  tire_pressure_status_fr: string;
  tire_pressure_status_rl: string;
  tire_pressure_status_rr: string;
  // Charging-context, populated during an active session. Zero/empty
  // outside a session. Surfaced on LivePanel's charge card.
  time_to_end_mins: number;
  // "estimate" when Parallax supplied it (an R2): label it so; absent when the vehicle reported it.
  time_to_end_source?: string;
  trip_target_soc_pct: number;
  trip_target_mins_remaining: number;
  charger_derate_status: string;
  charging_disabled_all: string;
  charging_time_estimation_validity: string;
  // Driver + special-mode chips on the live header.
  active_driver_name: string;
  gear_guard_locked: string;
  pet_mode_status: string;
  pet_mode_temperature_status: string;
  cabin_hold_status: string;
  // Windows. Raw Rivian closure-enum strings (the vocabulary is
  // richer than door open/closed: "opened", "open_allowed",
  // "obstructed_while_opening_close_allowed", "closed", "").
  window_front_left_closed: string;
  window_front_right_closed: string;
  window_rear_left_closed: string;
  window_rear_right_closed: string;
};

export type Drive = {
  ID: string;
  VehicleID: string;
  StartedAt: string;
  EndedAt: string;
  StartSoCPct: number;
  EndSoCPct: number;
  StartOdometerMi: number;
  EndOdometerMi: number;
  DistanceMi: number;
  StartLat: number;
  StartLon: number;
  EndLat: number;
  EndLon: number;
  MaxSpeedMph: number;
  AvgSpeedMph: number;
  // Driving-only duration in seconds. Set by collapseRoundTrips on a
  // merged round-trip row, where EndedAt-StartedAt spans the parked
  // gap(s) between legs and would overstate time driven (and desync
  // from DistanceMi / AvgSpeedMph, which are driving-only). Absent on
  // unmerged rows, where EndedAt-StartedAt already is the driving time.
  DrivingSeconds?: number;
  // Pack-side energy consumed, derived from SoC delta × usable pack
  // capacity at the time the drive was persisted. Zero on legacy rows
  // and on imports where --pack-kwh wasn't set.
  EnergyUsedKWh: number;
  Source: string;
  // Whether a trailer was connected (the connector, not the load: a bike rack with lights plugged in can read as one).
  // Trailer is what the truck reported (null when it never did); TrailerOverride is the user's own correction (null when
  // they made none). The effective value is TrailerOverride ?? Trailer: see driveHasTrailer in lib/drives.ts.
  Trailer?: boolean | null;
  TrailerOverride?: boolean | null;
  // Encoded GPS trace for the drive (Google polyline algorithm,
  // precision 5). Set by the live recorder going forward; legacy
  // ElectraFi imports and any drive that closed before migration
  // 0018 leave it empty, in which case the overview map falls back
  // to a straight start → end line.
  RoutePolyline?: string;
  // Locally-computed cost. The backend bills each drive at the
  // rate of the most recent charge that ended before it started
  // (RAN, home, or manual override), falling back to a blended
  // rate for drives that predate the first known charge. Present
  // when both EnergyUsedKWh and a usable rate exist.
  estimated_cost?: number;
  estimated_currency?: string;
  estimated_price_per_kwh?: number;
  // Set by collapseRoundTrips on a row it merged: how many recorded drives
  // it holds. Counts (Now, Money, the API) are of recorded drives.
  round_trip_legs?: number;
};

// PackHealthSample is one derived effective-pack-capacity reading
// from a qualifying charge session. Backend filters out small-SoC
// or out-of-range fits before sending.
export type PackHealthSample = {
  vehicle_id: string;
  charge_id: string;
  at: string;
  pack_kwh_effective: number;
  soc_delta_pct: number;
  energy_delivered_kwh: number;
  derate_active: boolean;
};

// PackHealthResponse pairs the time series with a headline median
// of the last N clean samples plus the vehicle's nameplate kWh for
// the "% of nameplate" line. effective_kwh + pct_of_nameplate are
// zero when no qualifying samples exist yet.
// DayActivity is one calendar day's power-state split (hours), from
// GET /api/sleep-activity. idle_awake_h is the car awake while parked and
// not charging — the vampire-drain signal. Populated forward from the
// deploy that began persisting power_state.
// GET /api/vehicles/{id}/sleep-health: numbers and a state; the words are the client's (lib/truckHealth sleepHealthLines).
export type SleepHealth = {
  version: number;
  state: "normal" | "more" | "not_enough_data";
  this_week: { days: number; sleeps: number; failed_sleeps: number };
  baseline_days: number;
  usual_failed_sleeps?: number;
  more_by?: number;
};

export type DayActivity = {
  day: string; // RFC3339 (UTC day start)
  asleep_h: number;
  idle_awake_h: number;
  // Times the car fell asleep that day, and how many of those sleeps
  // ended within a minute (something woke it right back up).
  sleeps: number;
  failed_sleeps: number;
};

export type PackHealthResponse = {
  samples: PackHealthSample[];
  headline: {
    effective_kwh: number;
    nameplate_kwh: number;
    pct_of_nameplate: number;
    // Documented (nameplate spec, InferPackKWh) vs current (vehicle-
    // reported usable capacity, from Parallax charge_state). Ratio is the
    // vehicle's own degradation signal; reported == documented (100%) when
    // no live capacity has been observed yet.
    documented_kwh: number;
    reported_kwh: number;
    reported_pct_of_documented: number;
    sample_count: number;
    window: number;
  };
};

export type Charge = {
  ID: string;
  VehicleID: string;
  StartedAt: string;
  EndedAt: string;
  StartSoCPct: number;
  EndSoCPct: number;
  EnergyAddedKWh: number;
  // The energy was lost and worked out from the battery's change in charge level (lib/energyEstimated). /api/charges sends EnergyEstimated;
  // the activity feed sends energy_estimated.
  EnergyEstimated?: boolean;
  energy_estimated?: boolean;
  MilesAdded: number;
  MaxPowerKW: number;
  AvgPowerKW: number;
  FinalState: string;
  Lat: number;
  Lon: number;
  Source: string;
  // Cost is the persisted total session cost in Currency, snapshotted
  // at close time. Zero for legacy rows (imports, pre-v0.3.29 live).
  Cost: number;
  Currency: string;
  PricePerKWh: number;
  // Locally-computed cost using the home $/kWh rate. Present when
  // Cost is zero AND both a rate is configured and EnergyAddedKWh > 0.
  estimated_cost?: number;
  estimated_currency?: string;
  // Energy the BMS spent on pack heating / cooling during the
  // session, decoded from Rivian's Parallax ChargingSessionLiveData
  // protobuf (field 3). Null on legacy rows recorded before the
  // column existed and on sessions that didn't go through the
  // Parallax stream (REST poller, ElectraFi import).
  ThermalKWh?: number | null;
  // The charge limit the truck was set to for the session (1 to 100), stored on the charge (migration 0080);
  // null when unknown. Absent on servers before #897.
  ChargeLimitPct?: number | null;
  // #906: the user's own home/away for this charge ("set by you"; empty: automatic) and "recorded" when the map pins
  // where the truck was instead of the nearest public charger. Absent on servers before #906.
  PlaceOverride?: "" | "home" | "away";
  LocationOverride?: "" | "recorded";
  // What Auto gives this charge from the current home; absent when the home or position is unknown.
  place_auto?: "home" | "away";
  // Wall-clock seconds the session actually spent charging (charger
  // power above the idle floor), accumulated by the live recorder from
  // the same gated dt that feeds the energy integral. Distinct from
  // EndedAt-StartedAt, which spans the whole plugged-in period
  // (including post-target idle / battery conditioning). Absent or 0 on
  // legacy rows, imports, and non-Parallax live sessions; the UI falls
  // back to the wall span then.
  ActiveSeconds?: number;
  // Where Cost came from (ledger contract §10/§12): "user" with Cost 0
  // is an explicit Free. Absent on older builds.
  CostSource?: string;
  // The ledger's classification of this row (contract §16): the same
  // rules the Money tab uses. Absent on builds that predate it, where
  // callers fall back to the stored fields.
  cost_source_effective?: "rivian" | "home_rate" | "network_estimate" | "observed_estimate" | "user" | "home_rate_misapplied" | "unknown";
  // observed_estimate: priced from the tracker's typical price for estimate_month (estimate_url is the report; link it only through
  // safeTrackerUrl). needs_rate: a fast-charging session waiting on the user's own rate for the network named in `network`/rate_slug.
  // The recorder's stored source; cost_source_effective (above) is the ledger's classification and is read first.
  cost_source?: string;
  estimate_month?: string | null;
  estimate_url?: string | null;
  needs_rate?: boolean;
  rate_slug?: string | null;
  network?: string | null;
  cost_source_derived?: boolean;
  // DCFC auto-network pricing; null until dcfc_auto_network is on.
  network_state?: import("./autoNetwork").NetworkState | null;
  network_confidence?: number | null;
  network_evidence?: import("./autoNetwork").NetworkEvidence | null;
  kind?: "ac" | "dcfc";
  place?: "home" | "away" | "unknown";
  km_from_home?: number | null;
  counts_in_spend?: boolean;
  ledger_flags?: string[];
  // Settings couldn't be read, so the classification fields are omitted
  // (contract §16.1): don't derive labels client-side.
  classification_unavailable?: boolean;
};

export type ChargingSettings = {
  home_price_per_kwh: number;
  home_currency: string;
  // Inputs for the gas-equivalent chip on the trip planner. Zero on
  // either input hides the chip.
  gas_price_per_gallon: number;
  comparison_mpg: number;
};

// ChargingNetwork is one entry in the user's price book for fast /
// public charging — a friendly name and a default rate the UI can
// one-click apply when manually pricing a session.
export type ChargingNetwork = {
  name: string;
  price_per_kwh: number;
  currency: string;
  // Member rate at this network. 0 means no membership tier exists
  // (ChargePoint, Francis Energy, etc.). Must be < price_per_kwh
  // when present.
  member_price_per_kwh?: number;
  // member_active = "I pay for this membership", drives the
  // user-totals column on the trip planner cost strip.
  member_active?: boolean;
  // Display hint for the membership row, e.g. "Pass+ $7/mo".
  member_plan?: string;
  // Slug ties this row to a built-in network for match-pattern
  // lookup. Empty = user-added custom network (matches by name).
  slug?: string;
  // preferred = "rank this network high in Rivian's planner".
  // Forwarded as networkPreferences[].preference=1 on plan requests.
  preferred?: boolean;
  // Where a custom row (no slug) is, when the user gave one (#906): both or neither. Private: only labels the user's
  // own charges and preselects the pricing prefill; never used to price anything by itself.
  lat?: number;
  lon?: number;
};

// LiveDrive is the in-flight drive snapshot returned by
// /api/live-drive/:vehicleID while the car is in gear. Mirrors
// internal/rivian.LiveDrive — fields are flat and already in mph /
// miles / kWh so the UI renders without unit conversion.
// NowSnapshot is GET /api/vehicles/{id}/now (now-contract v1): the
// server's view of state, freshness inputs and a few derived fields the
// raw VehicleState doesn't carry. Null means unknown, never 0.
export type NowSnapshot = {
  version: number;
  at: string | null;
  location_fix_at: string | null;
  // Not a freshness verdict: the client applies per-state cadence to `at`.
  connection: "live" | "replay" | "reauth" | "paused";
  state: "driving" | "charging" | "plugged" | "parked" | "asleep" | "unknown";
  // True when the newest frame's power_state is sleep, independent of
  // `state` (a plugged truck can be asleep). Absent on older servers.
  asleep?: boolean;
  state_since: string | null;
  soc_at_state_start: number | null;
  tire_pressure_min_bar: number | null;
  failed_sleeps_6h: number;
  charge: { started_at: string; soc_start: number; peak_kw: number } | null;
  // Present only when the user turned outside temperature on: an Open-Meteo reading for the truck's area.
  ambient?: { temp_c: number; source: string; as_of: string };
};
export type DemoInfo = { enabled: boolean; vehicle_id: string; home_label: string };

// DrainSnapshot is GET /api/vehicles/{id}/drain (drain-contract v1).
// Nulls mean unknown, never 0. Reports what happened, not why.
export type DrainWindow = {
  drain_pct_per_day: number | null;
  drain_kwh_per_day: number | null;
  idle_awake_hours_per_day: number | null;
  sleeps: number;
  quick_rewakes: number;
  median_sleep_seconds: number | null;
  parked_hours: number;
  power_state_observed_hours: number;
  days_in_window: number;
  days_with_data: number;
  // Only "insufficient" is reported (v1.1); older shapes may add more.
  days_excluded: { insufficient: number; charging?: number; driving?: number };
  sufficient: boolean;
  // When the parked loss happened: asleep or awake. Absent on a server that predates it.
  split?: DrainSplit;
};

// Where the parked loss happened. When `sufficient` is false, `reason` says why and the figures are null. SoC is whole
// percents, so every figure is an estimate. It says when the loss happened, never why.
export type DrainSplit = {
  sufficient: boolean;
  reason?: "not_enough_data" | "power_state_gaps" | "power_state_unreliable";
  asleep_hours: number | null;
  awake_hours: number | null;
  asleep_points_lost: number | null;
  awake_points_lost: number | null;
  awake_share_of_loss: number | null;
  awake_share_of_time: number | null;
  min_hours: number;
  min_points_lost: number;
};

export type DrainDay = {
  day: string;
  drain_pct: number | null;
  idle_awake_hours: number | null;
  parked_hours: number;
  sleeps: number;
  quick_rewakes: number;
  excluded: "charging" | "driving" | "insufficient" | null;
  // Informational (v1.1).
  charging_hours?: number;
  driving_hours?: number;
};

export type DrainSnapshot = {
  version: number;
  generated_at: string;
  tz: string;
  pack_kwh: number | null;
  windows: { "7d": DrainWindow; "30d": DrainWindow };
  daily: DrainDay[];
};

// Monthly statement email (statement-email-spec.md).
export type StatementEmailSettings = {
  enabled: boolean;
  // False when the operator flag is off or the account has no email.
  available: boolean;
  last_sent_period?: string | null;
  // IANA zone the monthly send uses (set when the user opts in).
  timezone?: string | null;
};

export type StatementEmailPreview = {
  subject: string;
  text: string;
  html: string;
  will_send: boolean;
  reason?: "not_opted_in" | "no_email" | "silent_month" | string;
};

export type LiveDrive = {
  vehicle_id: string;
  number: number;
  started_at: string;
  ended_at: string;
  elapsed_sec: number;
  start_soc_pct: number;
  end_soc_pct: number;
  soc_used_pct: number;
  start_odometer_mi: number;
  end_odometer_mi: number;
  distance_mi: number;
  max_speed_mph: number;
  avg_speed_mph: number;
  energy_used_kwh: number;
  mi_per_kwh: number;
  pack_kwh: number;
};

export type Sample = {
  VehicleID: string;
  At: string;
  BatteryLevelPct: number;
  RangeMi: number;
  OdometerMi: number;
  Lat: number;
  Lon: number;
  // ISO timestamp the GNSS module reported on the (Lat, Lon) fix.
  // Differs from At when the modem replays a stale fix — the
  // recorder splits these so the UI can paint a "GPS stale" badge
  // instead of treating frozen coords as live. Absent on legacy
  // rows and on imports that never carried a fix timestamp.
  LocationFixAt?: string;
  SpeedMph: number;
  ShiftState: string;
  ChargingState: string;
  drive_mode?: string;
  ChargerPowerKW: number;
  ChargeLimitPct: number;
  InsideTempC: number;
  OutsideTempC: number;
  DriveNumber: number;
  ChargeNumber: number;
  Source: string;
  // Elevation above sea level in meters at (Lat, Lon), looked up
  // by the live recorder against the Mapzen Terrarium DEM. Absent
  // on legacy rows (pre-migration 0019), ElectraFi imports, samples
  // without a GPS fix, and cold-cache misses where the tile fetch
  // landed too late. The drive-detail Elevation chart hides itself
  // when every sample in the window is missing this field.
  altitude_m?: number;
  // High-voltage battery pack cell temperatures (°C), merged in from the
  // Parallax battery_state topic. Absent on legacy rows and on samples
  // recorded while the car was asleep (the topic only streams when
  // awake). avg is the pack average; the min↔max spread is a
  // thermal-imbalance signal (wide during DCFC / cold soak).
  pack_temp_avg_c?: number;
  pack_temp_max_c?: number;
  pack_temp_min_c?: number;
};

// DriveWeather is the optional weather snapshot the backend captured
// for a drive's start (only populated when the operator opted in).
// Numbers are imperial (F, mph, in) to match the rest of the SPA;
// every field except `conditions` is nullable so the UI can render
// only what the upstream returned.
export type DriveWeather = {
  temp_f?: number | null;
  feels_like_f?: number | null;
  wind_mph?: number | null;
  wind_from_deg?: number | null;
  // Signed: positive = headwind, negative = tailwind.
  headwind_mph?: number | null;
  precip_in?: number | null;
  humidity_pct?: number | null;
  conditions?: string;
};

// DriveWeatherSamplePoint is one entry in the per-drive weather time
// series. Cadence is 15 minutes for drives recent enough to hit
// Open-Meteo's forecast endpoint, 60 minutes for older drives that
// fall back to the archive endpoint. Same imperial units as
// DriveWeather; nullable fields use the same convention (omitted =
// upstream didn't supply that metric for this sample).
export type DriveWeatherSamplePoint = {
  at: string;
  cadence_minutes: number;
  temp_f?: number | null;
  feels_like_f?: number | null;
  wind_mph?: number | null;
  wind_from_deg?: number | null;
  headwind_mph?: number | null;
  precip_in?: number | null;
  humidity_pct?: number | null;
  conditions?: string;
};

export type DriveWeatherSeries = {
  points: DriveWeatherSamplePoint[];
};

export type EfficiencyFactor = {
  name: string;
  impact_estimate_pct: number; // Negative = hurt efficiency, positive = helped
  confidence_0_to_100: number;
  // Signed kWh impact on this specific drive (negative = hurt). Set
  // alongside impact_estimate_pct so the SPA can show "−8% (−0.7 kWh)"
  // and so factors can be summed and sanity-checked against the
  // drive's total energy. Optional for backwards compat with rows
  // generated before the field landed.
  magnitude_kwh?: number;
  // Short citation (≤ 80 chars) of the data point that justified
  // the factor. Rendered as a muted subtitle under the factor name
  // so users can verify the AI's reasoning. Optional for the same
  // backwards-compat reason.
  evidence?: string;
};

export type DriveEfficiency = {
  analysis: string;
  factors?: EfficiencyFactor[];
  recommendation?: string;
  forecast?: string;
  summary?: string;
  model: string;
  generated_at: string;
  input_tokens?: number;
  output_tokens?: number;
};

// VehicleProfile is the per-vehicle context the efficiency analyzer
// factors into its breakdown. Stored in vehicles.metadata.profile
// JSONB; all fields optional so an empty object means "unset" and
// the analyzer silently omits them from the prompt.
export type VehicleTireType =
  | ""
  | "all_season"
  | "all_terrain"
  | "winter"
  | "summer";
export type VehicleProfile = {
  tire_type?: VehicleTireType;
  wheel_inches?: number;
  accessories?: string[];
  default_extra_load_lb?: number;
  frequently_tows?: boolean;
  // Door-jamb cold-fill placard pressure (psi). Optional; when set,
  // the efficiency analyzer cites the delta between current and
  // placard so it can attribute "Low tire pressure" against ground
  // truth instead of guessing the placard from R1S/R1T priors.
  tire_placard_psi?: number;
  // null/undefined = auto (heuristic on model_year), true = native NACS,
  // false = CCS / needs Tesla adapter.
  native_nacs?: boolean | null;
};

// vehicleNeedsTeslaAdapter resolves the per-vehicle NACS state. Auto
// (undefined/null) falls back to the model-year heuristic: Rivian R1
// Gen 2 onwards (MY2026+) ships with native NACS ports.
export function vehicleNeedsTeslaAdapter(
  profile: VehicleProfile | undefined | null,
  modelYear: number | undefined,
): boolean {
  if (profile && typeof profile.native_nacs === "boolean") {
    return !profile.native_nacs;
  }
  if (!modelYear) return true;
  return modelYear < 2026;
}

export type RivianStatus = {
  enabled: boolean;
  authenticated: boolean;
  mfa_pending: boolean;
  email?: string;
  needs_reauth?: boolean;
  needs_reauth_reason?: string;
  // Present only while signed in and not needs_reauth: when Rivian ends this sign-in (180 days after it; nothing extends it) and the
  // whole days left (rounded up, 0 when past).
  session_expires_at?: string;
  session_days_left?: number;
  // The operator paused Rivolt's connection to Rivian (kill switch).
  // paused_reason is a fixed code only ("operator_pause").
  paused?: boolean;
  paused_reason?: string;
};

// AIProvider enumerates the LLM backends Rivolt supports. Image/speech
// aren't offered — Rivolt only uses text analysis (digests, anomaly
// explanations, trip planning prose).
export type AIProvider = "openai" | "anthropic" | "gemini";

// AISettings is the redacted public view returned by GET /api/settings/ai.
// API keys are surfaced as a boolean `has_key` only — the secret never
// leaves the backend.
export type AISettings = {
  // "" means "auto": first provider with a key wins. Otherwise pinned.
  provider: "" | AIProvider;
  effective_provider?: AIProvider;
  // e.g. "openai:gpt-4o-mini" — set only when ready=true.
  effective_model?: string;
  providers: Record<AIProvider, { model: string; has_key: boolean }>;
  ready: boolean;
};

// RecapSettings is the redacted view returned by GET
// /api/admin/settings/recap. Lives on its own surface (not under
// AI) because the toggle here is a data-egress switch — disabling
// AI shouldn't imply disabling external weather lookups, and vice
// versa. Default false: the disclosure must be opted into.
export type RecapSettings = {
  weather_enabled: boolean;
};

// RecapSettingsUpdate is the partial patch for PUT
// /api/admin/settings/recap. Omitted fields are left alone.
export type RecapSettingsUpdate = {
  weather_enabled?: boolean;
};

// GPSSettings is the install-wide GPS accuracy heuristic
// configuration returned from GET /api/admin/settings/gps.
export type GPSSettings = {
  missing_pct: number;
  stale_sec: number;
  jump_count: number;
};

// GPSSettingsUpdate is the partial patch for PUT
// /api/admin/settings/gps. Omitted fields are left alone.
export type GPSSettingsUpdate = {
  missing_pct?: number;
  stale_sec?: number;
  jump_count?: number;
};

// PushPreferences mirrors internal/push.Preferences — per-subscription
// filters chosen by the user when subscribing or updating prefs.
export type PushPreferences = {
  on_charging_done: boolean;
  on_plug_in_reminder: boolean;
  on_anomaly: boolean;
  on_software_update: boolean;
  on_milestones: boolean;
  // The left-open alert: an opt-in. Optional so the subscribe call (which never sends it) and older stubs keep their shape;
  // the server returns it when it reads a device's switches back.
  on_left_open?: boolean;
};

// The account-wide engagement settings, GET and PUT /api/settings/engagement. A PUT may carry any
// subset; the answer is the whole object after the change.
export type EngagementSettings = {
  timezone: string;
  // False until a zone has been saved: the plug-in reminder never sends without one.
  timezone_stored: boolean;
  weekly_recap: boolean;
  // One notification after driving a saved trip (off until chosen). Absent from an older server, where the switch is hidden.
  trip_recap?: boolean;
  plug_threshold_pct: number;
  plug_remind_at: string; // HH:MM, local, no later than 22:00
};

// A milestone the truck reached (GET /api/milestones): once each per vehicle, with the server's own words.
export type MilestoneItem = {
  vehicle_id: string;
  key: string;
  kind: "miles_1000" | "home_mwh_1" | "charges_100" | "lease_half" | "best_eff" | string;
  achieved_at: string;
  title: string;
  body: string;
};

// PushStatus is the aggregated state returned by GET /api/push/status.
export type PushStatus = {
  enabled: boolean;
  subscription_count: number;
};

// PushSubscribeBody is the JSON body for POST /api/push/subscribe.
// Mirrors the shape produced by browser PushSubscription.toJSON() with
// the optional preferences + user_agent the server records.
export type PushSubscribeBody = {
  endpoint: string;
  keys: { p256dh: string; auth: string };
  preferences?: PushPreferences;
  user_agent?: string;
};

// DriveWeatherBackfillResult is one tick of progress returned by
// POST /api/drives/weather/backfill. Each call processes a bounded
// batch and reports `remaining` so the SPA can poll until zero.
// `disabled: true` means the operator hasn't opted into the
// recap weather enrichment yet — the SPA short-circuits without
// re-issuing the call.
export type DriveWeatherBackfillResult = {
  disabled: boolean;
  processed: number;
  succeeded: number;
  failed: number;
  remaining: number;
};

// Partial patch for PUT /api/settings/ai. Omitted fields are left alone;
// an explicit empty string clears the value.
export type AISettingsUpdate = {
  provider?: "" | AIProvider;
  openai_model?: string;
  openai_api_key?: string;
  anthropic_model?: string;
  anthropic_api_key?: string;
  gemini_model?: string;
  gemini_api_key?: string;
};

// AIPingResult is what POST /api/ai/ping returns. The backend sends
// a trivial smoke-test prompt to the active provider and echoes the
// reply plus latency / token usage so the UI can confirm the
// key+model triple actually works.
export type AIPingResult = {
  reply: string;
  model: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
};

// ChargeCluster is one group returned by /api/charges/clusters. Member
// IDs reference rows in the /api/charges response so the UI can paint
// a Home/Public/Fast badge next to each session.
export type ChargeClusterLabel = "Home" | "Public" | "Fast" | "";

export type ChargeCluster = {
  label: ChargeClusterLabel;
  lat: number;
  lon: number;
  sessions: number;
  energy_kwh: number;
  radius_m: number;
  member_ids: string[];
};

// GET /api/vehicles/{id}/cold-range: the user's own efficiency and range near a temperature (all temperatures in °C).
// With a figure `reason` is absent; the range fields are null when the pack is unknown (mi/kWh only).
export type ColdRange = {
  units: "imperial" | "metric";
  distance_unit: string;
  efficiency_unit: string;
  temp_c: number;
  reason?: "not_enough_data" | "not_tracked";
  efficiency_low: number | null;
  efficiency_high: number | null;
  range_low: number | null;
  range_high: number | null;
  based_on: { drives: number; miles: number; temp_lo_c: number; temp_hi_c: number } | null;
  available: number[];
  min_drives: number;
  min_miles: number;
};

// GET /api/vehicles/{id}/trends: one row per local month (a month under min_month_distance is a gap in the chart),
// and the usable-capacity estimate from charges. capacity is null on a truck that cannot be tracked (R2).
export type TruckTrends = {
  units: "imperial" | "metric";
  distance_unit: string;
  efficiency_unit: string;
  timezone: string;
  months: number;
  min_month_distance: number;
  min_months: number;
  qualifying_months: number;
  // Distance, energy, efficiency and drives are the drives without a trailer; trailer, when the month has such drives, is their
  // figure on its own (they are left out of the headline so that a trailer does not drag the truck's own number).
  efficiency: {
    month: string;
    distance: number;
    energy_kwh: number;
    efficiency: number | null;
    drives: number;
    trailer?: { drives: number; distance: number; energy_kwh: number; efficiency: number | null };
  }[];
  // The drives over the window left out of those points because a trailer was connected.
  excluded_trailer_drives?: number;
  capacity: {
    // energy_kwh / soc_delta_pct: what the charge added (for weighting the dots); absent on servers before they're sent.
    samples: { at: string; kwh_effective: number; energy_kwh?: number | null; soc_delta_pct?: number | null }[];
    median_kwh: number | null;
    // The documented figure, only when the trim identified the pack; null means draw no spec line.
    spec_kwh?: number | null;
    reported_kwh?: number | null;
    qualifying_count: number;
    span_days: number;
    min_samples: number;
    min_span_days: number;
    min_soc_delta_pct?: number;
  } | null;
};

// Efficiency insights (architect's /api/insights/efficiency): efficiency by outside temperature,
// speed and drive mode over a period, with labels and values already in the requested units.
// not_enough_data marks a bucket under min_bucket_distance; efficiency is null only when it has
// no drives at all.
export type EfficiencyBucket = {
  key: string;
  label: string;
  distance: number;
  energy_kwh: number;
  efficiency: number | null;
  drives: number;
  not_enough_data: boolean;
};
export type EfficiencyInsights = {
  units: "imperial" | "metric";
  distance_unit: string;
  efficiency_unit: string;
  min_bucket_distance: number;
  total: EfficiencyBucket;
  // The drives with a trailer connected, which the total and every breakdown leave out; drives is 0 when there are none.
  with_trailer?: EfficiencyBucket;
  by_temperature: EfficiencyBucket[];
  by_speed: EfficiencyBucket[];
  by_drive_mode: EfficiencyBucket[];
  excluded?: { no_energy?: number; duplicates?: number; no_temperature?: number; no_drive_mode?: number; trailer?: number };
};

// Lease tracker: the terms the owner enters and the figures the server computes from them and
// the odometer. The status is absent (404) when no lease is set.
export type LeaseTerms = {
  start_date: string;
  end_date: string;
  start_odometer_mi: number;
  allowed_miles_total: number;
  overage_per_mile: number;
  currency: string;
};
export type LeaseStatus = {
  used_mi: number | null;
  allowed_to_date_mi: number;
  ahead_behind_mi: number | null;
  days_total: number;
  days_elapsed: number;
  days_left: number;
  projected_total_mi: number | null;
  projected_return_odometer_mi: number | null;
  projected_overage_mi: number | null;
  projected_overage_cost: number | null;
  miles_per_day_budget: number | null;
  pace_mi_per_day: number | null;
  // "last_90_days", or "lease_to_date" when the lease is young or the history thin.
  pace_basis?: string;
  // Latest odometer reading; when it is over 7 days old or missing, used_mi, ahead_behind_mi and every
  // projected_* field are null.
  odometer_mi?: number | null;
  odometer_at?: string | null;
};

export const backend = {
  health: () => api.get<Health>("/api/health"),
  // whoami returns the logged-in user, or null when auth is
  // disabled / no session. We squash the 401 here so callers can
  // just await { user_id, username } | null without a try/catch.
  whoami: async (): Promise<AuthUser | null> => {
    // The public demo is signed out by design: don't ask (a 401 each
    // load is console noise and nothing there needs the answer).
    if (inDemo()) return null;
    try {
      const me = await api.get<AuthUser & { authenticated?: boolean }>("/api/auth/me");
      // A signed-out visitor gets 200 {"authenticated": false}
      // (no 401 console noise); callers still see "no user" as null.
      if (me.authenticated === false || !("user_id" in me)) return null;
      return me;
    } catch (e) {
      if (e instanceof ApiError && (e.status === 401 || e.status === 404)) {
        return null;
      }
      throw e;
    }
  },
  logout: () => api.post<void>("/api/auth/logout"),
  // The signed-in user's own browsers and iPhone app (GET /api/auth/sessions), one sign-out, and "sign out everywhere else".
  // Never the token or the address. 404 on an install without sessions, 403 while an admin views as someone.
  // The whole list by default. recent=N cuts it to every app session, this one and the N newest other browsers (hidden_web counts the rest);
  // ask for it only where the screen says "N older browsers not shown".
  authSessions: (recent?: number) =>
    api.get<{ sessions: AuthSession[]; total?: number; hidden_web?: number }>(`/api/auth/sessions${recent === undefined ? "" : `?recent=${recent}`}`),
  revokeAuthSession: (id: string) => api.del<void>(`/api/auth/sessions/${encodeURIComponent(id)}`),
  revokeOtherAuthSessions: () => api.post<{ revoked: number }>("/api/auth/sessions/revoke-others", {}),
  // Passkeys (WebAuthn) for email accounts. Management is for a browser session only (404 where passkeys are off, 403 for the app
  // and for an admin viewing as someone); adding asks for the password again. The ceremony options and credentials are the
  // browser's own JSON forms.
  // Change the password (passkeys stay), or reset everything: a new password and every passkey removed. Both ask for the current
  // password, answer for a browser session of an email account only, and sign the other devices out.
  changePassword: (body: { current: string; new: string }) =>
    api.post<{ ok: boolean; sessions_revoked: number; passkeys: Passkey[] }>("/api/auth/password", body),
  resetAccountAccess: (body: { current: string; new: string }) =>
    api.post<{ ok: boolean; passkeys_removed: number; sessions_revoked: number }>("/api/auth/secure", body),
  passkeys: () => api.get<{ passkeys: Passkey[]; max: number }>("/api/auth/passkeys"),
  passkeyRegisterBegin: (password: string) =>
    api.post<{ ceremony_id: string; options: { publicKey: unknown } }>("/api/auth/passkeys/register/begin", { password }),
  passkeyRegisterFinish: (body: { ceremony_id: string; label: string; credential: unknown }) =>
    api.post<{ passkey: Passkey }>("/api/auth/passkeys/register/finish", body),
  passkeyRename: (id: string, label: string) => api.patch<{ ok: boolean }>(`/api/auth/passkeys/${encodeURIComponent(id)}`, { label }),
  passkeyDelete: (id: string) => api.del<void>(`/api/auth/passkeys/${encodeURIComponent(id)}`),
  hydraPasskeyBegin: (challenge: string) =>
    api.post<{ ceremony_id: string; options: { publicKey: unknown } }>("/api/auth/hydra/passkey/begin", { challenge }),
  hydraPasskeyFinish: (body: { challenge: string; ceremony_id: string; credential: unknown }) =>
    api.post<{ redirect_to: string }>("/api/auth/hydra/passkey/finish", body),
  signup: (body: {
    signup_token: string;
    display_name?: string;
    password: string;
  }) => api.post<{ ok: boolean; signed_in?: boolean }>("/api/signup", body),
  completeOnboarding: () =>
    api.post<{ ok: boolean }>("/api/onboarding/complete"),
  // oidcProviders returns the list of OIDC sign-in options the
  // server has wired up. An empty array (or a 404 when an old
  // server is in front of a new SPA) means the server isn't
  // configured for any IdP \u2014 LoginPage shows a clear message
  // since OIDC is the only sign-in method.
  // hydraLoginGet fetches the metadata for an in-progress Hydra
  // login challenge. The browser was redirected here from Hydra
  // with ?login_challenge=…; we hand that back to the backend
  // which calls Hydra's admin /oauth2/auth/requests/login. The
  // response tells the SPA which OAuth2 client is asking, what
  // scopes it wants, and whether a login_hint was provided.
  hydraLoginGet: (challenge: string) =>
    api.get<{
      // skip=true means Hydra remembered a prior login; we accepted
      // server-side and the SPA must navigate to redirect_to without
      // rendering a form.
      skip?: boolean;
      redirect_to?: string;
      challenge?: string;
      client_id?: string;
      client_name?: string;
      requested_scope?: string[];
      login_hint?: string;
      // Whether this install lets the person sign in with a passkey (absent: no).
      passkeys?: boolean;
    }>(`/api/auth/hydra/login?login_challenge=${encodeURIComponent(challenge)}`),
  // hydraLoginPost authenticates the user against Kratos via
  // Rivolt's backend and asks Hydra to accept the login. The
  // backend returns a redirect_to URL which the SPA must navigate
  // to via a full-page assign (a fetch redirect would never reach
  // Hydra's cookie domain).
  hydraLoginPost: (body: {
    challenge: string;
    email: string;
    password: string;
  }) => api.post<{ redirect_to: string }>("/api/auth/hydra/login", body),
  oidcProviders: async (): Promise<OIDCProvider[]> => {
    try {
      return await api.get<OIDCProvider[]>("/api/auth/oidc/");
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 401)) {
        return [];
      }
      throw e;
    }
  },
  vehicles: () => api.get<Vehicle[]>("/api/vehicles"),
  // listOwnedVehicles is the DB-backed listing used by the import
  // picker. Always returns the calling user's vehicles, even when the
  // Rivian gateway is unreachable (unlike /api/vehicles which proxies
  // upstream). Excludes legacy electrafi-* synthetic rows.
  listOwnedVehicles: async (): Promise<{ vehicles: OwnedVehicle[] }> => {
    // /api/vehicles/owned needs a session; the demo lists its sample truck
    // through the public /api/vehicles instead.
    if (inDemo()) {
      const vs = await api.get<Vehicle[]>("/api/vehicles");
      return {
        vehicles: (vs ?? []).map((v) => ({
          id: v.id,
          rivian_vehicle_id: v.id,
          vin: v.vin,
          display_name: v.name,
          model: v.model,
          model_year: v.model_year,
          pack_kwh: v.pack_kwh,
        })),
      };
    }
    return api.get<{ vehicles: OwnedVehicle[] }>("/api/vehicles/owned");
  },
  vehicleState: (vehicleID: string) =>
    api.get<VehicleState>(`/api/state/${encodeURIComponent(vehicleID)}`),
  // vehicleNow returns null when the endpoint is absent (404: an older
  // server), so callers fall back to /api/state alone.
  vehicleNow: async (vehicleID: string): Promise<NowSnapshot | null> => {
    try {
      return await api.get<NowSnapshot>(`/api/vehicles/${encodeURIComponent(vehicleID)}/now`);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  // demoInfo: GET /api/demo (public). Null when the demo is off (404).
  demoInfo: async (): Promise<DemoInfo | null> => {
    try {
      const r = await api.get<DemoInfo>("/api/demo");
      return r?.enabled ? r : null;
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 401)) return null;
      throw e;
    }
  },
  // vehicleDrain returns null when the endpoint is absent (404: an older
  // server); the Truck tab then hides the drain card.
  vehicleDrain: async (vehicleID: string): Promise<DrainSnapshot | null> => {
    try {
      return await api.get<DrainSnapshot>(`/api/vehicles/${encodeURIComponent(vehicleID)}/drain`);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  // sleepHealth: this week's short sleeps against the truck's own last 30 days; null when the endpoint is absent (an older server).
  vehicleSleepHealth: async (vehicleID: string): Promise<SleepHealth | null> => {
    try {
      const h = await api.get<SleepHealth>(`/api/vehicles/${encodeURIComponent(vehicleID)}/sleep-health`);
      // Anything that isn't the contract's shape (an older server answering another route) is no answer.
      return h && ["normal", "more", "not_enough_data"].includes(h.state) && h.this_week ? h : null;
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  // efficiency returns null when the endpoint is absent (404/501), so the view says so.
  efficiency: async (rivianVehicleID: string, units: "imperial" | "metric", since?: string, until?: string): Promise<EfficiencyInsights | null> => {
    const q = new URLSearchParams({ vehicle: rivianVehicleID, units });
    if (since) q.set("since", since);
    if (until) q.set("until", until);
    try {
      return await api.get<EfficiencyInsights>(`/api/insights/efficiency?${q}`);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  // Lease: null on 404 (no lease set, or an older server).
  leaseGet: async (vehicleID: string): Promise<LeaseTerms | null> => {
    try {
      return await api.get<LeaseTerms>(`/api/vehicles/${encodeURIComponent(vehicleID)}/lease`);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  leaseStatus: async (vehicleID: string): Promise<LeaseStatus | null> => {
    try {
      return await api.get<LeaseStatus>(`/api/vehicles/${encodeURIComponent(vehicleID)}/lease/status`);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  leasePut: (vehicleID: string, t: LeaseTerms) => api.put<LeaseTerms>(`/api/vehicles/${encodeURIComponent(vehicleID)}/lease`, t),
  leaseDelete: (vehicleID: string) => api.del<unknown>(`/api/vehicles/${encodeURIComponent(vehicleID)}/lease`),
  // Accept the current Terms of Service and Privacy Policy. 409 (version_not_current) when the
  // version moved on: the caller refetches /api/auth/me and shows the new one.
  acceptTerms: (version: string) => api.post<{ ok: boolean; terms: TermsState }>("/api/terms/accept", { version }),
  // Null on 404 (feature off / not wired): the Settings card hides.
  statementEmailGet: async (): Promise<StatementEmailSettings | null> => {
    try {
      return await api.get<StatementEmailSettings>("/api/settings/statement-email");
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  statementEmailPut: (enabled: boolean, timezone?: string) =>
    api.put<StatementEmailSettings>("/api/settings/statement-email", { enabled, timezone }),
  // Welcome and setup-reminder emails (on by default). Null on 404/501: the Settings card hides.
  onboardingEmailGet: async (): Promise<{ enabled: boolean } | null> => {
    try {
      return await api.get<{ enabled: boolean }>("/api/settings/onboarding-email");
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  onboardingEmailPut: (enabled: boolean) => api.put<{ enabled: boolean }>("/api/settings/onboarding-email", { enabled }),
  // The heads-up emails before the Rivian connection expires (on by default). Null on 404/501: the toggle hides. It does not touch
  // the reconnect email sent when the connection has already failed.
  rivianExpiryEmailGet: async (): Promise<{ enabled: boolean } | null> => {
    try {
      return await api.get<{ enabled: boolean }>("/api/settings/rivian-expiry-email");
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  rivianExpiryEmailPut: (enabled: boolean) => api.put<{ enabled: boolean }>("/api/settings/rivian-expiry-email", { enabled }),
  // No month: the server uses the previous month in the user's zone.
  statementEmailPreview: () => api.get<StatementEmailPreview>("/api/statement-email/preview"),
  liveSession: (vehicleID: string) =>
    api.get<LiveSession>(`/api/live-session/${encodeURIComponent(vehicleID)}`),
  // liveDrive returns null when the server replies 204 — no drive
  // session is currently open for the vehicle. We coerce undefined →
  // null at this boundary so TanStack Query's queryFn contract holds
  // (v5 rejects undefined as a query return value); callers can keep
  // treating null/undefined as "not driving" either way.
  liveDrive: async (vehicleID: string): Promise<LiveDrive | null> => {
    // /api/live-drive needs a session; the signed-out demo isn't on its
    // allowlist, so don't ask (it would 401 into the console).
    if (inDemo()) return null;
    const r = await api.get<LiveDrive | undefined>(
      `/api/live-drive/${encodeURIComponent(vehicleID)}`,
    );
    return r ?? null;
  },
  rivianStatus: async (): Promise<RivianStatus> => {
    if (inDemo()) return { enabled: false, authenticated: false, mfa_pending: false };
    return api.get<RivianStatus>("/api/settings/rivian/");
  },
  // vehicle_count on a completed sign-in: how many vehicles the Rivian
  // account exposed (-1 if the lookup failed). 0 means connected but
  // nothing to record - usually an unaccepted Authorized Driver invite.
  rivianLogin: (email: string, password: string) =>
    api.post<{
      authenticated: boolean;
      mfa_pending?: boolean;
      email?: string;
      vehicle_count?: number;
    }>(
      "/api/settings/rivian/login",
      { email, password },
    ),
  rivianMFA: (otp: string) =>
    api.post<{ authenticated: boolean; email?: string; vehicle_count?: number }>(
      "/api/settings/rivian/mfa",
      { otp },
    ),
  // Re-reads the connected Rivian account's vehicles ("Check again").
  rivianRefreshVehicles: () =>
    api.post<{ vehicle_count: number }>("/api/settings/rivian/refresh-vehicles"),
  rivianLogout: () =>
    api.post<{ authenticated: boolean }>("/api/settings/rivian/logout"),
  getUnits: () => api.get<AccountUnits>("/api/settings/units"),
  patchUnits: (body: UnitsPatch) => api.patch<AccountUnits>("/api/settings/units", body),
  getChargingSettings: () =>
    api.get<ChargingSettings>("/api/settings/charging/"),
  setChargingSettings: (cfg: ChargingSettings) =>
    fetch("/api/settings/charging/", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
    }).then(async (res) => {
      const text = await res.text();
      const parsed = text ? JSON.parse(text) : null;
      if (!res.ok) throw new ApiError(res.status, parsed);
      return parsed as ChargingSettings;
    }),
  // The off-peak schedule: windows of the week with their own rate. PUT replaces it; an empty list clears it.
  getOffpeak: () => api.get<OffpeakSchedule>("/api/settings/charging/offpeak"),
  setOffpeak: (cfg: OffpeakSchedule) => api.put<OffpeakSchedule>("/api/settings/charging/offpeak", cfg),
  // "My electricity is one flat rate": stops the off-peak estimates (savings, the share line, the statement saving); the schedule stays.
  setFlatRate: (on: boolean) => api.put<{ flat_rate: boolean }>("/api/settings/charging/flat-rate", { flat_rate: on }),
  // Price book for fast/public charging networks. GET returns the
  // current list (possibly empty); PUT replaces it wholesale.
  getChargingNetworks: () =>
    api.get<ChargingNetwork[]>("/api/settings/charging/networks"),
  setChargingNetworks: (networks: ChargingNetwork[]) =>
    api.put<ChargingNetwork[]>("/api/settings/charging/networks", networks),
  resetChargingNetworks: () =>
    api.post<ChargingNetwork[]>("/api/settings/charging/networks/reset", null),
  // AI provider configuration is admin-only — the keys are install-
  // wide and the operator pays the LLM bill. Routes live under
  // /api/admin/* behind the requireAdminMW gate. Non-admins get a
  // 403 from these calls; the SPA hides the /admin route behind
  // me().role === "admin" so they don't even render the buttons.
  getAISettings: () => api.get<AISettings>("/api/admin/settings/ai"),
  updateAISettings: (patch: AISettingsUpdate) =>
    api.put<AISettings>("/api/admin/settings/ai", patch),
  // Fetch the provider's own model catalogue via its list endpoint,
  // proxied server-side so the API key never hits the browser.
  listAIModels: (provider: AIProvider) =>
    api.get<{ models: string[] }>(
      `/api/admin/settings/ai/models/${encodeURIComponent(provider)}`,
    ),
  // Smoke-test the currently configured AI provider. Sends a trivial
  // prompt and returns the reply + token usage + round-trip latency,
  // so the admin UI can confirm key/model validity without waiting
  // for a downstream feature to exercise the integration.
  pingAI: () => api.post<AIPingResult>("/api/admin/ai/ping", {}),
  // Recap settings live on a separate surface from AI provider
  // config (see RecapSettings). Same admin gating as the AI
  // endpoints because the toggle controls install-wide data egress.
  getRecapSettings: () =>
    api.get<RecapSettings>("/api/admin/settings/recap"),
  updateRecapSettings: (patch: RecapSettingsUpdate) =>
    api.put<RecapSettings>("/api/admin/settings/recap", patch),
  getGPSSettings: () =>
    api.get<GPSSettings>("/api/admin/settings/gps"),
  updateGPSSettings: (patch: GPSSettingsUpdate) =>
    api.put<GPSSettings>("/api/admin/settings/gps", patch),
  pushVAPIDKey: () =>
    api.get<{ public_key: string }>("/api/push/vapid-key"),
  pushStatus: () => api.get<PushStatus>("/api/push/status"),
  // Whether this user has turned AI on (off by default). The AI calls answer 403 {class: "ai_off"} until they have.
  // Null on 404 (a server from before the switch existed).
  aiConsentGet: async (): Promise<{ enabled: boolean } | null> => {
    try {
      return await api.get<{ enabled: boolean }>("/api/settings/ai-consent");
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  },
  // Outside temperature (off by default). Null on 404 (a server without the setting).
  ambientTempGet: async (): Promise<{ enabled: boolean } | null> => {
    try {
      return await api.get<{ enabled: boolean }>("/api/settings/ambient-temp");
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  },
  ambientTempPut: (enabled: boolean) => api.put<{ enabled: boolean }>("/api/settings/ambient-temp", { enabled }),
  aiConsentPut: (enabled: boolean) => api.put<{ enabled: boolean }>("/api/settings/ai-consent", { enabled }),
  // Milestones reached in the last 30 days and not dismissed, newest first. Title and body are the server's
  // words, shown as given. Null when the server has no such call (404).
  milestones: async (): Promise<MilestoneItem[] | null> => {
    try {
      return (await api.get<{ milestones: MilestoneItem[] }>("/api/milestones")).milestones;
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 405)) return null;
      throw e;
    }
  },
  milestoneDismiss: (vehicle_id: string, key: string) => api.post<{ ok?: boolean }>("/api/milestones/dismiss", { vehicle_id, key }),
  pushSubscribe: (body: PushSubscribeBody) =>
    api.post<{ ok: boolean; preferences: PushPreferences }>("/api/push/subscribe", body),
  engagementGet: () => api.get<EngagementSettings>("/api/settings/engagement"),
  engagementPut: (patch: Partial<Omit<EngagementSettings, "timezone_stored">>) =>
    api.put<EngagementSettings>("/api/settings/engagement", patch),
  // This device's stored switches, so the panel shows what the server has. A POST so the endpoint, a
  // capability URL, stays out of access logs. Null when the server does not know the device (404).
  pushDevice: async (endpoint: string): Promise<PushPreferences | null> => {
    try {
      return (await api.post<{ preferences: PushPreferences }>("/api/push/device", { endpoint })).preferences;
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 405)) return null;
      throw e;
    }
  },
  // Turns the plug-in reminder and/or milestones on or off for this device. The subscribe call ignores both
  // switches (so a re-save can never opt anyone in); this is how a tap on either reaches the server.
  pushOptin: (endpoint: string, patch: { on_plug_in_reminder?: boolean; on_milestones?: boolean; on_left_open?: boolean }) =>
    api.post<{ ok?: boolean }>("/api/push/optin", { endpoint, ...patch }),
  // Turns one reminder kind off on every device of the account. Null when the server has no such call yet.
  pushOff: async (kind: "plug_in_reminder" | "milestones" | "left_open"): Promise<{ devices_changed: number } | null> => {
    try {
      return await api.post<{ devices_changed: number }>("/api/push/off", { kind });
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 405)) return null;
      throw e;
    }
  },
  pushUnsubscribe: (endpoint: string) =>
    api.post<{ ok: boolean }>("/api/push/unsubscribe", { endpoint }),
  pushTest: (endpoint: string) =>
    api.post<{ ok: boolean }>("/api/push/test", { endpoint }),
  // Bulk-enrich historical drives with weather snapshots. The
  // server processes a bounded batch per call so a slow upstream
  // can't lock up a worker; callers should poll until
  // `remaining === 0` (or `disabled === true`).
  backfillDriveWeather: () =>
    api.post<DriveWeatherBackfillResult>(
      "/api/drives/weather/backfill",
      {},
    ),
  // Operational flags. GET returns both flags' state (kill switch
  // + trip planner); each PUT flips one. Admin-only on the server.
  adminFlagsGet: () => api.get<AdminFlagsState>("/api/admin/kill-switch"),
  adminFlagsKillPut: (paused: boolean, reason: string) =>
    api.put<AdminFlagsState>("/api/admin/kill-switch", { paused, reason }),
  adminFlagsTripPlannerPut: (enabled: boolean) =>
    api.put<{ trip_planner: { enabled: boolean; actor?: string } }>(
      "/api/admin/flags/trip-planner",
      { enabled },
    ),
  adminFlagsStatementEmailPut: (all: boolean, users: string[]) =>
    api.put<{ statement_email: AudienceFlag }>("/api/admin/flags/statement-email", { all, users }),
  adminFlagsAICallCapPut: (dailyLimit: number) =>
    api.put<{ ai_call_cap: { daily_limit: number; actor?: string } }>("/api/admin/flags/ai-call-cap", { daily_limit: dailyLimit }),
  // Admin audit log (newest first). `next_before` pages: pass it back as `before`.
  adminAudit: (p: { action?: string; limit?: number; before?: number; actor?: string; target_user?: string } = {}) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(p)) if (v !== undefined && v !== "") q.set(k, String(v));
    const qs = q.toString();
    return api.get<AdminAuditPage>(`/api/admin/audit${qs ? `?${qs}` : ""}`);
  },
  // The admin home's health summary. Always 200; each section may be {status:"unavailable"}.
  adminHealthSummary: () => api.get<import("./adminHealth").HealthSummary>("/api/admin/health-summary"),
  adminSignupCapGet: () =>
    api.get<AdminSignupCapState>("/api/admin/signup-cap"),
  adminSignupCapPut: (limit: number) =>
    api.put<AdminSignupCapState>("/api/admin/signup-cap", { limit }),
  // Admin user management. Same gating as the AI endpoints.
  adminListUsers: () =>
    api.get<{ users: AdminUserRow[] }>("/api/admin/users"),
  // Pre-provision a user row. Auth is OIDC-only; this does NOT
  // issue a password unless the server has the IdP integration
  // configured (Kratos) — in which case the response also carries
  // a one-time `password` field that the admin must deliver
  // out-of-band before the next page reload (it is never
  // persisted on the rivolt side).
  adminCreateUser: (input: {
    email: string;
    display_name?: string;
    role: "user" | "admin";
    disabled?: boolean;
  }) =>
    api.post<{
      id: string;
      password?: string;
      idp_provisioned?: boolean;
    }>("/api/admin/users", input),
  adminSetUserRole: (id: string, role: "user" | "admin") =>
    api.post<{ ok: true }>(`/api/admin/users/${encodeURIComponent(id)}/role`, {
      role,
    }),
  // Toggles the per-user disabled flag. Disabling clears every
  // existing session on the next request (the auth middleware re-
  // checks). Server enforces last-admin / self-disable guards.
  adminSetUserDisabled: (id: string, disabled: boolean) =>
    api.post<{ ok: true }>(
      `/api/admin/users/${encodeURIComponent(id)}/disabled`,
      { disabled },
    ),
  // Deep-dive bundle for a single user, used by the admin drawer.
  // Includes identity, Rivian session state, activity rollups,
  // vehicles + telemetry recency, and signup origin.
  adminUserDetail: (id: string) =>
    api.get<AdminUserDetail>(`/api/admin/users/${encodeURIComponent(id)}`),
  // Force-prime the user's vehicles table from Rivian — used to fix
  // up accounts that connected before the eager-prime fix shipped or
  // any case where the local vehicles row drifted from the Rivian
  // account. Idempotent.
  // One audited action for an account someone else may hold: a new random password (returned once, never stored), every session and
  // remembered sign-in ended, every passkey removed, the owner emailed. Safe to run again after a partial failure (502 `incomplete`
  // names the step and carries no password).
  adminSecureUser: (id: string) =>
    api.post<{ ok: true; temporary_password: string; sessions_revoked: number; passkeys_removed: number }>(
      `/api/admin/users/${encodeURIComponent(id)}/secure`,
    ),
  adminSyncRivian: (id: string) =>
    api.post<{ ok: true; vehicle_count: number }>(
      `/api/admin/users/${encodeURIComponent(id)}/sync-rivian`,
    ),
  // Force a fresh a-sess/csrf and probe the user's Rivian session.
  // healed=true means the session was salvageable (stale csrf) and
  // needs_reauth was cleared; healed=false means the u-sess is dead
  // and the user must re-authenticate with password + OTP.
  adminRefreshRivianSession: (id: string) =>
    api.post<{ ok: boolean; healed: boolean; message: string; error?: string }>(
      `/api/admin/users/${encodeURIComponent(id)}/refresh-rivian-session`,
    ),
  adminDeleteUser: (id: string) =>
    fetch(`/api/admin/users/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }).then(async (res) => {
      const text = await res.text();
      const parsed = text ? JSON.parse(text) : null;
      if (!res.ok) throw new ApiError(res.status, parsed);
      return parsed as {
        ok: true;
        // Account deletion also removes the sign-in identity; "failed"
        // means the Rivolt account is gone but the identity remains.
        idp?: "deleted" | "failed" | "skipped";
        idp_error?: string;
      };
    }),
  adminIdpFailures: () =>
    api.get<IdpDeleteFailure[]>("/api/admin/idp-delete-failures"),
  // 200 resolved; 502 still failing; 409 the account exists again.
  adminRetryIdpFailure: (id: string) =>
    api.post<{ resolved: boolean }>(
      `/api/admin/idp-delete-failures/${encodeURIComponent(id)}/retry`,
    ),
  // Re-stamps every charge's recorded place against the currently saved
  // home (contract A15). dryRun counts without writing. Prices are never
  // touched. 409 when no home is saved.
  // #906: the user's own home/away for one charge (null hands it back to the home location), and whether its map pins
  // where the truck was. Web only; 204.
  setChargePlace: (id: string, place: "home" | "away" | null) =>
    api.put<void>(`/api/charges/${encodeURIComponent(id)}/place`, { place }),
  setChargeLocation: (id: string, recorded: boolean) =>
    api.put<void>(`/api/charges/${encodeURIComponent(id)}/location`, { recorded }),
  restampPlace: (dryRun: boolean) =>
    api.post<RestampResult>(`/api/charges/restamp-place${dryRun ? "?dry_run=1" : ""}`, null),
  // Undoes a correction (latest when restampId is omitted). 404 when
  // there is nothing to undo.
  restampPlaceUndo: (restampId?: string | null) =>
    api.post<RestampUndoResult>(
      `/api/charges/restamp-place/undo${restampId ? `?restamp_id=${encodeURIComponent(restampId)}` : ""}`,
      null,
    ),
  // Pre-account waitlist. Public requestSignup is used by /signup
  // when the visitor has no invite code. Admin endpoints sit under
  // the same role gate as the rest of /api/admin.
  requestSignup: (body: { email: string; message?: string }) =>
    api.post<{ ok: boolean; already_pending?: boolean; auto_approved?: boolean }>("/api/signup/request", body),
  // Magic-link prefill. Returns 410 when the token is invalid /
  // expired / used; the SPA branches on that to show a "request a
  // new invite" view.
  signupTokenLookup: (token: string) =>
    api.get<{ email: string; expires_at?: string }>(
      `/api/signup/token/${encodeURIComponent(token)}`,
    ),
  adminListSignupRequests: (status: "pending" | "approved" | "rejected" | "" = "pending") =>
    api.get<{ requests: SignupRequest[] }>(
      `/api/admin/signup-requests${status ? `?status=${status}` : ""}`,
    ),
  adminApproveSignupRequest: (id: string) =>
    api.post<{ request: SignupRequest; link?: string; email_sent: boolean }>(
      `/api/admin/signup-requests/${id}/approve`,
      {},
    ),
  adminRejectSignupRequest: (id: string) =>
    api.post<{ request: SignupRequest }>(`/api/admin/signup-requests/${id}/reject`, {}),
  // Local DBSCAN clustering of charge locations. Returns one row per
  // cluster, largest-first, with "Home" / "Public" / "Fast" labels.
  chargeClusters: () =>
    api.get<ChargeCluster[]>("/api/charges/clusters"),
  drives: (limit = 50) => api.get<Drive[]>(`/api/drives?limit=${limit}`),
  charges: (limit = 50) => api.get<Charge[]>(`/api/charges?limit=${limit}`),
  // `allDrives` / `allCharges` pull enough history to drive the
  // overview analytics and detail-page lookups without paginating.
  // The store queries cap out at a few hundred rows so this stays cheap.
  // slim leaves the route polyline out of each row (the lists draw no maps): most of the weight of the full list.
  allDrives: (o: { slim?: boolean } = {}) => api.get<Drive[]>(`/api/drives?limit=5000${o.slim ? "&slim=1" : ""}`),
  // One drive with its route polyline and cost estimate. 404 for an unknown id or someone else's drive.
  drive: (id: string) => api.get<Drive>(`/api/drives/${encodeURIComponent(id)}`),
  // The user's own correction of whether a trailer was connected on a drive: true, false, or null to go back to what the truck reported.
  driveTrailer: (id: string, trailer: boolean | null) =>
    api.patch<{ trailer_override: boolean | null }>(`/api/drives/${encodeURIComponent(id)}/trailer`, { trailer }),
  allCharges: () => api.get<Charge[]>(`/api/charges?limit=5000`),
  recentCharges: (n: number) => api.get<Charge[]>(`/api/charges?limit=${n}`),
  // Per-day asleep/awake hours for the overview sleep graph. `since`
  // is RFC3339; `vehicle` scopes to one car (optional).
  sleepActivity: (since: string, vehicleID?: string, until?: string) =>
    api.get<DayActivity[]>(
      `/api/sleep-activity?since=${encodeURIComponent(since)}` +
        (until ? `&until=${encodeURIComponent(until)}` : "") +
        (vehicleID ? `&vehicle=${encodeURIComponent(vehicleID)}` : ""),
    ),
  // Per-truck trends (monthly efficiency, estimated usable capacity). Null when the server has no such endpoint.
  trends: async (vehicleID: string, units: "imperial" | "metric", months = 12): Promise<TruckTrends | null> => {
    try {
      return await api.get<TruckTrends>(`/api/vehicles/${encodeURIComponent(vehicleID)}/trends?months=${months}&units=${units}`);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  // Range at a given outside temperature, from the user's own drives. Null when the server has no such endpoint.
  coldRange: async (vehicleID: string, tempC: number, gridC: number[], units: "imperial" | "metric"): Promise<ColdRange | null> => {
    const q = new URLSearchParams({ temp_c: String(tempC), units });
    if (gridC.length > 0) q.set("grid", gridC.join(","));
    try {
      return await api.get<ColdRange>(`/api/vehicles/${encodeURIComponent(vehicleID)}/cold-range?${q}`);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  packHealth: (vehicleID: string) =>
    api.get<PackHealthResponse>(
      `/api/vehicles/${encodeURIComponent(vehicleID)}/pack-health`,
    ),
  // Removes a single charge row by its external ID. Used by the
  // detail page's danger-zone affordance to clear obviously-broken
  // sessions (e.g. pre-v0.10.7 phantom rows).
  deleteCharge: (id: string) =>
    api.del<void>(`/api/charges/${encodeURIComponent(id)}`),
  // Overrides the persisted cost / currency / price-per-kWh on a
  // single charge. Useful for paid-outside-Rivian DCFC sessions
  // where Rivian doesn't surface a price; otherwise our drive cost
  // model has to fall back to the home rate, which underestimates.
  // Pass zeros / empty string to clear a field and let the
  // recent-charge or home-rate fallbacks take over again.
  // Prices this network's tracker-estimated and unpriced fast sessions at the user's own saved rate (never a user price, bill,
  // receipt or a session still recording). 400 when the user has no priced row for the network; priced: 0 when run again.
  applyNetworkRate: (networkSlug: string) =>
    api.post<{ priced: number; network: string }>("/api/charges/apply-rate", { network_slug: networkSlug }),
  updateChargePricing: (
    id: string,
    // free:true marks the session explicitly free (contract §12, cost 0,
    // source user). {} or cost 0 still clears the price, which is how
    // Free is undone. free + a non-zero cost is a 400.
    body: {
      cost?: number;
      currency?: string;
      price_per_kwh?: number;
      free?: boolean;
      // Change to a known network: re-priced at the user's rate there.
      network_slug?: string;
      network?: string;
    },
  ) =>
    api.patch<void>(`/api/charges/${encodeURIComponent(id)}/pricing`, body),
  // Confirm the auto-detected network as is (409 if nothing to confirm).
  confirmChargePricing: (id: string) =>
    api.post<void>(`/api/charges/${encodeURIComponent(id)}/pricing/confirm`, null),
  // `until` is required on purpose: the server downsamples explicit
  // windows only, so an open-ended request would thin an old session
  // across its whole span up to now.
  // `vehicle` (Rivian vehicle id) scopes the window to one truck; without it
  // the server returns every vehicle on the account interleaved by time.
  samples: (since: Date, limit: number, until: Date, vehicle?: string | null) => {
    const params = new URLSearchParams({
      since: since.toISOString(),
      until: until.toISOString(),
      limit: String(limit),
    });
    if (vehicle) params.set("vehicle", vehicle);
    return api.get<Sample[]>(`/api/samples?${params.toString()}`);
  },
  // Efficiency analysis: AI-driven breakdown of what drove efficiency
  // variance for a drive, with actionable recommendations. POST runs
  // a fresh analysis (re-bills the LLM account) and persists the
  // result; GET fetches the stored result so the SPA can show a
  // previously-analyzed drive on page mount without re-billing. The
  // optional POST body carries per-trip transient context (extra
  // cargo, the user's chosen temperature unit so prose mentions °F
  // or °C consistently). Towing is auto-detected server-side from
  // the persisted driveMode samples.
  driveEfficiencyGenerate: (
    id: string,
    body?: {
      extra_load_lb?: number;
      temperature_unit?: "c" | "f";
    },
  ) =>
    api.post<DriveEfficiency>(
      `/api/drives/${encodeURIComponent(id)}/efficiency`,
      body ?? {},
    ),
  driveEfficiencyGet: async (id: string): Promise<DriveEfficiency | null> => {
    try {
      return await api.get<DriveEfficiency>(
        `/api/drives/${encodeURIComponent(id)}/efficiency`,
      );
    } catch (e) {
      // 404 = no analysis stored yet; surface it to the caller as
      // null so the card can fall through to the empty-state form.
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  },
  // Per-vehicle profile (tire type, wheel size, accessories, default
  // extra load, frequently_tows). Persisted in vehicles.metadata.profile;
  // pulled into every efficiency analysis. The path param is the
  // Rivian gateway vehicle id.
  vehicleProfileGet: async (vehicleID: string): Promise<VehicleProfile> => {
    // Per-account tire/wheel profile: nothing to read for the sample truck.
    if (inDemo()) return {};
    return api.get<VehicleProfile>(`/api/vehicles/${encodeURIComponent(vehicleID)}/profile`);
  },
  vehicleProfilePut: (vehicleID: string, profile: VehicleProfile) =>
    api.put<VehicleProfile>(
      `/api/vehicles/${encodeURIComponent(vehicleID)}/profile`,
      profile,
    ),
  // Standalone weather snapshot for a drive. Returns the same DTO
  // attached to recap responses, but works independently of whether
  // an AI recap was ever generated -- so the detail-page chart can
  // render the outside-temp line even on un-narrated drives.
  driveWeatherGet: (id: string) =>
    api.get<DriveWeather>(`/api/drives/${encodeURIComponent(id)}/weather`),
  // Time-series sibling of driveWeatherGet. Returns an empty array
  // (not 404) when the drive was never enriched, so the SPA can
  // render a "backfill needed" affordance without a special branch.
  driveWeatherSeries: (id: string) =>
    api.get<DriveWeatherSeries>(
      `/api/drives/${encodeURIComponent(id)}/weather/series`,
    ),
  // User's saved home location. Read by the trip planner to render
  // a "Home" preset on Origin/Destination; settable from the
  // Settings page.
  homeLocationGet: () => api.get<HomeLocation>("/api/settings/home-location"),
  homeLocationPut: (h: HomeLocation) =>
    api.put<HomeLocation>("/api/settings/home-location", h),
  // Back to the home the latest change replaced. 404 when there is none.
  homeLocationRestore: () => api.post<HomeLocation>("/api/settings/home-location/restore", null),
  // A street address on this server's own Photon (the address in the body, never the URL). 404 without Photon.
  homeAddressSearch: (q: string) => api.post<GeocodeResult[]>("/api/settings/home-location/search", { q }),
  // Trip-planner default drive mode + Tesla NACS adapter flag.
  // SPA pre-fills the per-trip form from these.
  // In the demo these live in browser memory only (no request, nothing saved).
  // Public demo only: the preset trips (stored plans in the real planner's
  // response shape). There is no demo planner compute.
  demoTrips: () => api.get<{ trips: DemoTripPreset[] }>("/api/demo/trips"),
  // Release notes (public): the hand-written notes of each release, newest first.
  changelog: () => api.get<{ releases: ChangelogRelease[] }>("/api/changelog"),
  demoTripPlan: (id: string) => api.get<TripPlan | SavedMultidayPlan>(`/api/demo/trips/${encodeURIComponent(id)}/plan`),
  plannerFavoritesGet: async (): Promise<PlannerFavorite[]> =>
    inDemo() ? [] : api.get<PlannerFavorite[]>("/api/settings/planner/favorites"),
  plannerFavoritesPut: async (list: PlannerFavorite[]): Promise<PlannerFavorite[]> =>
    inDemo() ? list : api.put<PlannerFavorite[]>("/api/settings/planner/favorites", list),
  plannerPrefsGet: async (): Promise<PlannerPrefs> =>
    inDemo() ? { drive_mode: "EVERYDAY", has_adapter: false } : api.get<PlannerPrefs>("/api/settings/planner"),
  plannerPrefsPut: async (p: PlannerPrefs): Promise<PlannerPrefs> =>
    inDemo() ? p : api.put<PlannerPrefs>("/api/settings/planner", p),
  // Geocoding for the trip planner: free-text → city/town
  // suggestions with lat/lon. Backed by Open-Meteo, sorted by
  // population. Empty array when no match.
  geocode: (q: string, count = 5) =>
    api.get<GeocodeResult[]>(
      `/api/geocode?q=${encodeURIComponent(q)}&count=${count}`,
    ),
  // Trip planner — pass-through to Rivian's planTripWithMultiStop.
  // Slice 1: read-only, no AI, no save. The caller supplies origin
  // + destination (+ optional intermediate waypoints) and an
  // optional target arrival SoC; vehicle_id and starting_soc are
  // back-filled by the server from the live state cache when omitted.
  planTrip: (req: TripPlanRequest) =>
    api.post<TripPlan>("/api/trips/plan", req),
  planTripMultiday: (req: TripPlanMultidayRequest) =>
    api.post<TripPlanMultidayResponse>("/api/trips/plan-multiday", req),
  chargersAlongPath: (req: {
    path: [number, number][];
    filter?: "dcfc" | "l2" | "hotels" | "camp" | "all";
    corridor_km?: number;
    min_power_kw?: number;
  }) =>
    api.post<{
      chargers: {
        lat: number;
        lon: number;
        name?: string;
        isDCFC: boolean;
        isL2: boolean;
        maxPowerKW?: number;
        facilityType?: string;
        evNetwork?: string;
        evPricing?: string;
        entityID?: string;
        dcfcCount?: number;
        l2Count?: number;
        socketTeslaSupercharger?: boolean;
        socketType1Combo?: boolean;
        socketChademo?: boolean;
        // Recreation.gov (RIDB) campgrounds only.
        hookupAmps?: number;
        electricSites?: number;
        sites?: number;
        reservable?: boolean;
        ridbID?: string;
        asof?: string;
        source?: string;
      }[];
      count: number;
    }>("/api/maps/chargers-along", req),
  // Hotels with a listed Level 2 charger near where the day's driving budget runs out. Archive
  // only: no Rivian call. Null on 404 (no archive on this install).
  overnightCandidates: async (req: OvernightCandidatesRequest): Promise<OvernightCandidatesResponse | null> => {
    try {
      return await api.post<OvernightCandidatesResponse>("/api/trips/overnight-candidates", req);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  },
  planTripAdvice: (req: TripAdviceRequest) =>
    api.post<TripAdvice>("/api/trips/plan/advice", req),
  // Named trip templates. Inputs are the form state the user typed
  // in; plan + advice are optional snapshots so reopening a saved
  // trip can render the result instantly while still letting the
  // user re-plan against current station / weather state.
  savedTripsList: () => api.get<SavedTrip[]>("/api/trips/saved"),
  // Read-only and cheap (no Rivian call). Null on 404 (not the user's trip).
  savedTripActive: async (id: string): Promise<SavedTripActive | null> => {
    try {
      return await api.get<SavedTripActive>(`/api/trips/saved/${encodeURIComponent(id)}/active`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  },
  // Null on 404 (not the user's trip), so the section
  // simply doesn't render.
  planVsActual: async (savedTripID: string): Promise<PlanVsActual | PlanVsActualMulti | null> => {
    try {
      return await api.get<PlanVsActual | PlanVsActualMulti>(`/api/trips/saved/${encodeURIComponent(savedTripID)}/actual`);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 501)) return null;
      throw e;
    }
  },
  // A multi-day trip links each day to its own outing: day (1..N) is required there and refused otherwise.
  planVsActualLink: (savedTripID: string, tripID: string, day?: number) =>
    api.post<PlanVsActual>(`/api/trips/saved/${encodeURIComponent(savedTripID)}/link`, { trip_id: tripID, ...(day ? { day } : {}) }),
  // Records the rejection of this outing (never suggested again); for a
  // linked trip it also removes the link. trip_id is required (v1.1).
  planVsActualUnlink: (savedTripID: string, tripID: string, day?: number) =>
    api.post<PlanVsActual>(`/api/trips/saved/${encodeURIComponent(savedTripID)}/unlink`, { trip_id: tripID, ...(day ? { day } : {}) }),
  savedTripCreate: (body: SavedTripBody) =>
    api.post<SavedTrip>("/api/trips/saved", body),
  savedTripUpdate: (id: string, body: SavedTripBody) =>
    api.put<SavedTrip>(`/api/trips/saved/${encodeURIComponent(id)}`, body),
  savedTripDelete: (id: string) =>
    api.del<void>(`/api/trips/saved/${encodeURIComponent(id)}`),
  // Multipart upload of one or more ElectraFi CSV files. Returns a per-
  // file result summary (rows/samples/drives/charges ingested).
  // onProgress, when provided, is called for each server-emitted NDJSON
  // event (file_start / progress / file_done) so the UI can render a
  // live "row N of file.csv" status during long imports.
  importElectrafi: async (
    files: File[],
    vehicleID: string,
    packKWh?: number,
    tz?: string,
    onProgress?: (p: ImportProgress) => void,
  ) => {
    const fd = new FormData();
    for (const f of files) fd.append("file", f, f.name);
    fd.append("vehicle_id", vehicleID);
    if (packKWh && packKWh > 0) fd.append("pack_kwh", String(packKWh));
    // ElectraFi timestamps are local-without-zone. Default to the
    // browser's IANA zone so imports land on the date the user
    // actually drove/charged, not shifted by their UTC offset.
    const zone =
      tz && tz.trim()
        ? tz
        : Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    fd.append("tz", zone);
    const res = await fetch("/api/import/electrafi", { method: "POST", body: fd });
    if (!res.ok) {
      const text = await res.text();
      let parsed: unknown = text;
      try {
        parsed = JSON.parse(text);
      } catch {
        // keep as text
      }
      throw new ApiError(res.status, parsed);
    }

    // The server streams NDJSON (progress events + final {done:true})
    // so the reverse proxy doesn't time out on long imports. Read the
    // stream line-by-line; the last event is either {event:"done",
    // files:[...]} or {event:"error", file, error}.
    if (!res.body) throw new ApiError(500, "no response body");
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    let done: { files: ImportResult[] } | null = null;
    let err: { file?: string; error: string } | null = null;
    const consumeLine = (line: string) => {
      if (!line.trim()) return;
      let ev: Record<string, unknown>;
      try {
        ev = JSON.parse(line);
      } catch {
        return;
      }
      if (ev.event === "done" && Array.isArray(ev.files)) {
        done = { files: ev.files as ImportResult[] };
      } else if (ev.event === "error") {
        err = { file: ev.file as string, error: ev.error as string };
      } else if (onProgress) {
        // Forward start / file_start / progress / file_done so the
        // UI can render a live status line.
        onProgress(ev as unknown as ImportProgress);
      }
    };
    for (;;) {
      const { value, done: eof } = await reader.read();
      if (value) {
        buf += dec.decode(value, { stream: true });
        let idx: number;
        while ((idx = buf.indexOf("\n")) >= 0) {
          consumeLine(buf.slice(0, idx));
          buf = buf.slice(idx + 1);
        }
      }
      if (eof) {
        if (buf) consumeLine(buf);
        break;
      }
    }
    if (err) throw new ApiError(400, (err as { error: string }).error);
    if (!done) throw new ApiError(500, "import stream ended without done event");
    return done as { files: ImportResult[] };
  },

  // Streams a full JSON backup (drives + charges + samples) into a
  // browser download. Returns the blob size in bytes for the UI
  // confirmation message. Does not keep anything server-side.
  backupData: async () => {
    const res = await fetch("/api/data/backup");
    if (!res.ok) {
      const body = await res.text();
      throw new ApiError(res.status, body);
    }
    const blob = await res.blob();
    // Prefer the server's suggested filename (Content-Disposition).
    const cd = res.headers.get("Content-Disposition") || "";
    const m = cd.match(/filename="?([^";]+)"?/i);
    const filename = m?.[1] || `rivolt-backup-${Date.now()}.json`;
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
    return { filename, bytes: blob.size };
  },

  // Wipes drives + charges + samples for the current user.
  // Vehicles, settings, push subscriptions, and the user row
  // are preserved. Returns per-table deleted counts.
  resetSessions: () =>
    api.del<{ drives: number; charges: number; samples: number }>(
      "/api/data/sessions",
    ),

  // Self-service account deletion. Cascades through every per-user
  // table (drives, charges, samples, settings, vehicle profile,
  // user_secrets, push_subscriptions, sessions) and removes the
  // user from the IdP. Returns 204 No Content from the auth
  // service's Logout handler — the session cookie is cleared so
  // subsequent requests are unauthenticated. Refuses with 409 if
  // the caller is the install's last admin.
  // The server wants the confirmation as a JSON body (a bare DELETE is refused), so a stray or replayed call cannot delete.
  deleteAccount: async () => {
    const res = await fetch("/api/data/account", {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ confirm: "delete my account" }),
    });
    if (!res.ok) {
      const text = await res.text();
      let msg = text;
      try {
        const j = JSON.parse(text) as { error?: string };
        if (j.error) msg = j.error;
      } catch { /* keep text */ }
      throw new ApiError(res.status, res.statusText, msg);
    }
  },

  // Uploads a JSON bundle previously produced by backupData() and
  // upserts every drive, charge, and sample back. Safe to re-run
  // (drives/charges upsert by external_id; samples dedupe by
  // (vehicle_id, at)). Returns per-table processed counts.
  restoreData: async (file: File, onProgress?: (p: RestoreProgress) => void) => {
    const res = await fetch("/api/data/restore", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: file,
    });
    if (!res.ok) {
      // Refused before it started (not signed in, too many restores, ...).
      const text = await res.text();
      let parsed: unknown = text;
      try {
        parsed = JSON.parse(text);
      } catch {
        // non-JSON error body; keep as text
      }
      throw new ApiError(res.status, parsed);
    }
    // The server answers at once and streams NDJSON ({event:"start"},
    // {event:"progress",...} every few seconds, then {event:"done",...} or
    // {event:"error",status,error}), so a long restore never goes quiet past
    // the proxy's timeout.
    if (!res.body) throw new ApiError(500, "no response body");
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    let done: RestoreProgress | null = null;
    let err: { status: number; error: string } | null = null;
    const consumeLine = (line: string) => {
      if (!line.trim()) return;
      let ev: Record<string, unknown>;
      try {
        ev = JSON.parse(line);
      } catch {
        return;
      }
      if (ev.event === "done") {
        done = ev as unknown as RestoreProgress;
      } else if (ev.event === "error") {
        err = { status: Number(ev.status) || 500, error: String(ev.error ?? "restore failed") };
      } else if (onProgress) {
        onProgress(ev as unknown as RestoreProgress);
      }
    };
    for (;;) {
      const { value, done: eof } = await reader.read();
      if (value) {
        buf += dec.decode(value, { stream: true });
        let idx: number;
        while ((idx = buf.indexOf("\n")) >= 0) {
          consumeLine(buf.slice(0, idx));
          buf = buf.slice(idx + 1);
        }
      }
      if (eof) {
        if (buf) consumeLine(buf);
        break;
      }
    }
    if (err) {
      const e = err as { status: number; error: string };
      throw new ApiError(e.status, { error: e.error });
    }
    if (!done) {
      // The connection ended without a final event (a proxy timeout or a
      // dropped connection): the restore may have stopped partway.
      throw new ApiError(500, { error: "the restore stream ended early" });
    }
    return done as RestoreProgress;
  },
};

// A restore progress / final event.
export type RestoreProgress = {
  event: string;
  drives: number;
  charges: number;
  samples: number;
  skipped?: number;
  // Samples older than the install keeps raw samples (their drives and charges are restored).
  samples_past_retention?: number;
};

export type ImportResult = {
  File: string;
  Rows: number;
  Samples: number;
  Drives: number;
  Charges: number;
  SkippedRows: number;
};

// ImportProgress is a single server-emitted NDJSON event for the
// streaming /api/import/electrafi endpoint. The final {event:"done"}
// and any {event:"error"} are consumed internally; everything else
// (start, file_start, progress, file_done) is forwarded via the
// onProgress callback so the UI can render a live status line.
export type ImportProgress = {
  event: "start" | "file_start" | "progress" | "file_done";
  index?: number;
  file?: string;
  files?: number;
  phase?: string;
  rows?: number;
  result?: ImportResult;
};

// AuthUser is whatever /api/auth/me returns — user_id, the
// display username, and the role flag the SPA uses to decide
// whether to render the /admin route + nav link.
// One of the account's passkeys: a name the person gave, when it was added and last used, and whether it can be backed up (iCloud
// Keychain, a password manager) rather than living on one device.
export type Passkey = { id: string; label: string; created_at: string; last_used_at: string | null; synced: boolean };

export type AuthUser = {
  user_id: string;
  username: string;
  role: "user" | "admin";
  onboarding_completed: boolean;
  // The Terms version the user has accepted vs the current one; absent on older servers.
  terms?: TermsState;
  // The account's display units (absent on a server without the setting); the same object as GET /api/settings/units.
  units?: AccountUnits;
  // True when this server offers passkeys to the user (absent where they are off): the Passkeys card asks nothing otherwise.
  passkeys?: boolean;
};

// How the account shows distance and temperature (the server always stores km and °C). A source of "unset" means nothing was
// chosen yet and the values are placeholders; "region" was seeded once from the client's region; "user" is a pick.
export type AccountUnits = {
  distance_unit: "mi" | "km";
  temperature_unit: "c" | "f";
  distance_source: "user" | "region" | "unset";
  temperature_source: "user" | "follows_distance";
};
export type UnitsPatch = { distance_unit?: "mi" | "km"; temperature_unit?: "c" | "f" | "auto"; seed?: true };

// required: only new accounts (created after the terms took effect) must accept; existing users never see the gate.
export type TermsState = { current: string; accepted: string | null; accepted_at: string | null; required?: boolean };

// AdminFlagsState is the response shape from GET /api/admin/kill-switch.
// Carries every operational flag — currently the Rivian-upstream kill
// switch and the trip-planner feature flag — with their audit fields.
export type AdminFlagsState = {
  kill_switch: { paused: boolean; reason?: string; actor?: string };
  trip_planner: { enabled: boolean; actor?: string };
  // Who a feature is on for: everyone, or the listed user ids (empty: nobody).
  statement_email?: AudienceFlag;
  ai_call_cap?: { daily_limit: number; actor?: string }; // <= 0: no cap
};
export type AudienceFlag = { all?: boolean; users?: string[]; actor?: string };

// One admin action from GET /api/admin/audit (internal/adminaudit.Row).
export type AdminAuditRow = {
  id: number;
  at: string;
  actor_user_id: string;
  action: string;
  target_type: string;
  target_id: string;
  target_user_id?: string;
  details: Record<string, unknown>;
  ip: string;
  access_email?: string; // the Cloudflare Access identity, when the request carried one
  outcome: string | null; // "ok" | "failed"; null: in flight or unknown
  status?: number;
  finished_at?: string;
};
export type AdminAuditPage = { items: AdminAuditRow[]; next_before: number | null };

// AdminSignupCapState is the response shape from /api/admin/signup-cap.
// Carries the cap (Limit, Actor) plus the live user count so the UI can
// render "N of M seats used" without a second round trip.
export type AdminSignupCapState = {
  signup_cap: { limit: number; actor?: string };
  used: number;
};

export type RestampResult = {
  dry_run: boolean;
  changed: number;
  to_home: number;
  to_away: number;
  unchanged: number;
  skipped_user_priced: number;
  // Charges whose place the user set by hand (#906): never re-checked. Absent on older servers.
  skipped_overridden?: number;
  // A15b: charges that become home AC and unpriced get the home rate.
  // Absent on older servers.
  would_price_at_home_rate?: number;
  total_cost_delta?: number;
  currency?: string;
  // Null on a dry run, or when nothing changed; needed for undo.
  restamp_id?: string | null;
};

export type RestampUndoResult = { restamp_id: string; restored_place: number; unpriced: number };

// IdpDeleteFailure is one pending identity-provider delete failure
// (GET /api/admin/idp-delete-failures).
export type IdpDeleteFailure = {
  id: string;
  username: string;
  error: string;
  attempts: number;
  failed_at: string;
  last_tried: string;
  // True when the person signed back in and a Rivolt account exists
  // again: Retry isn't offered; delete that account instead.
  account_exists: boolean;
};

// AdminUserRow is one entry from GET /api/admin/users.
export type AdminUserRow = {
  id: string;
  username: string;
  email?: string;
  display_name?: string;
  role: "user" | "admin";
  disabled: boolean;
  created_at: string;
  vehicle_count: number;
  drive_count: number;
  import_count: number;
  rivian_connected: boolean;
  needs_reauth: boolean;
  needs_reauth_at?: string | null;
  last_seen_at?: string | null;
  // The columns below arrive with the admin-split API additions; until the server sends them the
  // table shows "-" (undefined = not supplied, null = supplied but empty).
  last_data_at?: string | null; // newest vehicle_state across the user's vehicles
  leased?: boolean; // a vehicle is leased to a pod for streaming (an unexpired subscription lease)
  receipts_opt_in?: boolean;
  purge_warned_at?: string | null; // inactive-account purge warning sent
};

// AdminUserVehicle is one vehicle inside an AdminUserDetail bundle.
export type AdminUserVehicle = {
  id: string;
  rivian_vehicle_id: string;
  vin?: string;
  display_name?: string;
  model?: string;
  model_year?: number;
  pack_kwh?: number;
  drive_count: number;
  charge_count: number;
  last_sample_at?: string | null;
};

// AdminSignupSnapshot captures how a user joined the system. Nil
// for direct admin-created users.
export type AdminSignupSnapshot = {
  requested_at: string;
  decided_at?: string | null;
  status: "pending" | "approved" | "rejected";
  message?: string;
};

// AdminUserDetail is the deep-dive bundle from GET /api/admin/users/{id}.
export type AdminUserDetail = {
  id: string;
  username: string;
  email?: string;
  display_name?: string;
  role: "user" | "admin";
  disabled: boolean;
  onboarding_completed: boolean;
  created_at: string;
  updated_at: string;
  rivian_connected: boolean;
  rivian_session_at?: string | null;
  needs_reauth: boolean;
  needs_reauth_reason?: string;
  needs_reauth_at?: string | null;
  last_seen_at?: string | null;
  active_sessions: number;
  drive_count: number;
  drive_miles_total: number;
  charge_count: number;
  charge_kwh_total: number;
  sample_count: number;
  oldest_sample_at?: string | null;
  newest_sample_at?: string | null;
  import_count: number;
  vehicles: AdminUserVehicle[];
  signup_request?: AdminSignupSnapshot | null;
};

// SignupRequest is one row from GET /api/admin/signup-requests.
export type SignupRequest = {
  id: string;
  email: string;
  message: string;
  status: "pending" | "approved" | "rejected";
  signup_token?: string | null;
  token_expires_at?: string | null;
  token_used_at?: string | null;
  decided_by?: string | null;
  decided_at?: string | null;
  requested_at: string;
};

// OIDCProvider is one entry in /api/auth/oidc/. The SPA renders
// one button per entry; clicking sends the browser to start_url
// where the server kicks off the auth-code flow.
export type OIDCProvider = {
  name: string;
  display_name: string;
  start_url: string;
};

// PlannerPrefs are the user's saved trip-planner defaults.
// drive_mode is one of "EVERYDAY" / "DISTANCE" / "SPORT" /
// "WINTER" / "OFF_ROAD_AUTO".
// has_adapter is the Tesla NACS adapter availability; absent on the
// wire when unset.
export type PlannerPrefs = {
  drive_mode:
    | ""
    | "EVERYDAY"
    | "DISTANCE"
    | "SPORT"
    | "WINTER"
    | "OFF_ROAD_AUTO";
  has_adapter?: boolean;
};

// PlannerFavorite is one user-saved place surfaced as a preset on
// the trip planner's From/To rows. id is a stable client-minted
// UUID so renames/deletes can target a single entry.
export type PlannerFavorite = {
  id: string;
  label: string;
  latitude: number;
  longitude: number;
};

// HomeLocation is the user's saved "home" base. set=false means no
// home is configured — the planner UI should hide the Home preset.
export type HomeLocation = {
  set: boolean;
  latitude: number;
  longitude: number;
  // Short user-visible name ("Home"). Defaults to "Home" server-side.
  label?: string;
  // Full geocoded address, kept as Settings-card subtitle context.
  address?: string;
  // How it was set: "city", "address", "coordinates" or "charge" (absent before it was recorded).
  source?: "city" | "address" | "coordinates" | "charge" | "";
  // Read-only, from GET/PUT: an earlier home can be restored; a street address can be looked up (Photon).
  previous_set?: boolean;
  address_search?: boolean;
};

// GeocodeResult is one match from /api/geocode (Open-Meteo).
// admin1 is the state/province; useful to disambiguate "Dallas, TX"
// vs "Dallas, OR".
export type GeocodeResult = {
  name: string;
  latitude: number;
  longitude: number;
  country?: string;
  country_code?: string;
  admin1?: string;
  population?: number;
  timezone?: string;
};

// TripPlanRequest is the body for POST /api/trips/plan. vehicle_id
// and starting_soc may be omitted — the server fills them from the
// live state cache.
export type TripPlanRequest = {
  vehicle_id?: string;
  starting_soc?: number;
  starting_range_meters?: number;
  origin_bearing: number;
  // ISO 8601 UTC — when omitted, Rivian uses the request time.
  // Returned times are shifted client-side to match this departure.
  departure_datetime?: string;
  // RFC 3339: when the trip leaves. The server plans for that time (weather, ETAs) and marks a far-off departure
  // far_future. Omit to leave now.
  depart_at?: string;
  waypoints: TripPlanInputWaypoint[];
  target_arrival_soc_percent?: number;
  drive_mode?: string;
  has_adapter?: boolean;
  trailer_profile?: string;
  avoid_adapter_required?: boolean;
  route_preferences?: { avoid_tolls: boolean; avoid_ferries: boolean; avoid_highways: boolean };
  supported_connector_types?: string[];
  network_preferences?: { network_id: string; preference: number }[];
  // pack_kwh enables server-side per-route cost computation. The
  // SPA pulls this from the vehicle profile and forwards it; without
  // it the response omits costs[] and the route table renders
  // without a $ column.
  pack_kwh?: number;
};

export type TripPlanInputWaypoint = {
  latitude: number;
  longitude: number;
  // "origin" | "destination" | "waypoint" — strings the gateway
  // recognises. Kept loose so a future planner extension doesn't
  // break the SPA build.
  waypoint_type: string;
  entity_id?: string;
};

// TripPlan mirrors internal/rivian.TripPlan (Go side). The gateway
// returns one or more candidate routes; the first is typically the
// recommended one.
export type TripPlan = {
  Status: string;
  ChargeStationsAvailable: boolean;
  SoCBelowLimit: boolean;
  Routes: TripRoute[];
  // weather_adjustment is the rivolt-side post-correction. Rivian's
  // planner doesn't know about temperature, wind, or precipitation —
  // this annotation reapplies a per-leg energy multiplier and
  // surfaces "below target SoC" when the corrected arrival is worse
  // than the user's floor. Server-omitted (and thus undefined here)
  // when the weather toggle is off or every leg's fetch failed.
  weather_adjustment?: TripWeatherAdjustment;
  // true when the trip leaves more than 30 days from now: planned from today's network and
  // conditions, with no weather forecast (the server plans such a trip at today's date).
  far_future?: boolean;
  // The IANA zone at the destination ("America/Denver"), for showing an arrival on its own clock with a zone label.
  destination_tz?: string;
  // costs is aligned with Routes - costs[i] is the DCFC + home
  // equivalent estimate for Routes[i]. Omitted when the caller
  // didn't pass pack_kwh on the plan request.
  costs?: TripCostEstimate[];
};

// TripCostEstimate is the deterministic per-route cost projection.
// Shared between the plan response (per-route) and the advice
// response (Routes[0] convenience).
export type TripCostEstimate = {
  currency: string;
  // The fast-charging cost over the stops that have a price, as a range (equal ends when no stop has one). A stop without a
  // price shows "price shown at the charger" and isn't in the sum. A plan saved before this has none of these: it has only
  // the old dcfc_spend and no estimate_basis (read that as an older estimate).
  dcfc_spend_low?: number;
  dcfc_spend_high?: number;
  priced_stops?: number;
  unpriced_stops?: number;
  estimate_basis?: "your_receipts" | "your_setting" | "observed" | "mixed" | "none";
  dcfc_spend?: number;
  home_equivalent: number;
  gas_equivalent: number;
  gas_price_per_gallon: number;
  gas_mpg: number;
  home_rate_used: number;
  breakdown?: TripCostStop[];
};

export type TripWeatherAdjustment = {
  adjusted_arrival_soc: number[];
  final_arrival_soc: number;
  below_target: boolean;
  target_arrival_soc: number;
  legs: TripWeatherAdjLeg[];
  // profile_multiplier is the trip-wide energy multiplier derived
  // from the user's vehicle profile (wheel size, tire type,
  // accessories, default cargo). 1.0 (or absent) means the profile
  // had no effect. profile_reasons is the human-readable list the
  // chip uses to explain what's contributing.
  profile_multiplier?: number;
  profile_reasons?: string[];
};

export type TripWeatherAdjLeg = {
  multiplier: number;
  temp_c?: number;
  headwind_kph?: number;
  precip_mm?: number;
};

export type TripRoute = {
  // Stored route geometry (encoded, precision 6); drawn instead of straight
  // legs when the map has no routing service (the demo).
  route_polyline?: string;
  DestinationReached: boolean;
  TotalChargingDurationSec: number;
  TotalDriveDurationSec: number;
  TotalTripDurationSec: number;
  TotalDriveDistanceMeters: number;
  ArrivalSoC: number;
  ArrivalReachableMeters: number;
  EnergyConsumptionKWh: number;
  BatteryEmptyToDestMeters: number;
  BatteryEmptyLat: number;
  BatteryEmptyLon: number;
  RouteResponseRaw: unknown;
  Waypoints: PlannedWaypoint[];
};

export type PlannedWaypoint = {
  WaypointType: string;
  EntityID: string;
  Name: string;
  Latitude: number;
  Longitude: number;
  MaxPowerKW: number;
  ChargeDurationSec: number;
  ArrivalSoC: number;
  DepartureSoC: number;
  ArrivalReachableMeters: number;
  DepartureReachableMeters: number;
  AdapterRequired: boolean;
  // UTC timestamps from Rivian's planner (computed from request time).
  ArrivalTimeUTC?: string;
  DepartureTimeUTC?: string;
  // Cumulative distance from the route origin (meters). Powers the
  // "180 mi from prev / 360 mi total" columns in the stops table.
  // 0 on legacy responses; we treat 0 as "missing".
  DistanceFromOriginMeters?: number;
};

// TripPlanMultidayRequest is the body for POST /api/trips/plan-multiday: an origin and a list of days;
// day k starts where day k-1 ended, so each day's `end` is an overnight or, for the last day, the trip's
// destination. Vehicle, starting SoC and pack size are filled in by the server when omitted. See
// internal/api/trip_multiday.go and internal/tripmultiday.
export type TripPoint = { lat: number; lon: number; label?: string; entity_id?: string };

export type TripPlanDayRequest = {
  // Stops on the way to this day's end (not overnights).
  vias?: TripPoint[];
  end: TripPoint;
  // RFC 3339; omitted: the next local `overnight.until` after the previous day's arrival (day 1: start_at).
  depart_at?: string;
  target_arrival_soc_pct?: number;
  // The stay that ends this day (ignored on the last day).
  overnight?: {
    // Available AC power in kW; 0 means no charging; omitted means the server's default (7).
    l2_kw?: number;
    // Local time ("15:04") the truck is wanted the next morning.
    until?: string;
    // Cap on the post-charge SoC %; 0 or omitted: the request's max_overnight_soc_pct (default 80).
    max_soc_pct?: number;
    name?: string;
  };
};

export type TripPlanMultidayRequest = {
  vehicle_id?: string;
  starting_soc?: number;
  pack_kwh?: number;
  start_at: string; // RFC 3339
  tz?: string; // IANA zone of the browser, used for "until" and the day boundaries
  drive_mode?: string;
  has_adapter?: boolean;
  route_preferences?: { avoid_tolls: boolean; avoid_ferries: boolean; avoid_highways: boolean };
  target_arrival_soc_percent?: number;
  network_preferences?: { network_id: string; preference: number }[];
  origin: TripPoint;
  days: TripPlanDayRequest[];
  max_overnight_soc_pct?: number;
  // Re-plan from here: the origin and the starting charge are the truck's latest reading.
  origin_from_vehicle?: boolean;
};

// Where the truck was when a re-plan started: the plan's first day begins here.
export type FromPosition = { lat: number; lon: number; soc: number; at: string };

// GET /api/trips/saved/{id}/active: where a saved trip stands now, for "re-plan from here".
export type SavedTripActive = {
  version: number;
  saved_trip_id: string;
  kind: "multiday" | "single";
  day_count: number;
  state: "before" | "active" | "after" | "none";
  current_day: number | null;
  remaining_days: number;
  position: FromPosition | null;
  phase?: "driving" | "overnight";
  next_day?: number;
  next_depart_at?: string;
  projected_soc?: number;
  // The current day's via stops (1-based) the truck has already gone by.
  vias_passed?: number[];
};

export type TripPlanMultidayResponse = {
  days: TripPlanMultidayDay[];
  total: { distance_meters: number; drive_duration_sec: number; charging_duration_sec: number };
  // Any day came from preview data rather than Rivian's planner.
  simulated?: boolean;
  // The trip starts more than 30 days out: see TripPlan.far_future.
  far_future?: boolean;
  // The IANA zone at the final destination ("America/Denver"); each day's own end is in its end_tz.
  destination_tz?: string;
  // The day that could not be planned (1-based); the days before it are complete and chaining stopped there.
  failed_day?: number;
  error?: string;
  error_class?: string;
  error_reason?: string;
  // Set when the plan started from the truck's reading (origin_from_vehicle).
  from_position?: FromPosition;
};

export type OvernightCandidatesRequest = {
  path: [number, number][];
  drive_sec?: number;
  budget_hours?: number;
  budget_miles?: number;
  // Stays to suggest. Omitted: hotels only.
  kinds?: ("hotel" | "campground")[];
};

export type OvernightCandidate = {
  name: string;
  lat: number;
  lon: number;
  entity_id?: string;
  facility_type?: string;
  l2_kw: number;
  // True when the listing gave no power and 7 kW is assumed.
  l2_kw_assumed: boolean;
  // Set when the truck cannot draw the whole l2_kw (a 19.2 kW station plans at the truck's 11.5).
  planned_kw?: number;
  l2_count?: number;
  // The round trip off the route, in metres.
  detour_m: number;
  // How far along the day the stop is, in metres.
  along_m: number;
  dcfc_nearby?: { name: string; distance_m: number; max_power_kw: number } | null;
  // Absent on a hotel from an older server.
  kind?: "hotel" | "campground";
  // Campgrounds only (Recreation.gov, RIDB).
  connector?: "j1772" | "nema_14_50" | "nema_tt_30";
  needs_adapter?: boolean;
  electric_sites?: number;
  sites?: number;
  // null: not known.
  reservation_required?: boolean | null;
  fee?: string;
  source?: string;
  source_url?: string;
  asof?: string;
  // The labelled demo/sample campground.
  sample?: boolean;
};

export type OvernightCandidatesResponse = {
  candidates: OvernightCandidate[];
  target: {
    lat: number;
    lon: number;
    along_m: number;
    reaches_end: boolean;
    // Minutes of driving before and after the budget point that the search covered (the widest tried, when nothing was found). Absent on an older server.
    window_before_min?: number;
    window_after_min?: number;
  };
  route_m: number;
  source: string;
  attribution?: string;
  attribution_source_url?: string;
  attribution_license_url?: string;
  // Set when the day does not fit the budget and `candidates` is empty: the closest listed stay on each side of the budget point.
  nearest?: { before?: OvernightNearestStay; after?: OvernightNearestStay };
  // Set when `candidates` is empty: hotels and motels with NO charger listed (OpenStreetMap), each with the nearest fast charger. Credit
  // `attribution`. The truck does not charge overnight there; the next day starts at `fast_charger`.
  fallback_stays?: OvernightFallbackStay[];
  path_snapped?: boolean;
};

export type OvernightNearestStay = OvernightCandidate & { drive_sec: number; offset_sec: number };

export type OvernightFallbackStay = {
  name: string;
  lat: number;
  lon: number;
  id?: string;
  kind: "hotel";
  locality?: string;
  no_onsite_charger: true;
  fast_charger: { name?: string; lat: number; lon: number; entity_id?: string; distance_m: number; max_power_kw?: number };
  detour_m: number;
  along_m: number;
  source: string;
  attribution: string;
};

export type TripPlanWaypointEcho = { latitude: number; longitude: number; label?: string; entity_id?: string };

export type TripPlanMultidayDay = {
  index: number;
  plan: TripPlan;
  depart_at: string; // RFC 3339
  arrive_at: string;
  start: TripPlanWaypointEcho;
  vias?: TripPlanWaypointEcho[];
  end: TripPlanWaypointEcho;
  // The IANA zone where this day ends (an overnight can be in another zone than the start).
  end_tz?: string;
  departure_soc: number;
  arrival_soc: number;
  destination_reached: boolean;
  soc_below_limit: boolean;
  overnight?: TripPlanOvernightResult;
  weather_adjustment?: TripWeatherAdjustment;
  costs?: TripCostEstimate[];
};

export type TripPlanOvernightResult = {
  name?: string;
  parked_hours: number;
  l2_kw: number;
  // Present only when the truck cannot draw the whole l2_kw; the plan used this.
  planned_kw?: number;
  hookup?: string;
  l2_kw_assumed?: boolean;
  added_kwh: number;
  post_charge_soc_pct: number;
  capped: boolean;
};

// TripAdviceRequest is the body for POST /api/trips/plan/advice.
export type TripAdviceRequest = {
  plan: TripPlan;
  origin?: string;
  destination?: string;
  drive_mode?: string;
  starting_soc?: number;
  has_adapter?: boolean;
  // Tire pressures from live vehicle state, in bar. Omit when unavailable.
  tire_fl_bar?: number;
  tire_fr_bar?: number;
  tire_rl_bar?: number;
  tire_rr_bar?: number;
  // Observed pack capacity in kWh (from vehicles table). Omit when unknown.
  pack_kwh?: number;
  // Door-jamb cold-fill placard PSI from the per-vehicle profile.
  // Omit when unconfigured; backend prompt then frames tire
  // commentary generically instead of citing a guessed number.
  tire_placard_psi?: number;
  // ISO 8601 UTC of planned departure; backend uses it to fetch forecast
  // weather for that hour instead of "now".
  departure_datetime?: string;
};

// TripAdvice is the categorized analysis returned by the advice endpoint.
// The lists may be empty when the model has nothing useful to say
// for that category; the cost_estimate is always computed server-side
// (not by the LLM) so the dollar figures stay accurate.
export type TripAdvice = {
  headline: string;
  cost: string[];
  efficiency: string[];
  weather: string[];
  vehicle: string[];
  cost_estimate: TripCostEstimate;
  model: string;
};

// StopPrice: where one stop's price per kWh comes from. `label` is the exact text to show. For a network with a
// membership price the ends differ, and low_note / high_note say which end is which.
export type StopPrice = {
  basis: "your_receipts" | "your_setting" | "observed";
  per_kwh_low: number;
  per_kwh_high: number;
  low_note?: string;
  high_note?: string;
  sessions?: number;
  source?: string;
  source_url?: string;
  observed_month?: string;
  label: string;
};

// TripCostStop is one DCFC stop's contribution to the spend totals. `price` is null when no price is known: the stop says
// "price shown at the charger" and its cost is null (never $0).
export type TripCostStop = {
  network_slug: string;
  network_name: string;
  charger_name: string;
  energy_kwh: number;
  member_plan?: string;
  price?: StopPrice | null;
  cost_low?: number | null;
  cost_high?: number | null;
};

// SavedTripInputs is the planner form state we persist. Kept loose
// (Record<string, unknown>) at the boundary so the backend doesn't
// have to track every form change; the TripPlanPage casts on the
// way back in.
export type SavedTripInputs = {
  origin: { lat: number; lon: number; label: string };
  destination: { lat: number; lon: number; label: string };
  extra_stops: { lat: number; lon: number; label: string }[];
  target_soc?: string;
  starting_soc?: string;
  drive_mode?: string;
  has_adapter?: boolean;
  route_preferences?: { avoid_tolls: boolean; avoid_ferries: boolean; avoid_highways: boolean };
  departure_at?: string;
  // Multi-day overnight metadata. overnight_flags is a parallel
  // array to extra_stops; any `true` flips the saved trip into the
  // multi-day flow on load. The other fields control the overnight
  // SoC/time/depart limit. All optional — single-day saves omit them.
  overnight_flags?: boolean[];
  max_overnight_soc_pct?: number;
  overnight_limit_mode?: "soc" | "time" | "depart";
  overnight_parked_hours?: number;
  overnight_departure_local?: string;
  // Charger at the overnight stops, in kW (0: none). Older saves omit it (7 kW).
  overnight_l2_kw?: number;
  // Each overnight's own settings, matched to its stop by position.
  overnight_settings?: { lat: number; lon: number; until: string; l2_kw: number; max_soc_pct: number; hookup?: string; campground?: boolean }[];
};

// PlanVsActual is GET /api/trips/saved/{id}/actual
// (plan-vs-actual-contract v1): a saved trip compared with the outing
// that was actually driven. Nulls mean unknown, never 0; an incomplete
// actual cost is null with actual_known/unpriced_sessions instead.
export type PvaBand = "high" | "medium" | "low";

export type PvaCandidate = {
  trip_id: string;
  started_at: string;
  ended_at: string;
  confidence: number;
  band: PvaBand;
  signals: Record<string, number>;
  // v1.1
  miles?: number | null;
  in_progress?: boolean;
};

// start_soc_differed carries soc / plan_soc: the charge the drive left with vs the plan's.
export type PvaReason = { code: string; amount?: number | null; detail?: string; soc?: number; plan_soc?: number };

export type PvaMetric = {
  metric: "cost" | "energy_kwh" | "dcfc_stops" | "arrival_soc" | "duration";
  unit: string;
  planned: number | null;
  actual: number | null;
  delta: number | null;
  actual_complete?: boolean;
  actual_known?: number | null;
  unpriced_sessions?: number;
  actual_sources?: Partial<Record<"rivian" | "network_estimate" | "home_rate" | "user", number>>;
  reasons: PvaReason[];
};

export type PvaStop = {
  match: "same_station" | "same_network" | "other_network" | "nearby" | "skipped" | "extra";
  planned?: { name?: string; network?: string | null; arrival_soc?: number | null; charge_min?: number | null; est_kwh?: number | null; est_cost?: number | null } | null;
  actual?: { kind?: "dcfc" | "ac"; network?: string | null; kwh?: number | null; cost?: number | null; cost_source?: string; started_at?: string; minutes?: number | null } | null;
};

export type PvaComparison = {
  currency: string;
  plan_from: "snapshot" | "current";
  leg: { started_at: string; arrived_at: string; miles?: number | null };
  // v1.1: in_progress until the outing ends (e.g. back home).
  status?: "in_progress" | "complete";
  metrics: PvaMetric[];
  stops: PvaStop[];
};

export type PlanVsActual = {
  version: number;
  saved_trip_id: string;
  status: "linked" | "suggested" | "none";
  link: { trip_id: string; confidence: number; band: PvaBand; linked_at: string; link_stale?: boolean } | null;
  candidates: PvaCandidate[];
  comparison: PvaComparison | null;
};

// A multi-day saved trip answers with one comparison per day, each against that
// day's own slice of the recorded outing.
export type PlanVsActualMulti = {
  version: number;
  saved_trip_id: string;
  kind: "multiday";
  days: (PlanVsActual & { day: number })[];
};

export const isPvaMulti = (d: PlanVsActual | PlanVsActualMulti): d is PlanVsActualMulti =>
  (d as PlanVsActualMulti).kind === "multiday";

// DemoTripPreset: one entry of GET /api/demo/trips. `inputs` has the shape of
// SavedTrip.inputs, so a preset opens like a saved trip.
// ChangelogRelease is one release in GET /api/changelog. body_md is the notes as written (Markdown).
export type ChangelogRelease = {
  tag: string;
  published_at: string;
  title: string;
  body_md: string;
  url: string;
};

export type DemoTripPreset = {
  id: string;
  name: string;
  inputs: SavedTripInputs;
  summary: { distance_mi: number; duration_min: number; charge_stops: number; arrival_soc_pct: number };
  // The seeded saved trip whose Plan vs Actual belongs to this preset.
  saved_trip_id?: string;
  // Encoded route geometry (polyline6, origin to destination) to draw
  // instead of straight legs.
  route_polyline?: string;
};

// A saved multi-day trip keeps the planner's answer as it was: the days, the
// totals and the request that produced them, so reopening needs no planner call.
export type SavedMultidayPlan = {
  kind: "multiday";
  request?: TripPlanMultidayRequest;
  days: TripPlanMultidayDay[];
  total?: TripPlanMultidayResponse["total"];
  simulated?: boolean;
  far_future?: boolean;
};

export const isSavedMultiday = (p: unknown): p is SavedMultidayPlan =>
  !!p && typeof p === "object" && (p as { kind?: unknown }).kind === "multiday" && Array.isArray((p as { days?: unknown }).days);

export type SavedTrip = {
  id: string;
  name: string;
  inputs: SavedTripInputs;
  plan?: TripPlan | SavedMultidayPlan;
  advice?: TripAdvice;
  created_at: string;
  updated_at: string;
};

export type SavedTripBody = {
  name: string;
  inputs: SavedTripInputs;
  plan?: TripPlan | SavedMultidayPlan;
  advice?: TripAdvice;
};
