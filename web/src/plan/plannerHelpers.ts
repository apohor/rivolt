import { type SavedTripInputs } from "../lib/api";

// Selection is what the form actually plans against. lat/lon are
// resolved (search result, preset, or current vehicle position);
// label is human-readable so the form can render "From: Home" and
// not bother the user with raw coordinates.
export type Selection = {
  lat: number;
  lon: number;
  label: string;
} | null;

// LAST_TRIP_KEY versions the localStorage shape. Bump on breaking
// changes to the persisted struct so a stale entry from an older
// build can't crash the page on rehydrate.
export const LAST_TRIP_KEY = "rivolt:trip-planner:last-trip:v1";

// HOME_LABEL_RADIUS_M is how close a selected point must be to the
// saved home location before the planner relabels it. 150 m covers
// driveway-vs-street-address geocode jitter without false-positiving
// onto a neighbor's lot.
export const HOME_LABEL_RADIUS_M = 150;

export function haversineMeters(
  a: { lat: number; lon: number },
  b: { lat: number; lon: number },
): number {
  const R = 6371000;
  const toRad = (x: number) => (x * Math.PI) / 180;
  const dLat = toRad(b.lat - a.lat);
  const dLon = toRad(b.lon - a.lon);
  const sLat = Math.sin(dLat / 2);
  const sLon = Math.sin(dLon / 2);
  const h =
    sLat * sLat +
    Math.cos(toRad(a.lat)) * Math.cos(toRad(b.lat)) * sLon * sLon;
  return 2 * R * Math.asin(Math.sqrt(h));
}

export type HomePoint = { latitude: number; longitude: number; label?: string };

export function relabelIfHome<T extends { lat: number; lon: number; label: string }>(
  sel: T,
  home: HomePoint | null,
): T {
  if (!home) return sel;
  const d = haversineMeters(
    { lat: sel.lat, lon: sel.lon },
    { lat: home.latitude, lon: home.longitude },
  );
  if (d > HOME_LABEL_RADIUS_M) return sel;
  return { ...sel, label: home.label || "Home" };
}

export type LastTrip = {
  origin: NonNullable<Selection>;
  destination: NonNullable<Selection>;
  extraStops: NonNullable<Selection>[];
  targetSoc: string;
  driveMode: DriveMode;
  hasAdapter: boolean;
};

export function readLastTrip(): LastTrip | null {
  try {
    const raw = localStorage.getItem(LAST_TRIP_KEY);
    if (!raw) {
      console.debug("[trip-planner] no saved trip in localStorage");
      return null;
    }
    const parsed = JSON.parse(raw) as LastTrip;
    console.debug("[trip-planner] hydrated last trip:", parsed);
    return parsed;
  } catch (e) {
    console.warn("[trip-planner] readLastTrip failed:", e);
    return null;
  }
}

export function writeLastTrip(t: LastTrip): void {
  try {
    localStorage.setItem(LAST_TRIP_KEY, JSON.stringify(t));
    console.debug("[trip-planner] saved last trip:", t);
  } catch (e) {
    console.warn("[trip-planner] writeLastTrip failed:", e);
  }
}

// collectInputs folds the current form state into the SavedTripInputs
// shape the backend persists. Centralized so save + future "what
// changed since the snapshot?" comparisons stay in sync.
export function collectInputs(s: {
  origin: NonNullable<Selection>;
  destination: NonNullable<Selection>;
  extraStops: NonNullable<Selection>[];
  overnightFlags: boolean[];
  maxOvernightSoCPct: number;
  overnightL2Kw: number;
  overnightDepartureLocal: string;
  targetSoc: string;
  startingSoc: string;
  driveMode: DriveMode;
  hasAdapter: boolean;
  departureAt: string;
}): SavedTripInputs {
  const hasOvernight = s.overnightFlags.some(Boolean);
  return {
    origin: s.origin,
    destination: s.destination,
    extra_stops: s.extraStops,
    target_soc: s.targetSoc,
    starting_soc: s.startingSoc,
    drive_mode: s.driveMode,
    has_adapter: s.hasAdapter,
    departure_at: s.departureAt,
    // Only persist multi-day fields when actually used — single-day
    // saves stay byte-compatible with the pre-multi-day schema.
    ...(hasOvernight
      ? {
          overnight_flags: s.overnightFlags,
          max_overnight_soc_pct: s.maxOvernightSoCPct,
          overnight_l2_kw: s.overnightL2Kw,
          overnight_departure_local: s.overnightDepartureLocal,
        }
      : {}),
  };
}

// DriveMode is restricted to the two values the dropdown surfaces.
// Older saved-trip / planner-prefs payloads may still carry empty
// or now-retired values (SPORT/WINTER/OFF_ROAD_AUTO); the
// normalizeDriveMode helper below coerces them to EVERYDAY so the
// controlled select always matches an option and Rivian gets a
// valid enum.
export type DriveMode = "EVERYDAY" | "DISTANCE";

export function normalizeDriveMode(v: unknown): DriveMode {
  return v === "DISTANCE" ? "DISTANCE" : "EVERYDAY";
}
