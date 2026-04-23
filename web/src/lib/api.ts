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

export type Vehicle = { id: string; vin: string; name: string; model: string };

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
  charger_state: string;
  charger_power_kw: number;
  charge_target_pct: number;
  latitude: number;
  longitude: number;
  locked: boolean;
  cabin_temp_c: number;
  outside_temp_c: number;
  power_state: string;
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
