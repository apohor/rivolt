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
  const res = await fetch(url, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
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
    if (
      res.status === 401 &&
      !url.endsWith("/api/auth/me") &&
      !url.startsWith("/api/auth/hydra/login")
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
  model_year?: number;
  make?: string;
  trim_id?: string;
  trim_name?: string;
  pack_kwh?: number;
  image_url?: string;
  images?: VehicleImage[];
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
  cabin_preconditioning_status: string;
  power_state: string;
  alarm_sound_status: string;
  twelve_volt_battery_health: string;
  wiper_fluid_state: string;
  ota_current_version: string;
  ota_available_version: string;
  ota_status: string;
  ota_install_progress: number;
  tire_pressure_fl_bar: number;
  tire_pressure_fr_bar: number;
  tire_pressure_rl_bar: number;
  tire_pressure_rr_bar: number;
  tire_pressure_status_fl: string;
  tire_pressure_status_fr: string;
  tire_pressure_status_rl: string;
  tire_pressure_status_rr: string;
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
  // Pack-side energy consumed, derived from SoC delta × usable pack
  // capacity at the time the drive was persisted. Zero on legacy rows
  // and on imports where --pack-kwh wasn't set.
  EnergyUsedKWh: number;
  Source: string;
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
};

export type Charge = {
  ID: string;
  VehicleID: string;
  StartedAt: string;
  EndedAt: string;
  StartSoCPct: number;
  EndSoCPct: number;
  EnergyAddedKWh: number;
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
};

export type ChargingSettings = {
  home_price_per_kwh: number;
  home_currency: string;
};

// ChargingNetwork is one entry in the user's price book for fast /
// public charging — a friendly name and a default rate the UI can
// one-click apply when manually pricing a session.
export type ChargingNetwork = {
  name: string;
  price_per_kwh: number;
  currency: string;
};

// LiveDrive is the in-flight drive snapshot returned by
// /api/live-drive/:vehicleID while the car is in gear. Mirrors
// internal/rivian.LiveDrive — fields are flat and already in mph /
// miles / kWh so the UI renders without unit conversion.
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
};

