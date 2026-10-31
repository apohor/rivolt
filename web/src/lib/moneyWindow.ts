// What a free account's Money shows (backend #1370): this month and the two before it. The statement for an older month answers 402
// outside_free_window, and the month's CSV 402 pro_required, so the page never offers a download it knows will be refused.

import type { PlanState } from "./plan";

export const FREE_MONTHS = 3;

// Whole calendar months from `month` ("2026-07") up to `now` ("2026-10"): 3 for July when it is October. Negative never happens for a
// real month, and 0 is the current month.
export function monthsBack(month: string, now: string): number {
  const a = /^(\d{4})-(\d{2})$/.exec(month);
  const b = /^(\d{4})-(\d{2})$/.exec(now);
  if (!a || !b) return 0;
  return (Number(b[1]) - Number(a[1])) * 12 + (Number(b[2]) - Number(a[2]));
}

// True when this account's plan shows only the last FREE_MONTHS months and the month is older than that. Pro, billing off and the demo
// ("off") see everything.
export function csvIsPro(plan: PlanState, month: string, now: string): boolean {
  if (plan.kind !== "free" && plan.kind !== "lapsed") return false;
  return monthsBack(month, now) >= FREE_MONTHS;
}
