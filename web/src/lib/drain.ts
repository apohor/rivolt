// Pure helpers for the Truck tab's parked-drain card (drain-contract
// v1). The card states what happened and how much data it rests on; it
// never guesses a rate and never names a cause.

import type { DrainDay, DrainWindow } from "./api";

// What `sufficient` requires server-side, so the copy can say how far
// along a vehicle is.
export const NEEDED_PARKED_HOURS = { "7d": 24, "30d": 72 } as const;

export function formatPct(v: number): string {
  return v < 10 ? v.toFixed(1) : v.toFixed(0);
}

export function formatHours(h: number): string {
  if (h < 1) return `${Math.round(h * 60)} min`;
  return `${h < 10 ? h.toFixed(1) : Math.round(h)} h`;
}

export type DrainView =
  | { kind: "insufficient"; haveHours: number; needHours: number }
  | {
      kind: "ok";
      pctPerDay: number;
      kwhPerDay: number | null;
      idleAwakeHours: number | null;
      sleeps: number;
      quickRewakes: number;
      medianSleepSeconds: number | null;
      daysUsed: number;
      daysExcluded: number;
    };

// view turns a window into what the card may say. Insufficient data
// (or a missing rate) never yields a number.
export function view(w: DrainWindow, key: "7d" | "30d"): DrainView {
  if (!w.sufficient || w.drain_pct_per_day == null) {
    return { kind: "insufficient", haveHours: w.parked_hours, needHours: NEEDED_PARKED_HOURS[key] };
  }
  const ex = w.days_excluded;
  return {
    kind: "ok",
    pctPerDay: w.drain_pct_per_day,
    kwhPerDay: w.drain_kwh_per_day,
    idleAwakeHours: w.idle_awake_hours_per_day,
    sleeps: w.sleeps,
    quickRewakes: w.quick_rewakes,
    medianSleepSeconds: w.median_sleep_seconds,
    daysUsed: w.days_with_data,
    daysExcluded: ex.charging + ex.driving + ex.insufficient,
  };
}

export type Bar = { day: string; value: number | null; excluded: DrainDay["excluded"]; h: number };

// bars scales the daily series to 0..1 against its own max; excluded or
// unmeasured days keep their slot with no value (drawn hollow).
export function bars(daily: DrainDay[]): { bars: Bar[]; max: number } {
  const vals = daily.map((d) => (d.excluded == null && d.drain_pct != null ? Math.max(0, d.drain_pct) : null));
  const max = Math.max(0.5, ...vals.map((v) => v ?? 0));
  return {
    max,
    bars: daily.map((d, i) => ({
      day: d.day,
      value: vals[i],
      excluded: d.excluded,
      h: vals[i] == null ? 0 : vals[i]! / max,
    })),
  };
}

export const EXCLUDED_LABEL: Record<NonNullable<DrainDay["excluded"]>, string> = {
  charging: "charging most of the day",
  driving: "mostly driving",
  insufficient: "not enough data",
};
