// Reordering the stops a person added: a via moves past the next via in that direction (overnights keep their own list and place). Pure, so the
// list and its overnight flags stay paired.
export function movableVia(flags: boolean[], i: number, dir: -1 | 1): number {
  for (let j = i + dir; j >= 0 && j < flags.length; j += dir) if (!flags[j]) return j;
  return -1;
}

// Which stop a Via row is, counting only the person's own stops (overnights have their own list): for the buttons' accessible names, since every pin
// the person drops is labelled "A place you chose" and the names would otherwise be identical.
export function viaPosition(flags: boolean[], i: number): { n: number; total: number } {
  let n = 0;
  let total = 0;
  flags.forEach((overnight, j) => {
    if (overnight) return;
    total++;
    if (j <= i) n++;
  });
  return { n, total };
}

export function viaMoveLabel(label: string, n: number, total: number, dir: -1 | 1): string {
  return `Move stop ${n} of ${total}, ${label}, ${dir < 0 ? "earlier" : "later"}`;
}

// Starts with the visible word ("remove") so the name contains what is on the button.
export function viaRemoveLabel(label: string, n: number): string {
  return `remove stop ${n}: ${label}`;
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
