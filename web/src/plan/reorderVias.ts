// Reordering the stops a person added: a via moves past the next via in that direction (overnights keep their own list and place). Pure, so the
// list and its overnight flags stay paired.
export function movableVia(flags: boolean[], i: number, dir: -1 | 1): number {
  for (let j = i + dir; j >= 0 && j < flags.length; j += dir) if (!flags[j]) return j;
  return -1;
}

export function moveVia<T>(stops: T[], flags: boolean[], i: number, dir: -1 | 1): { stops: T[]; flags: boolean[] } {
  const j = movableVia(flags, i, dir);
  if (flags[i] || j < 0 || i >= stops.length) return { stops, flags };
  const s = stops.slice();
  const f = flags.slice();
  [s[i], s[j]] = [s[j], s[i]];
  [f[i], f[j]] = [f[j], f[i]];
  return { stops: s, flags: f };
}
