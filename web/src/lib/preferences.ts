// Client-side display preferences persisted in localStorage. Purely
// presentational — the backend always stores and serves SI units
// (Celsius, kilometers, bar, etc.); these toggles just decide how we
// render them. A future iteration can sync this to the server if we
// want it to follow the user across devices.

import { useSyncExternalStore } from "react";

export type TemperatureUnit = "c" | "f";

// TimeZone preference: "auto" defers to the browser (Intl resolved
// zone); otherwise an IANA identifier (e.g. "America/Chicago", "UTC").
export type TimeZonePref = "auto" | string;

type Preferences = {
  temperatureUnit: TemperatureUnit;
  timeZone: TimeZonePref;
};

const STORAGE_KEY = "rivolt.preferences.v1";

const DEFAULT_PREFERENCES: Preferences = {
  temperatureUnit: "c",
  timeZone: "auto",
};

function readPreferences(): Preferences {
  if (typeof window === "undefined") return DEFAULT_PREFERENCES;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_PREFERENCES;
    const parsed = JSON.parse(raw) as Partial<Preferences>;
    return {
      ...DEFAULT_PREFERENCES,
      ...parsed,
      temperatureUnit:
        parsed.temperatureUnit === "f" || parsed.temperatureUnit === "c"
          ? parsed.temperatureUnit
          : DEFAULT_PREFERENCES.temperatureUnit,
      timeZone:
        typeof parsed.timeZone === "string" && parsed.timeZone.length > 0
          ? parsed.timeZone
          : DEFAULT_PREFERENCES.timeZone,
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

export function setTemperatureUnit(unit: TemperatureUnit): void {
  if (current.temperatureUnit === unit) return;
  current = { ...current, temperatureUnit: unit };
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(current));
  } catch {
    // quota exceeded / disabled — silently ignore, in-memory value
    // still applies for this tab.
  }
  emit();
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
