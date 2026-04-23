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
  if (!res.ok) throw new ApiError(res.status, parsed);
  return parsed as T;
}

export const api = {
  get: <T>(url: string, signal?: AbortSignal) => request<T>("GET", url, undefined, signal),
  post: <T>(url: string, body?: unknown, signal?: AbortSignal) =>
    request<T>("POST", url, body, signal),
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
  Source: string;
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
};

export type ChargingSettings = {
  home_price_per_kwh: number;
  home_currency: string;
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

export const backend = {
  health: () => api.get<Health>("/api/health"),
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
  drives: (limit = 50) => api.get<Drive[]>(`/api/drives?limit=${limit}`),
  charges: (limit = 50) => api.get<Charge[]>(`/api/charges?limit=${limit}`),
  // `allDrives` / `allCharges` pull enough history to drive the
  // overview analytics and detail-page lookups without paginating.
  // The store queries cap out at a few hundred rows so this stays cheap.
  allDrives: () => api.get<Drive[]>(`/api/drives?limit=5000`),
  allCharges: () => api.get<Charge[]>(`/api/charges?limit=5000`),
  samples: (since: Date, limit = 1000) =>
    api.get<Sample[]>(
      `/api/samples?since=${encodeURIComponent(since.toISOString())}&limit=${limit}`,
    ),
  // Multipart upload of one or more ElectraFi CSV files. Returns a per-
  // file result summary (rows/samples/drives/charges ingested).
  importElectrafi: async (files: File[], packKWh?: number) => {
    const fd = new FormData();
    for (const f of files) fd.append("file", f, f.name);
    if (packKWh && packKWh > 0) fd.append("pack_kwh", String(packKWh));
    const res = await fetch("/api/import/electrafi", { method: "POST", body: fd });
    const text = await res.text();
    let parsed: unknown = undefined;
    if (text) {
      try {
        parsed = JSON.parse(text);
      } catch {
        parsed = text;
      }
    }
    if (!res.ok) throw new ApiError(res.status, parsed);
    return parsed as { files: ImportResult[] };
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
