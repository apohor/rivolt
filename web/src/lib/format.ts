// Shared formatters and small presentational helpers.

import { resolvedTimeZone } from "./preferences";

// Timestamp rendering zone. Defaults to the browser's local zone via
// `resolvedTimeZone("auto") === undefined`; users can pick an explicit
// IANA identifier in Settings → Display → Time zone. Reading the
// current preference at call time (not module init) is important so
// updates take effect without a reload.
function currentTZ(): string | undefined {
  if (typeof window === "undefined") return undefined;
  try {
    const raw = window.localStorage.getItem("rivolt.preferences.v1");
    if (!raw) return undefined;
    const parsed = JSON.parse(raw) as { timeZone?: string };
    if (!parsed.timeZone || parsed.timeZone === "auto") return undefined;
    return resolvedTimeZone(parsed.timeZone);
  } catch {
    return undefined;
  }
}

// Format an RFC3339 string as a short date-time in the display zone.
export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZone: currentTZ(),
  });
}

// Format a duration in seconds as "1h 23m" / "5m".
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "—";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export function durationSeconds(startIso: string, endIso: string): number {
  return (new Date(endIso).getTime() - new Date(startIso).getTime()) / 1000;
}

// Fixed-precision with a fallback dash for missing/zero values.
export function num(v: number, digits = 1, unit = ""): string {
  if (!Number.isFinite(v) || v === 0) return "—";
  return `${v.toFixed(digits)}${unit ? " " + unit : ""}`;
}

// Pct shows 0..100 with a percent sign and falls back to dash for 0.
export function pct(v: number, digits = 0): string {
  if (!Number.isFinite(v) || v === 0) return "—";
  return `${v.toFixed(digits)}%`;
}

// Humanise the raw chargingState value stored on Charge.FinalState.
// Values come from ElectraFi / the Tesla API: "Complete",
// "Disconnected", "Stopped", "Starting", "NoPower", "Charging",
// "charging_station_err".
//
// "Charging" as a *final* state is not informative — it just means the
// last snapshot ElectraFi wrote for this chargeNumber was still in the
// Charging state (no terminal transition captured before the session
// boundary). The session itself ended regardless (we have EndedAt), so
// we collapse it to the em-dash.
export function formatChargeState(s: string): string {
  if (!s) return "—";
  switch (s) {
    case "Complete":
      return "Complete";
    case "Charging":
      return "—";
    case "Disconnected":
      return "Disconnected";
    case "Stopped":
      return "Stopped";
    case "Starting":
      return "Starting";
    case "NoPower":
      return "No power";
    case "charging_station_err":
      return "Interrupted";
    default:
      // Fallback: turn snake_case into Sentence case.
      return s
        .replace(/_/g, " ")
        .replace(/\s+/g, " ")
        .trim()
        .replace(/^./, (c) => c.toUpperCase());
  }
}

// True when the charge session is still in progress. The backend
// keeps `EndedAt` updated to the last-seen sample timestamp on open
// live sessions, so callers must NOT treat EndedAt as the real end
// for these — show "in progress" instead. A session is active when
// FinalState is a non-terminal `charging_*` value (the same rule
// the Go store uses to find LatestOpenLive).
export function isActiveCharge(c: { FinalState: string; Source?: string }): boolean {
  const s = c.FinalState;
  if (!s) return false;
  if (!s.startsWith("charging_")) return false;
  return s !== "charging_complete" && s !== "charging_station_err";
}

// maxFixAgeSeconds returns the worst (largest) gap between a sample's
// wall-clock timestamp and the GNSS fix timestamp it carries. Picking
// the max rather than the mean is intentional: a single 50-minute
// frozen-fix span across a 60-minute window tells the user the marker
// is untrustworthy, even when most samples carried fresh fixes.
// Returns null when no sample carries a fix timestamp (legacy rows,
// imports) so the caller can render nothing instead of a misleading
// "0s stale" badge.
//
// Bogus timestamps from older Go zero-time serialization
// ("0001-01-01T00:00:00Z") are filtered out: any fix that pre-dates
// 2010 cannot be a real Rivian GNSS reading (the company was founded
// in 2009, its first vehicles shipped in 2021), and a 2000-year fix
// age is unambiguously a serialization artifact rather than a stuck
// modem.
export function maxFixAgeSeconds(
  samples: { At: string; LocationFixAt?: string }[],
): number | null {
  const MIN_PLAUSIBLE_MS = Date.UTC(2010, 0, 1);
  let worst: number | null = null;
  for (const p of samples) {
    if (!p.LocationFixAt) continue;
    const fixMs = new Date(p.LocationFixAt).getTime();
    if (!Number.isFinite(fixMs) || fixMs < MIN_PLAUSIBLE_MS) continue;
    const ageMs = new Date(p.At).getTime() - fixMs;
    if (!Number.isFinite(ageMs) || ageMs < 0) continue;
    const ageS = ageMs / 1000;
    if (worst == null || ageS > worst) worst = ageS;
  }
  return worst;
}