export type RivianStatus = {
  enabled: boolean;
  authenticated: boolean;
  mfa_pending: boolean;
  email?: string;
  needs_reauth?: boolean;
  needs_reauth_reason?: string;
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

export const backend = {
  health: () => api.get<Health>("/api/health"),
  // whoami returns the logged-in user, or null when auth is
  // disabled / no session. We squash the 401 here so callers can
  // just await { user_id, username } | null without a try/catch.
  whoami: async (): Promise<AuthUser | null> => {
    try {
      return await api.get<AuthUser>("/api/auth/me");
    } catch (e) {
      if (e instanceof ApiError && (e.status === 401 || e.status === 404)) {
        return null;
      }
      throw e;
    }
  },
  logout: () => api.post<void>("/api/auth/logout"),
  signup: (body: {
    invite_code: string;
    email: string;
    display_name?: string;
    password: string;
  }) => api.post<{ ok: boolean }>("/api/signup", body),
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
  listOwnedVehicles: () =>
    api.get<{ vehicles: OwnedVehicle[] }>("/api/vehicles/owned"),
  vehicleState: (vehicleID: string) =>
    api.get<VehicleState>(`/api/state/${encodeURIComponent(vehicleID)}`),
  liveSession: (vehicleID: string) =>
    api.get<LiveSession>(`/api/live-session/${encodeURIComponent(vehicleID)}`),
  // liveDrive returns undefined when the server replies 204 — no
  // drive session is currently open for the vehicle. Callers should
  // treat undefined the same as "not driving".
  liveDrive: (vehicleID: string) =>
    api.get<LiveDrive | undefined>(
      `/api/live-drive/${encodeURIComponent(vehicleID)}`,
    ),
  rivianStatus: () => api.get<RivianStatus>("/api/settings/rivian/"),
  rivianLogin: (email: string, password: string) =>
    api.post<{ authenticated: boolean; mfa_pending?: boolean; email?: string }>(
      "/api/settings/rivian/login",
      { email, password },
    ),
  rivianMFA: (otp: string) =>
    api.post<{ authenticated: boolean; email?: string }>(
      "/api/settings/rivian/mfa",
      { otp },
    ),
  rivianLogout: () =>
    api.post<{ authenticated: boolean }>("/api/settings/rivian/logout"),
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
  // Price book for fast/public charging networks. GET returns the
  // current list (possibly empty); PUT replaces it wholesale.
  getChargingNetworks: () =>
    api.get<ChargingNetwork[]>("/api/settings/charging/networks"),
  setChargingNetworks: (networks: ChargingNetwork[]) =>
    api.put<ChargingNetwork[]>("/api/settings/charging/networks", networks),
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
  adminDeleteUser: (id: string) =>
    fetch(`/api/admin/users/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }).then(async (res) => {
      const text = await res.text();
      const parsed = text ? JSON.parse(text) : null;
      if (!res.ok) throw new ApiError(res.status, parsed);
      return parsed as { ok: true };
    }),
  adminGenerateInviteCodes: (count = 1) =>
    api.post<{ codes: string[] }>("/api/admin/invite-codes", { count }),
  adminListInviteCodes: () =>
    api.get<{ codes: InviteCode[] }>("/api/admin/invite-codes"),
  // Local DBSCAN clustering of charge locations. Returns one row per
  // cluster, largest-first, with "Home" / "Public" / "Fast" labels.
  chargeClusters: () =>
    api.get<ChargeCluster[]>("/api/charges/clusters"),
  drives: (limit = 50) => api.get<Drive[]>(`/api/drives?limit=${limit}`),
  charges: (limit = 50) => api.get<Charge[]>(`/api/charges?limit=${limit}`),
  // `allDrives` / `allCharges` pull enough history to drive the
  // overview analytics and detail-page lookups without paginating.
  // The store queries cap out at a few hundred rows so this stays cheap.
  allDrives: () => api.get<Drive[]>(`/api/drives?limit=5000`),
  allCharges: () => api.get<Charge[]>(`/api/charges?limit=5000`),
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
  updateChargePricing: (
    id: string,
    body: { cost?: number; currency?: string; price_per_kwh?: number },
  ) =>
    api.patch<void>(`/api/charges/${encodeURIComponent(id)}/pricing`, body),
  samples: (since: Date, limit = 1000) =>
    api.get<Sample[]>(
      `/api/samples?since=${encodeURIComponent(since.toISOString())}&limit=${limit}`,
    ),
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
  vehicleProfileGet: (vehicleID: string) =>
    api.get<VehicleProfile>(
      `/api/vehicles/${encodeURIComponent(vehicleID)}/profile`,
    ),
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
  // Trip-planner default drive mode + Tesla NACS adapter flag.
  // SPA pre-fills the per-trip form from these.
  plannerPrefsGet: () => api.get<PlannerPrefs>("/api/settings/planner"),
  plannerPrefsPut: (p: PlannerPrefs) =>
    api.put<PlannerPrefs>("/api/settings/planner", p),
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
  planTripAdvice: (req: TripAdviceRequest) =>
    api.post<TripAdvice>("/api/trips/plan/advice", req),
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

  // Uploads a JSON bundle previously produced by backupData() and
  // upserts every drive, charge, and sample back. Safe to re-run
  // (drives/charges upsert by external_id; samples dedupe by
  // (vehicle_id, at)). Returns per-table processed counts.
  restoreData: async (file: File) => {
    const res = await fetch("/api/data/restore", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: file,
    });
    const text = await res.text();
    let parsed: unknown = text;
    try {
      parsed = JSON.parse(text);
    } catch {
      // non-JSON error body; keep as text
    }
    if (!res.ok) throw new ApiError(res.status, parsed);
    return parsed as { drives: number; charges: number; samples: number };
  },
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
export type AuthUser = {
  user_id: string;
  username: string;
  role: "user" | "admin";
  onboarding_completed: boolean;
};

// AdminFlagsState is the response shape from GET /api/admin/kill-switch.
// Carries every operational flag — currently the Rivian-upstream kill
// switch and the trip-planner feature flag — with their audit fields.
export type AdminFlagsState = {
  kill_switch: { paused: boolean; reason?: string; actor?: string };
  trip_planner: { enabled: boolean; actor?: string };
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
};

// InviteCode is one row from GET /api/admin/invite-codes.
export type InviteCode = {
  Code: string;
  CreatedAt: string;
  UsedAt?: string | null;
  UsedBy?: string | null;
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

// HomeLocation is the user's saved "home" base. set=false means no
// home is configured — the planner UI should hide the Home preset.
export type HomeLocation = {
  set: boolean;
  latitude: number;
  longitude: number;
  label?: string;
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
  waypoints: TripPlanInputWaypoint[];
  target_arrival_soc_percent?: number;
  drive_mode?: string;
  has_adapter?: boolean;
  trailer_profile?: string;
  avoid_adapter_required?: boolean;
  supported_connector_types?: string[];
  network_preferences?: { network_id: string; preference: number }[];
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
};

export type TripRoute = {
  DestinationReached: boolean;
  TotalChargingDurationSec: number;
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
};

// TripAdvice is the AI-generated analysis returned by the advice endpoint.
export type TripAdvice = {
  headline: string;
  insights: string[];
  model: string;
};
