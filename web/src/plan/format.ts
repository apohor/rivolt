// formatWaypointTime formats a UTC ISO string to local HH:MM, applying
// an optional millisecond shift (user departure offset vs Rivian's "now").
// `round` rounds to the nearest minute (the new look): a 3:37:40 arrival then reads 3:38 PM,
// matching "10:00 AM + 5 h 38 min" instead of truncating the seconds.
export function formatWaypointTime(utcStr: string | undefined, shiftMs: number, round = false): string {
  if (!utcStr) return "";
  let ms = new Date(utcStr).getTime() + shiftMs;
  if (round) ms = Math.round(ms / 60_000) * 60_000;
  const d = new Date(ms);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// formatDuration turns a seconds count into "Hh Mm" / "Mm" suitable
// for the route summary header.
export function formatDuration(seconds: number): string {
  const m = Math.round(seconds / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  const rem = m % 60;
  if (rem === 0) return `${h}h`;
  return `${h}h ${rem}m`;
}

