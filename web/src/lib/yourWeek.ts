// "Your week" on Now: this week's miles, energy and charging cost, and how the miles compare with last week at the same point of the week.
// The same drive and charge lists Now already loads (no new request); the week is Monday to Sunday in the browser's zone, as the Week scope.
import type { Charge, Drive } from "./api";
import { chargeStats, driveStats } from "./analytics";
import { filterByRange, periodRange } from "./period";

export type WeekFigures = { miles: number; kWh: number; cost: number; currency: string; drives: number; charges: number; costEstimated: boolean };

// Cost sources the user or Rivian stated; any other source (or an estimated cost with no stated one) is an estimate.
const STATED = new Set(["rivian", "user"]);

export function weekFigures(drives: Drive[], charges: Charge[], start: Date, end: Date): WeekFigures {
  const range = { start, end };
  const ds = driveStats(filterByRange(drives, range));
  const cs = chargeStats(filterByRange(charges, range));
  const priced = filterByRange(charges, range).filter((c) => (c.Cost || 0) > 0 || (c.estimated_cost || 0) > 0);
  const costEstimated = priced.some((c) => (c.Cost || 0) <= 0 || !STATED.has(c.cost_source_effective ?? ""));
  return { miles: ds.miles, kWh: cs.energyKWh, cost: cs.cost, currency: cs.currency || "USD", drives: ds.count, charges: cs.count, costEstimated };
}

export type YourWeek = {
  week: WeekFigures;
  // Miles against last week up to the same moment of the week, as a whole percent; null unless last week gives something to compare with.
  milesChangePct: number | null;
};

// Last week needs at least this many miles before a percentage is worth saying (an empty or near-empty week gives no "up 400%").
export const MIN_LAST_WEEK_MILES = 5;

// Null when nothing was recorded this week (no strip).
export function yourWeek(drives: Drive[], charges: Charge[], now: Date = new Date()): YourWeek | null {
  const cur = periodRange({ kind: "week", offset: 0 }, now);
  if (!cur.start || !cur.end) return null;
  const week = weekFigures(drives, charges, cur.start, cur.end);
  if (week.drives === 0 && week.charges === 0) return null;
  // The same stretch of last week: Monday to this moment, seven days earlier (so Monday's one drive is not set against a whole week).
  const elapsed = Math.min(now.getTime(), cur.end.getTime()) - cur.start.getTime();
  const lastStart = new Date(cur.start.getFullYear(), cur.start.getMonth(), cur.start.getDate() - 7);
  const lastEnd = new Date(lastStart.getTime() + elapsed);
  const last = weekFigures(drives, charges, lastStart, lastEnd);
  const milesChangePct = last.miles >= MIN_LAST_WEEK_MILES && week.miles > 0 ? Math.round(((week.miles - last.miles) / last.miles) * 100) : null;
  return { week, milesChangePct };
}

// The change as words: "▲ 12% miles vs last week so far", "about the same miles as last week so far".
export function changeLine(pct: number | null): string | null {
  if (pct == null) return null;
  if (pct === 0) return "About the same miles as last week so far";
  return `${pct > 0 ? "▲" : "▼"} ${Math.abs(pct)}% miles vs last week so far`;
}
