// Durations for the idle-awake chart: minutes under an hour, hours plus
// minutes above ("45 min", "2 h 10 min", "3 h"). Values are never capped.

function split(minutes: number): { h: number; m: number } {
  const total = Math.max(0, Math.round(minutes));
  return { h: Math.floor(total / 60), m: total % 60 };
}

export function formatMinutes(minutes: number): string {
  if (!Number.isFinite(minutes)) return "—";
  const { h, m } = split(minutes);
  if (h === 0) return `${m} min`;
  return m === 0 ? `${h} h` : `${h} h ${m} min`;
}

// Compact variant for axis ticks ("45m", "2h 10m"), where room is tight.
export function formatMinutesShort(minutes: number): string {
  if (!Number.isFinite(minutes)) return "—";
  const { h, m } = split(minutes);
  if (h === 0) return `${m}m`;
  return m === 0 ? `${h}h` : `${h}h ${m}m`;
}
