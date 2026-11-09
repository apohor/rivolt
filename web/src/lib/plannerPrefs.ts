// The Trip planner settings, as the map and the settings page read them: one cached query, and a save that always sends every field.
import { useEffect } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { tilesRadar } from "./config";
import { backend, PLANNER_DEFAULTS, type LayerFilters, type PlannerPrefs } from "./api";

export const PLANNER_PREFS_KEY = ["settings", "plannerPrefs"] as const;

// plannerWithDefaults fills any field the server left out (an older server, or the signed-out demo), so no caller needs a default of its own.
export function plannerWithDefaults(p: PlannerPrefs | undefined): Required<Omit<PlannerPrefs, "has_adapter">> & Pick<PlannerPrefs, "has_adapter"> {
  return {
    drive_mode: p?.drive_mode ?? "",
    has_adapter: p?.has_adapter,
    corridor: p?.corridor ?? PLANNER_DEFAULTS.corridor,
    start_exclude_mi: p?.start_exclude_mi ?? PLANNER_DEFAULTS.start_exclude_mi,
    min_fast_kw: p?.min_fast_kw ?? PLANNER_DEFAULTS.min_fast_kw,
    layers_on: p?.layers_on ?? [...PLANNER_DEFAULTS.layers_on],
    preferred_only: p?.preferred_only ?? PLANNER_DEFAULTS.preferred_only,
    weather_on: p?.weather_on ?? PLANNER_DEFAULTS.weather_on,
    radar_on: p?.radar_on ?? PLANNER_DEFAULTS.radar_on,
  };
}

// The weather switch used to be remembered per device ("on"/"off"). It is the account's setting now; the device's choice is copied into it once.
export const DEVICE_WEATHER_KEY = "rivolt.weatherLayer";

function deviceWeather(): boolean | null {
  try {
    const v = localStorage.getItem(DEVICE_WEATHER_KEY);
    return v === "on" ? true : v === "off" ? false : null;
  } catch {
    return null;
  }
}

// usePlannerPrefs is the user's trip planner settings with defaults filled in. `settled` is true once the answer is in or has failed: a map waits
// for it so it does not draw once with the defaults and again with the user's choices (and falls back to the defaults if the call fails).
// A weather choice still stored on this device wins until it has been copied into the account setting (once, then the device copy is removed), so
// nobody who turned weather off gets it back.
export function usePlannerPrefs() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: PLANNER_PREFS_KEY, queryFn: backend.plannerPrefsGet, staleTime: 60 * 1000 });
  const device = deviceWeather();
  useEffect(() => {
    if (!q.isSuccess || device === null) return;
    let live = true;
    const done = () => { try { localStorage.removeItem(DEVICE_WEATHER_KEY); } catch { /* nothing to remove */ } };
    if (device === (q.data.weather_on ?? PLANNER_DEFAULTS.weather_on)) { done(); return; }
    void backend.plannerPrefsPut({ ...plannerWithDefaults(q.data), weather_on: device })
      .then((saved) => { if (live) qc.setQueryData(PLANNER_PREFS_KEY, saved); done(); })
      .catch(() => { /* keep the device copy; the next load tries again */ });
    return () => { live = false; };
  }, [q.isSuccess, q.data, device, qc]);
  const prefs = plannerWithDefaults(q.data);
  if (q.isSuccess && device !== null) prefs.weather_on = device;
  return { prefs, settled: q.isSuccess || q.isError };
}

// layerFiltersOf is the corridor-layers request fields for a user's settings.
export function layerFiltersOf(p: Pick<PlannerPrefs, "corridor" | "start_exclude_mi" | "min_fast_kw">): LayerFilters {
  return { preset: p.corridor, start_exclude_mi: p.start_exclude_mi, min_fast_kw: p.min_fast_kw };
}

// useSavePlannerPrefs saves a change to some of the settings: the PUT carries every field (the current ones with the change on top), so
// nothing it does not mention is ever reset, whatever else is stale in the cache.
export function useSavePlannerPrefs() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: Partial<PlannerPrefs>) => {
      const cur = qc.getQueryData<PlannerPrefs>(PLANNER_PREFS_KEY);
      return backend.plannerPrefsPut({ ...plannerWithDefaults(cur), ...patch });
    },
    // The change shows at once and a second change right behind it builds on it, not on the cache from before the first.
    onMutate: (patch) => {
      qc.setQueryData(PLANNER_PREFS_KEY, { ...plannerWithDefaults(qc.getQueryData<PlannerPrefs>(PLANNER_PREFS_KEY)), ...patch });
    },
    onSuccess: (data) => qc.setQueryData(PLANNER_PREFS_KEY, data),
    onError: () => qc.invalidateQueries({ queryKey: PLANNER_PREFS_KEY }),
  });
}

// radarAvailable: the server has the live radar on (RIVOLT_RADAR), so a radar control makes sense.
export function radarAvailable(): boolean {
  return tilesRadar().url !== "";
}
