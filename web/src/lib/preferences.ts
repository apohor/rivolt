// Client-side display preferences persisted in localStorage. Purely
// presentational — the backend always stores and serves SI units
// (Celsius, kilometers, bar, etc.); these toggles just decide how we
// render them. A future iteration can sync this to the server if we
// want it to follow the user across devices.

import { useSyncExternalStore } from "react";

export type TemperatureUnit = "c" | "f";

type Preferences = {
  temperatureUnit: TemperatureUnit;
};

const STORAGE_KEY = "rivolt.preferences.v1";

const DEFAULT_PREFERENCES: Preferences = {
  temperatureUnit: "c",
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
