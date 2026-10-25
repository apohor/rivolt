// Client-side display preferences persisted in localStorage. Purely
// presentational — the backend always stores and serves SI units
// (Celsius, kilometers, bar, etc.); these toggles just decide how we
// render them. A future iteration can sync this to the server if we
// want it to follow the user across devices.

import { useSyncExternalStore } from "react";

export type TemperatureUnit = "c" | "f";
export type DistanceUnit = "mi" | "km";

// TimeZone preference: "auto" defers to the browser (Intl resolved
// zone); otherwise an IANA identifier (e.g. "America/Chicago", "UTC").
export type TimeZonePref = "auto" | string;

type Preferences = {
  temperatureUnit: TemperatureUnit;
  // False until the user picks a temperature unit: then it follows the distance unit (miles gives °F, km gives °C).
  // Preferences saved before this existed count as picked, so nobody's choice changes under them.
  temperatureUnitSet: boolean;
  distanceUnit: DistanceUnit;
  timeZone: TimeZonePref;
  // Round-trip collapsing: merge consecutive A→B and B→A drive rows
  // when B is within `roundTripRadiusMeters` of A's start *and* the
  // park-gap between them is under `roundTripMaxGapMinutes`.
  roundTripsEnabled: boolean;
  roundTripRadiusMeters: number;
  roundTripMaxGapMinutes: number;
};

const STORAGE_KEY = "rivolt.preferences.v1";

// The temperature unit a user who has not picked one gets.
export const defaultTemperatureUnit = (d: DistanceUnit): TemperatureUnit => (d === "km" ? "c" : "f");

const DEFAULT_PREFERENCES: Preferences = {
  temperatureUnit: "f",
  temperatureUnitSet: false,
  distanceUnit: "mi",
  timeZone: "auto",
  roundTripsEnabled: true,
  roundTripRadiusMeters: 200,
  roundTripMaxGapMinutes: 90,
};

function readPreferences(): Preferences {
  if (typeof window === "undefined") return DEFAULT_PREFERENCES;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_PREFERENCES;
    const parsed = JSON.parse(raw) as Partial<Preferences>;
    const distanceUnit: DistanceUnit = parsed.distanceUnit === "km" ? "km" : "mi";
    const stored = parsed.temperatureUnit === "f" || parsed.temperatureUnit === "c" ? parsed.temperatureUnit : null;
    const set = parsed.temperatureUnitSet === true || (parsed.temperatureUnitSet === undefined && stored !== null);
    return {
      ...DEFAULT_PREFERENCES,
      ...parsed,
      temperatureUnitSet: set,
      temperatureUnit: set && stored ? stored : defaultTemperatureUnit(distanceUnit),
      distanceUnit,
      timeZone:
        typeof parsed.timeZone === "string" && parsed.timeZone.length > 0
          ? parsed.timeZone
          : DEFAULT_PREFERENCES.timeZone,
      roundTripsEnabled:
        typeof parsed.roundTripsEnabled === "boolean"
          ? parsed.roundTripsEnabled
          : DEFAULT_PREFERENCES.roundTripsEnabled,
      roundTripRadiusMeters:
        typeof parsed.roundTripRadiusMeters === "number" &&
        Number.isFinite(parsed.roundTripRadiusMeters) &&
        parsed.roundTripRadiusMeters > 0
          ? parsed.roundTripRadiusMeters
          : DEFAULT_PREFERENCES.roundTripRadiusMeters,
      roundTripMaxGapMinutes:
        typeof parsed.roundTripMaxGapMinutes === "number" &&
        Number.isFinite(parsed.roundTripMaxGapMinutes) &&
        parsed.roundTripMaxGapMinutes > 0
          ? parsed.roundTripMaxGapMinutes
          : DEFAULT_PREFERENCES.roundTripMaxGapMinutes,
    };
  } catch {
    return DEFAULT_PREFERENCES;
  }
}

// Single in-memory copy so every subscribed component sees the same
// object reference until we write. This is important for
// useSyncExternalStore — it compares by identity.
let current: Preferences = readPreferences();
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// Cross-tab sync: react to localStorage changes from other windows.
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key !== STORAGE_KEY) return;
    current = readPreferences();
    emit();
  });
}

// A signed-in account keeps the units too (accountUnits.ts): it hears about every pick made here, and applies the account's
// units back with applyAccountUnits, which does not tell it again.
type UnitPick = { distance_unit?: DistanceUnit; temperature_unit?: TemperatureUnit };
const pickListeners = new Set<(p: UnitPick) => void>();
export function onUnitPick(fn: (p: UnitPick) => void): () => void {
  pickListeners.add(fn);
  return () => {
    pickListeners.delete(fn);
  };
}

function store() {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(current));
  } catch {
    // quota exceeded / disabled — silently ignore, in-memory value
    // still applies for this tab.
  }
}

