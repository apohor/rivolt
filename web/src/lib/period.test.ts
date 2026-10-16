import { describe, expect, it } from "vitest";
import { barsEndDay, filterByRange, periodLabel, periodRange, rangeDays, stepPeriod, toYmd } from "./period";

// Wed 2026-10-14 12:00 local.
const NOW = new Date(2026, 9, 14, 12, 0, 0);
const ymd = (d: Date | null) => (d ? toYmd(d) : null);

describe("periodRange", () => {
  it("week is Monday to Sunday, stepping back a week at a time", () => {
    const r = periodRange({ kind: "week", offset: 0 }, NOW);
    expect([ymd(r.start), ymd(r.end)]).toEqual(["2026-10-12", "2026-10-19"]);
    const prev = periodRange({ kind: "week", offset: -1 }, NOW);
    expect([ymd(prev.start), ymd(prev.end)]).toEqual(["2026-10-05", "2026-10-12"]);
  });
  it("a Sunday belongs to the week that started the Monday before", () => {
    const sun = new Date(2026, 9, 18, 9, 0, 0);
    expect(ymd(periodRange({ kind: "week", offset: 0 }, sun).start)).toBe("2026-10-12");
  });
  it("month and year are calendar-aligned, and step across year boundaries", () => {
    const m = periodRange({ kind: "month", offset: 0 }, NOW);
    expect([ymd(m.start), ymd(m.end)]).toEqual(["2026-10-01", "2026-11-01"]);
    const jan = periodRange({ kind: "month", offset: -10 }, NOW);
    expect([ymd(jan.start), ymd(jan.end)]).toEqual(["2025-12-01", "2026-01-01"]);
    const y = periodRange({ kind: "year", offset: -1 }, NOW);
    expect([ymd(y.start), ymd(y.end)]).toEqual(["2025-01-01", "2026-01-01"]);
  });
  it("all is open; custom is inclusive of both dates and tolerates swapped ends", () => {
    expect(periodRange({ kind: "all", offset: 0 }, NOW)).toEqual({ start: null, end: null });
    const c = periodRange({ kind: "custom", offset: 0, from: "2026-10-03", to: "2026-10-17" }, NOW);
    expect([ymd(c.start), ymd(c.end)]).toEqual(["2026-10-03", "2026-10-18"]);
    const s = periodRange({ kind: "custom", offset: 0, from: "2026-10-17", to: "2026-10-03" }, NOW);
    expect([ymd(s.start), ymd(s.end)]).toEqual(["2026-10-03", "2026-10-18"]);
    expect(periodRange({ kind: "custom", offset: 0 }, NOW)).toEqual({ start: null, end: null });
  });
});

describe("labels, stepping, filtering", () => {
  it("labels read like Money's navigator", () => {
    expect(periodLabel({ kind: "month", offset: 0 }, NOW)).toBe("Oct 2026");
    expect(periodLabel({ kind: "week", offset: 0 }, NOW)).toBe("Oct 12–18");
    expect(periodLabel({ kind: "week", offset: -2 }, NOW)).toBe("Sep 28–Oct 4");
    expect(periodLabel({ kind: "year", offset: 0 }, NOW)).toBe("2026");
    expect(periodLabel({ kind: "all", offset: 0 }, NOW)).toBe("All time");
    expect(periodLabel({ kind: "custom", offset: 0, from: "2026-10-03", to: "2026-10-17" }, NOW)).toBe("Oct 3 – Oct 17, 2026");
  });
  it("never steps into the future and only steps calendar periods", () => {
    expect(stepPeriod({ kind: "month", offset: 0 }, 1).offset).toBe(0);
    expect(stepPeriod({ kind: "month", offset: 0 }, -1).offset).toBe(-1);
    expect(stepPeriod({ kind: "all", offset: 0 }, -1).offset).toBe(0);
  });
  it("filters by the half-open range and counts days", () => {
    const items = [
      { StartedAt: new Date(2026, 9, 11, 23, 59).toISOString() },
      { StartedAt: new Date(2026, 9, 12, 0, 0).toISOString() },
      { StartedAt: new Date(2026, 9, 18, 23, 59).toISOString() },
      { StartedAt: new Date(2026, 9, 19, 0, 0).toISOString() },
    ];
    const r = periodRange({ kind: "week", offset: 0 }, NOW);
    expect(filterByRange(items, r)).toHaveLength(2);
    expect(rangeDays(r)).toBe(7);
    expect(filterByRange(items, { start: null, end: null })).toHaveLength(4);
  });
  it("bars end today for the current period and on the last day for past ones", () => {
    expect(ymd(barsEndDay(periodRange({ kind: "week", offset: 0 }, NOW), NOW))).toBe("2026-10-14");
    expect(ymd(barsEndDay(periodRange({ kind: "week", offset: -1 }, NOW), NOW))).toBe("2026-10-11");
  });
});
