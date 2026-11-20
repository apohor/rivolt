import { e2eNow } from "./clock";

// GET /api/vehicles/{id}/parked-series as the server would answer it for the Now card's parked graph. Times are hours before "now".
export type ParkedCase = "overnight" | "asleep" | "never_slept" | "charged" | "flat" | "just" | "minutes" | "gone";

const H = 3_600_000;

type Sleep = [number, number]; // hours ago, from > to

// A sleep never overlaps the drive or the charge that breaks the line: the sleep before it ends where it starts.
const SLEEPS: Record<string, Sleep[]> = {
  overnight: [[22, 19], [18, 11.9], [11.4, 7], [6.4, 3.2], [2.6, 1.2]],
  asleep: [[22, 19], [18, 11.9], [11.4, 7], [6.4, 3.2], [2.6, 0]],
  never_slept: [],
  charged: [[22, 19], [18, 12], [11.4, 7.1], [6.4, 3.2], [2.6, 1.2]],
  // Asleep through the parked stretch with no loss at all, as a truck that sleeps well reads (the sleeps end at the parked marker's wake).
  flat: [[22, 19], [18, 11.9], [11.4, 7], [6.4, 3.2], [2.6, 0]],
  just: [[22, 19], [18, 11.9], [11.4, 7], [6.4, 3.2], [2.6, 0.6]],
};

// What breaks the line: [start, end] in hours ago and the battery change over it (no readings inside). A drive costs a little; a charge adds a lot.
const BREAK: Record<string, [number, number, number]> = {
  overnight: [11.9, 11.7, -0.6],
  asleep: [11.9, 11.7, -0.6],
  never_slept: [11.9, 11.7, -0.6],
  charged: [7.1, 6.7, 18],
  flat: [11.9, 11.7, -0.6],
  // Parked a few minutes ago after a short drive: the whole window is "before the parked marker".
  just: [0.5, 0.2, -5],
};

export function parkedSeriesFixture(kind: ParkedCase) {
  const now = e2eNow();
  const at = (h: number) => new Date(now - h * H).toISOString();
  const base = { version: 1, from: at(24), to: at(0) };
  if (kind === "minutes") {
    return { ...base, parked_since: at(0.3), parked: true, change_pct: null, points: [], sleeps: [], wakes: [], figures: null, projection: null };
  }
  const sleeps = SLEEPS[kind];
  const [brkFrom, brkTo, brkSoc] = BREAK[kind];
  // The battery: a slow loss while asleep, a little more at each wake-up, and a faster one awake and idle.
  const rate = (h: number) => (kind === "flat" && h < brkTo ? 0 : sleeps.some(([a, b]) => h <= a && h >= b) ? 0.012 : 0.07); // percent per 10 minutes
  const points: { t: string; soc: number; seg: number }[] = [];
  let soc = kind === "never_slept" ? 78 : 80;
  let broke = false;
  for (let h = 24; h >= 0; h -= 1 / 6) {
    if (h < brkFrom && h > brkTo) {
      if (!broke) {
        soc += brkSoc;
        broke = true;
      }
      continue;
    }
    soc -= rate(h) * (kind === "never_slept" ? 2 : 1);
    points.push({ t: at(h), soc: Math.round(soc * 10) / 10, seg: h >= brkFrom ? 0 : 1 });
  }
  // A sleep that ends at the drive or the charge is not a wake-up; an open one has not ended.
  const wakes = sleeps.filter(([, b]) => b !== 0 && b !== brkFrom).map(([, b]) => {
    const near = points.reduce((best, p) => (Math.abs(Date.parse(p.t) - (now - b * H)) < Math.abs(Date.parse(best.t) - (now - b * H)) ? p : best));
    return { t: at(b), soc_before: near.soc, soc_after: near.soc };
  });
  const last = points[points.length - 1];
  // Local midnight is taken as 12 h ago: the sleeps inside the last 12 h.
  const asleepToday = sleeps.reduce((sum, [a, b]) => sum + Math.max(0, Math.min(a, 12) - Math.min(b, 12)), 0);
  const idle = kind === "never_slept" ? 12 : 6;
  const flat = kind === "flat";
  return {
    ...base,
    parked_since: points.find((p) => p.seg === 1)!.t,
    parked: true,
    change_pct: Math.round((last.soc - points.find((p) => p.seg === 1)!.soc) * 10) / 10,
    points,
    sleeps: sleeps.map(([a, b]) => ({ from: at(a), to: at(b), open: b === 0 })),
    wakes,
    figures: { asleep_today_s: Math.round(asleepToday * 3600), wake_ups: wakes.length, wake_cost_pct: wakes.length ? (flat ? 0 : 0.4) : null, idle_pct_per_day: flat ? null : idle, idle_mi_per_day: flat ? null : idle * 3 },
    projection: flat ? null : { at: new Date(now + 2 * 24 * H).toISOString(), soc: Math.round((last.soc - idle * 2) * 10) / 10 },
  };
}
