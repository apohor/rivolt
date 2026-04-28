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
    super(message ?? `HTTP ${status}`);
    this.status = status;
    this.body = body;
  }
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
    if (res.status === 401 && !url.endsWith("/api/auth/me")) {
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
  SpeedMph: number;
  ShiftState: string;
  ChargingState: string;
  ChargerPowerKW: number;
  ChargeLimitPct: number;
  InsideTempC: number;
  OutsideTempC: number;
  DriveNumber: number;
  ChargeNumber: number;
  Source: string;
};

export type RivianStatus = {
  enabled: boolean;
  authenticated: boolean;
  mfa_pending: boolean;
  email?: string;
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
  login: (username: string, password: string) =>
    api.post<AuthUser>("/api/auth/login", { username, password }),
  logout: () => api.post<void>("/api/auth/logout"),
  // oidcProviders returns the list of OIDC sign-in options the
  // server has wired up. An empty array (or a 404 when an old
  // server is in front of a new SPA) means: just render the
  // password form, no social-login row.
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
  // AI provider configuration. GET returns the redacted view; PUT takes
  // a partial patch (nil = leave alone, "" = clear).
  getAISettings: () => api.get<AISettings>("/api/settings/ai"),
  updateAISettings: (patch: AISettingsUpdate) =>
    api.put<AISettings>("/api/settings/ai", patch),
  // Fetch the provider's own model catalogue via its list endpoint,
  // proxied server-side so the API key never hits the browser.
  listAIModels: (provider: AIProvider) =>
    api.get<{ models: string[] }>(
      `/api/settings/ai/models/${encodeURIComponent(provider)}`,
    ),
  // Smoke-test the currently configured AI provider. Sends a trivial
  // prompt and returns the reply + token usage + round-trip latency,
  // so the Settings UI can confirm key/model validity without waiting
  // for a downstream feature to exercise the integration.
  pingAI: () => api.post<AIPingResult>("/api/ai/ping", {}),
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
  // Multipart upload of one or more ElectraFi CSV files. Returns a per-
  // file result summary (rows/samples/drives/charges ingested).
  // onProgress, when provided, is called for each server-emitted NDJSON
  // event (file_start / progress / file_done) so the UI can render a
  // live "row N of file.csv" status during long imports.
  importElectrafi: async (
    files: File[],
    packKWh?: number,
    tz?: string,
    onProgress?: (p: ImportProgress) => void,
  ) => {
    const fd = new FormData();
    for (const f of files) fd.append("file", f, f.name);
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

// AuthUser is whatever /api/auth/me returns — today a user_id plus
// the display username. When OIDC lands we'll add email/name; the
// contract stays backward-compatible.
export type AuthUser = {
  user_id: string;
  username: string;
};

// OIDCProvider is one entry in /api/auth/oidc/. The SPA renders
// one button per entry; clicking sends the browser to start_url
// where the server kicks off the auth-code flow.
export type OIDCProvider = {
  name: string;
  display_name: string;
  start_url: string;
};
