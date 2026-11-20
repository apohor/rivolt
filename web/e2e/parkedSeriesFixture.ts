import { e2eNow } from "./clock";

// GET /api/vehicles/{id}/parked-series as the server would answer it for the Now card's parked graph. Times are hours before "now".
export type ParkedCase = "overnight" | "asleep" | "never_slept" | "minutes" | "gone";

const H = 3_600_000;

type Sleep = [number, number]; // hours ago, from > to

const SLEEPS: Record<string, Sleep[]> = {
  overnight: [[22, 19], [18, 12], [11.4, 7], [6.4, 3.2], [2.6, 1.2]],
  asleep: [[22, 19], [18, 12], [11.4, 7], [6.4, 3.2], [2.6, 0]],
  never_slept: [],
};

export function parkedSeriesFixture(kind: ParkedCase) {
  const now = e2eNow();
  const at = (h: number) => new Date(now - h * H).toISOString();
  const base = { version: 1, from: at(24), to: at(0) };
  if (kind === "minutes") {
    return { ...base, parked_since: at(0.3), parked: true, change_pct: null, points: [], sleeps: [], wakes: [], figures: null, projection: null };
  }
  const sleeps = SLEEPS[kind];
  // The battery: a slow loss while asleep, a little more at each wake-up, and a faster one awake and idle.
  const rate = (h: number) => (sleeps.some(([a, b]) => h <= a && h >= b) ? 0.012 : 0.07); // percent per 10 minutes, scaled below
  const driveAt = 14; // a short drive 14 h ago starts the current parked run
  const points: { t: string; soc: number; seg: number }[] = [];
  let soc = kind === "never_slept" ? 78 : 80;
  for (let h = 24; h >= 0; h -= 1 / 6) {
    if (h > driveAt - 0.3 && h < driveAt + 0.01 && h > driveAt - 0.4) {
      soc -= 0.6; // the drive's own loss; no points during it
      continue;
    }
    soc -= rate(h) * (kind === "never_slept" ? 2 : 1);
    points.push({ t: at(h), soc: Math.round(soc * 10) / 10, seg: h > driveAt ? 0 : 1 });
  }
  const wakes = sleeps.slice(0, kind === "asleep" ? -1 : undefined).map(([, b]) => {
    const near = points.reduce((best, p) => (Math.abs(Date.parse(p.t) - (now - b * H)) < Math.abs(Date.parse(best.t) - (now - b * H)) ? p : best));
    return { t: at(b), soc_before: near.soc, soc_after: near.soc };
  });
  const last = points[points.length - 1];
  // Local midnight is taken as 12 h ago: the sleeps inside the last 12 h.
  const asleepToday = sleeps.reduce((sum, [a, b]) => sum + Math.max(0, Math.min(a, 12) - Math.min(b, 12)), 0);
  const idle = kind === "never_slept" ? 12 : 6;
  return {
    ...base,
    parked_since: at(driveAt - 0.1),
    parked: true,
    change_pct: Math.round((last.soc - points.find((p) => p.seg === 1)!.soc) * 10) / 10,
    points,
    sleeps: sleeps.map(([a, b]) => ({ from: at(a), to: at(b), open: b === 0 })),
    wakes,
    figures: { asleep_today_s: Math.round(asleepToday * 3600), wake_ups: wakes.length, wake_cost_pct: wakes.length ? 0.4 : null, idle_pct_per_day: idle, idle_mi_per_day: idle * 3 },
    projection: { at: new Date(now + 2 * 24 * H).toISOString(), soc: Math.round((last.soc - idle * 2) * 10) / 10 },
  };
}
