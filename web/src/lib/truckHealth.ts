// Pure helpers for the Truck tab (ux-report S4). Everything a source
// doesn't provide returns null so the page hides it (NQ15): no "—"
// placeholders for tires, versions or 12V the vehicle never reports.

import type { DayActivity, VehicleState } from "./api";

const PSI_PER_BAR = 14.5038;
export const LOW_TIRE_BAR = 2.2;

export type TireReading = { bar: number; psi: number; low: boolean };

// Lowest of the wheels that actually reported a pressure. The replay
// feed only carries the min of four, so no wheel position is claimed.
export function lowestTire(s: Pick<VehicleState, "tire_pressure_fl_bar" | "tire_pressure_fr_bar" | "tire_pressure_rl_bar" | "tire_pressure_rr_bar">): TireReading | null {
  const vals = [s.tire_pressure_fl_bar, s.tire_pressure_fr_bar, s.tire_pressure_rl_bar, s.tire_pressure_rr_bar].filter(
    (v) => Number.isFinite(v) && v > 0,
  );
  if (vals.length === 0) return null;
  const bar = Math.min(...vals);
  return { bar, psi: bar * PSI_PER_BAR, low: bar < LOW_TIRE_BAR };
}

export type SoftwareInfo = { current: string; available: string | null };

export function softwareInfo(s: Pick<VehicleState, "ota_current_version" | "ota_available_version">): SoftwareInfo | null {
  const cur = (s.ota_current_version || "").trim();
  if (!cur) return null;
  const avail = (s.ota_available_version || "").trim();
  return { current: cur, available: avail && avail !== cur ? avail : null };
}

export type SleepSummary = {
  bars: { label: string; value: number; x: number }[];
  daysWithData: number;
  // Failed sleeps across the last two days, when it's worth saying so.
  trouble: { failed: number; sleeps: number } | null;
  avgIdleMinutes: number;
};

export function sleepSummary(days: DayActivity[], troubleThreshold = 3): SleepSummary {
  const bars = days.map((d) => ({ label: d.day.slice(0, 10), value: d.idle_awake_h * 60, x: new Date(d.day).getTime() }));
  const recent = days.slice(-2);
  const failed = recent.reduce((n, d) => n + (d.failed_sleeps ?? 0), 0);
  const sleeps = recent.reduce((n, d) => n + (d.sleeps ?? 0), 0);
  const avg = bars.length ? bars.reduce((a, b) => a + b.value, 0) / bars.length : 0;
  return {
    bars,
    daysWithData: bars.length,
    trouble: failed >= troubleThreshold ? { failed, sleeps } : null,
    avgIdleMinutes: avg,
  };
}
