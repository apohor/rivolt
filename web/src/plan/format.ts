// formatWaypointTime formats a UTC ISO string to local HH:MM, applying
// an optional millisecond shift (user departure offset vs Rivian's "now").
export function formatWaypointTime(utcStr: string | undefined, shiftMs: number): string {
  if (!utcStr) return "";
  const d = new Date(new Date(utcStr).getTime() + shiftMs);
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

