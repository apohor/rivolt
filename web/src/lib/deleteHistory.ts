// "Delete history before a month": the privacy exit for data a free account's 90-day window hides (POST /api/data/delete-range). It is
// free on every plan. The cutoff is the first of a month (UTC), never later than the current one.

export type MonthChoice = { value: string; label: string };

// This month back through `count` earlier months, newest first, as "2026-07" with "July 2026".
export function cutoffChoices(now: Date = new Date(), count = 36): MonthChoice[] {
  const out: MonthChoice[] = [];
  for (let i = 0; i <= count; i++) {
    const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i, 15));
    out.push({ value: `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`, label: d.toLocaleDateString("en-US", { month: "long", year: "numeric", timeZone: "UTC" }) });
  }
  return out;
}

export function deletedSummary(r: { drives: number; charges: number; samples: number }): string {
  const n = (x: number, one: string, many: string) => `${x.toLocaleString()} ${x === 1 ? one : many}`;
  return `Deleted ${n(r.drives, "drive", "drives")}, ${n(r.charges, "charge", "charges")} and ${n(r.samples, "telemetry sample", "telemetry samples")}.`;
}