export function setTemperatureUnit(unit: TemperatureUnit): void {
  if (current.temperatureUnit === unit && current.temperatureUnitSet) return;
  current = { ...current, temperatureUnit: unit, temperatureUnitSet: true };
  store();
  emit();
  for (const l of pickListeners) l({ temperature_unit: unit });
}

export function setDistanceUnit(unit: DistanceUnit): void {
  if (current.distanceUnit === unit) return;
  current = { ...current, distanceUnit: unit, temperatureUnit: current.temperatureUnitSet ? current.temperatureUnit : defaultTemperatureUnit(unit) };
  store();
  emit();
  for (const l of pickListeners) l({ distance_unit: unit });
}

// The account's units become this browser's, without a pick being reported back: the temperature is the account's own choice
// ("user") or follows the distance.
export function applyAccountUnits(distance: DistanceUnit, temperature: TemperatureUnit, temperatureSet: boolean): void {
  const next = { ...current, distanceUnit: distance, temperatureUnit: temperatureSet ? temperature : defaultTemperatureUnit(distance), temperatureUnitSet: temperatureSet };
  if (next.distanceUnit === current.distanceUnit && next.temperatureUnit === current.temperatureUnit && next.temperatureUnitSet === current.temperatureUnitSet) return;
  current = next;
  store();
  emit();
}

// Whether this browser has a saved choice (the preferences were written at some point), as opposed to the defaults.
export function hasLocalUnitPick(): boolean {
  try {
    return window.localStorage.getItem(STORAGE_KEY) !== null;
  } catch {
    return false;
  }
}

export function localUnits(): { distanceUnit: DistanceUnit; temperatureUnit: TemperatureUnit; temperatureUnitSet: boolean } {
  return { distanceUnit: current.distanceUnit, temperatureUnit: current.temperatureUnit, temperatureUnitSet: current.temperatureUnitSet };
}

export function setTimeZone(tz: TimeZonePref): void {
  if (current.timeZone === tz) return;
  current = { ...current, timeZone: tz };
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(current));
  } catch {
    // see setTemperatureUnit
  }
  emit();
}

export function setRoundTripsEnabled(enabled: boolean): void {
  if (current.roundTripsEnabled === enabled) return;
  current = { ...current, roundTripsEnabled: enabled };
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(current));
  } catch {
    // see setTemperatureUnit
  }
  emit();
}

export function setRoundTripRadiusMeters(m: number): void {
  if (!Number.isFinite(m) || m <= 0) return;
  if (current.roundTripRadiusMeters === m) return;
  current = { ...current, roundTripRadiusMeters: m };
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(current));
  } catch {
    // see setTemperatureUnit
  }
  emit();
}

export function setRoundTripMaxGapMinutes(mins: number): void {
  if (!Number.isFinite(mins) || mins <= 0) return;
  if (current.roundTripMaxGapMinutes === mins) return;
  current = { ...current, roundTripMaxGapMinutes: mins };
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(current));
  } catch {
    // see setTemperatureUnit
  }
  emit();
}

// resolvedTimeZone returns undefined for "auto" (letting Intl pick the
// browser's local zone) or the stored IANA identifier otherwise.
// Callers pass the return value directly into
// `Intl.DateTimeFormat`'s `timeZone` option.
export function resolvedTimeZone(pref: TimeZonePref): string | undefined {
  if (pref === "auto") return undefined;
  return pref;
}

export function usePreferences(): Preferences {
  return useSyncExternalStore(
    subscribe,
    () => current,
    () => DEFAULT_PREFERENCES,
  );
}


// formatTemperature renders a Celsius value in the user's chosen
// unit. Returns a string like "21 °C" or "70 °F". The backend always
// serves Celsius so this is the only place unit conversion happens
// in the UI.
export function formatTemperature(
  celsius: number | null | undefined,
  unit: TemperatureUnit,
  digits = 0,
): string {
  if (celsius === null || celsius === undefined || Number.isNaN(celsius)) {
    return "—";
  }
  if (unit === "f") {
    const f = celsius * 1.8 + 32;
    return `${f.toFixed(digits)} °F`;
  }
  return `${celsius.toFixed(digits)} °C`;
}

export const MI_PER_KM = 0.6213711922;

// formatDistance renders a distance the backend serves in km in the
// user's chosen unit, rounded so it never reveals a precise position:
// whole units, rounded to the nearest 10 km / 5 mi from 100 km / 60 mi
// up. `fine` keeps one decimal under 10 for the owner's own live view.
export function formatDistance(
  km: number | null | undefined,
  unit: DistanceUnit,
  opts: { fine?: boolean } = {},
): string {
  if (km === null || km === undefined || Number.isNaN(km)) return "—";
  const v = unit === "mi" ? km * MI_PER_KM : km;
  const step = unit === "mi" ? 5 : 10;
  const big = unit === "mi" ? 60 : 100;
  let n: string;
  if (v >= big) n = String(Math.round(v / step) * step);
  else if (opts.fine && v < 10) n = v.toFixed(1);
  else n = String(Math.round(v));
  return `${n} ${unit}`;
}
