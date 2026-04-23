// Shared formatters and small presentational helpers.

// Timestamps in this app come from the ElectraFi CSV export, which has
// no timezone info — we store them as UTC by convention. Displaying in
// the browser's local zone introduces a phantom offset ("07:21" in the
// export shows up as "02:21 AM" for a Central-time user). Render in
// UTC so the UI matches the source.
const DISPLAY_TZ = "UTC";

// Format an RFC3339 string as a short date-time in the display zone.
export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZone: DISPLAY_TZ,
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
