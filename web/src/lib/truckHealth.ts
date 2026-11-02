// Pure helpers for the Truck tab (ux-report S4). Everything a source
// doesn't provide returns null so the page hides it (NQ15): no "—"
// placeholders for tires, versions or 12V the vehicle never reports.

import type { DayActivity, SleepHealth, VehicleState } from "./api";

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

export type TirePos = "fl" | "fr" | "rl" | "rr";
export type Wheel = { pos: TirePos; label: string; bar: number | null; psi: number | null; low: boolean; rivianLow: boolean };

// The words Rivian's per-wheel status uses for a low tire, after trimming, lower-casing and turning spaces and hyphens into "_".
// Any other word (including "normal") makes no claim: the card shows the pressure only and never calls a wheel OK in our words.
const RIVIAN_LOW_WORDS = new Set(["warning_soft", "warning_hard", "warning_puncture"]);
export const rivianSaysLow = (status: string | undefined | null): boolean =>
  RIVIAN_LOW_WORDS.has((status ?? "").trim().toLowerCase().replace(/[\s-]+/g, "_"));

type TireFields = Pick<
  VehicleState,
  | "tire_pressure_fl_bar" | "tire_pressure_fr_bar" | "tire_pressure_rl_bar" | "tire_pressure_rr_bar"
  | "tire_pressure_status_fl" | "tire_pressure_status_fr" | "tire_pressure_status_rl" | "tire_pressure_status_rr"
>;

// The four wheels, front row first as on the truck. A wheel that sent no pressure (0) has no number and is never marked low on the
// floor; it can still carry Rivian's own word. Null when no wheel has anything to show.
export function tireWheels(s: TireFields): Wheel[] | null {
  const rows: [TirePos, string, number, string][] = [
    ["fl", "Front left", s.tire_pressure_fl_bar, s.tire_pressure_status_fl],
    ["fr", "Front right", s.tire_pressure_fr_bar, s.tire_pressure_status_fr],
    ["rl", "Rear left", s.tire_pressure_rl_bar, s.tire_pressure_status_rl],
    ["rr", "Rear right", s.tire_pressure_rr_bar, s.tire_pressure_status_rr],
  ];
  const wheels = rows.map(([pos, label, v, status]): Wheel => {
    const bar = Number.isFinite(v) && v > 0 ? v : null;
    return { pos, label, bar, psi: bar == null ? null : bar * PSI_PER_BAR, low: bar != null && bar < LOW_TIRE_BAR, rivianLow: rivianSaysLow(status) };
  });
  return wheels.some((w) => w.bar != null) ? wheels : null;
}

// Software update state from the live feed's otaStatus and otaInstallProgress. A fixed list of the truck's own words, after trimming,
// lower-casing and turning spaces and hyphens into "_": idle, empty or any other word means no update state and no claim. An R2 has none.
export type OtaPhase = "downloading" | "ready" | "installing";
const OTA_WORDS: Record<string, OtaPhase> = {
  downloading: "downloading",
  download: "downloading",
  download_in_progress: "downloading",
  ready_to_install: "ready",
  ready_for_install: "ready",
  installing: "installing",
  install: "installing",
  install_in_progress: "installing",
};
// The scale of otaInstallProgress: its maximum value (100 for percent, 1 for a fraction). Null until confirmed from real frames, and while
// it is null no number is shown, only the state: never "4000%" or "0.4%".
export const OTA_PROGRESS_MAX: number | null = null;

export type OtaUpdate = { phase: OtaPhase; version: string | null; pct: number | null };

export function otaUpdate(s: Pick<VehicleState, "ota_status" | "ota_install_progress" | "ota_current_version" | "ota_available_version">): OtaUpdate | null {
  const phase = OTA_WORDS[(s.ota_status ?? "").trim().toLowerCase().replace(/[\s-]+/g, "_")];
  if (!phase) return null;
  let pct: number | null = null;
  const p = s.ota_install_progress;
  if (OTA_PROGRESS_MAX != null && phase !== "ready" && Number.isFinite(p) && p > 0 && p <= OTA_PROGRESS_MAX) pct = Math.round((p / OTA_PROGRESS_MAX) * 100);
  return { phase, version: softwareInfo(s)?.available ?? null, pct };
}

// "2026.38 · Downloading 40%", "Ready to install", "Installing software 2026.38 · 40%" (Now). The truck's state in plain words, never
// an instruction or an estimate.
export function otaLine(u: OtaUpdate, where: "truck" | "now" = "truck"): string {
  const num = u.pct != null ? ` ${u.pct}%` : "";
  if (where === "now") return `Installing software${u.version ? ` ${u.version}` : ""}${u.pct != null ? ` · ${u.pct}%` : ""}`;
  const word = u.phase === "downloading" ? `Downloading${num}` : u.phase === "ready" ? "Ready to install" : `Installing${num}`;
  return u.version ? `${u.version} · ${word}` : word;
}

export type SoftwareInfo = { current: string; available: string | null };

export function softwareInfo(s: Pick<VehicleState, "ota_current_version" | "ota_available_version">): SoftwareInfo | null {
  const cur = (s.ota_current_version || "").trim();
  if (!cur) return null;
  const avail = (s.ota_available_version || "").trim();
  // The feed reports "0.0.0" (or zeros) when no update is pending.
  const none = avail === "" || /^0+(\.0+)*$/.test(avail);
  return { current: cur, available: !none && avail !== cur ? avail : null };
}

export type SleepSummary = {
  bars: { label: string; value: number; x: number }[];
  daysWithData: number;
  // Failed sleeps across the last two days, when it's worth saying so.
  trouble: { failed: number; sleeps: number } | null;
  // Average over completed days only; 0 when there are none.
  avgIdleMinutes: number;
  avgDays: number;
};

export function sleepSummary(days: DayActivity[], troubleThreshold = 3, now: Date = new Date()): SleepSummary {
  const bars = days.map((d) => ({ label: d.day.slice(0, 10), value: d.idle_awake_h * 60, x: new Date(d.day).getTime() }));
  const recent = days.slice(-2);
  const failed = recent.reduce((n, d) => n + (d.failed_sleeps ?? 0), 0);
  const sleeps = recent.reduce((n, d) => n + (d.sleeps ?? 0), 0);
  // Today is partial and would drag the average down, so the average
  // covers completed days only (UTC day starts, matching the endpoint).
  const todayStart = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
  const done = bars.filter((b) => b.x < todayStart);
  const avg = done.length ? done.reduce((a, b) => a + b.value, 0) / done.length : 0;
  return {
    bars,
    daysWithData: bars.length,
    trouble: failed >= troubleThreshold ? { failed, sleeps } : null,
    avgIdleMinutes: avg,
    avgDays: done.length,
  };
}

// Sleep health in words: the verdict, what a short sleep is with this week's numbers, and what it is compared with. Neutral on purpose:
// it never says why the truck woke (Rivolt's own requests, a phone key nearby or a fault can each do it) and never says the truck is fine.
export type SleepHealthLines = { headline: string; detail: string | null; basis: string };
export function sleepHealthLines(h: SleepHealth): SleepHealthLines {
  const basis = "Compared with this truck's own last 30 days.";
  if (h.state === "not_enough_data") return { headline: "Not enough history yet to say how this week compares.", detail: null, basis };
  const n = h.this_week.failed_sleeps;
  const usual = h.usual_failed_sleeps ?? 0;
  const usualText = usual < 0.5 ? "almost none in a usual week" : `about ${Math.round(usual)} in a usual week`;
  const detail = `${n} short ${n === 1 ? "sleep" : "sleeps"} (under a minute) this week; ${usualText}.`;
  if (h.state === "more") {
    const by = h.more_by ?? 0;
    return { headline: `Woke ${by} more ${by === 1 ? "time" : "times"} than usual this week.`, detail, basis };
  }
  return { headline: "Nothing unusual this week.", detail, basis };
}
