import { describe, expect, it } from "vitest";
import { dailyRanges, dayTicks, extremesOf, limitValue, monotoneAt, pctLabel, spokenBand } from "./SocTrendChart";

const at = (day: number, hour: number) => new Date(2026, 9, day, hour, 0, 0).getTime();
const pt = (day: number, hour: number, y: number) => ({ x: at(day, hour), y });

describe("SocTrendChart axis and labels", () => {
  it("labels a month on the 1st, 11th, 21st and its last day, through its end, as Miles per day", () => {
    const t = dayTicks(new Date(2026, 9, 1).getTime(), new Date(2026, 10, 1).getTime(), 5);
    expect(t.map((d) => d.label)).toEqual(["10-01", "10-11", "10-21", "10-31"]);
    expect(t[3].x).toBe(new Date(2026, 9, 31).getTime());
    expect(dayTicks(new Date(2026, 8, 1).getTime(), new Date(2026, 9, 1).getTime(), 5).map((d) => d.label)).toEqual(["09-01", "09-11", "09-21", "09-30"]);
  });

  it("labels each day of a week and spaces a year evenly", () => {
    expect(dayTicks(new Date(2026, 9, 19).getTime(), new Date(2026, 9, 26).getTime(), 5)).toHaveLength(7);
    expect(dayTicks(new Date(2026, 0, 1).getTime(), new Date(2027, 0, 1).getTime(), 4)).toHaveLength(4);
  });

  it("rounds the high and low half away from zero", () => {
    expect(pctLabel(2.5)).toBe("3%");
    expect(pctLabel(2.4)).toBe("2%");
    expect(pctLabel(99.6)).toBe("100%");
  });
});

describe("the battery by day", () => {
  // The owner's Oct 12: 58 → 22 → 73 → 3 → 73 → 17 → 42 → 12 → 70, on the same local day.
  const trip = [pt(12, 5, 58), pt(12, 7, 22), pt(12, 8, 73), pt(12, 13, 3), pt(12, 14, 73), pt(12, 18, 17), pt(12, 21, 42), pt(12, 22, 12), pt(12, 23, 70)];

  it("is one entry per local day: lowest, highest and where it ended", () => {
    const [d] = dailyRanges(trip, []);
    expect(dailyRanges(trip, [])).toHaveLength(1);
    expect([d.low, d.high, d.end]).toEqual([3, 73, 70]);
  });

  it("skips days with no readings, ignores 0 as unknown and flags the days a charge touched", () => {
    const rows = dailyRanges(
      [pt(2, 13, 71), pt(2, 14, 69), pt(2, 22, 60), pt(3, 2, 70), pt(5, 13, 66), pt(5, 14, 0)],
      [{ x0: at(2, 22), x1: at(3, 2) }],
    );
    expect(rows.map((r) => new Date(r.day).getDate())).toEqual([2, 3, 5]);
    expect(rows.map((r) => r.charged)).toEqual([true, true, false]);
    expect(rows[0].end).toBe(60);
    expect(rows[2].low).toBe(66);
  });

  it("names the period's highest and lowest, and says so in a sentence", () => {
    const rows = dailyRanges([pt(9, 7, 97), pt(9, 14, 41), pt(12, 9, 73), pt(12, 13, 3), pt(15, 9, 70), pt(15, 10, 66)], []);
    const e = extremesOf(rows);
    expect(e?.hi.high).toBe(97);
    expect(new Date(e!.hi.day).getDate()).toBe(9);
    expect(e?.lo.low).toBe(3);
    expect(new Date(e!.lo.day).getDate()).toBe(12);
    expect(spokenBand(rows)).toBe("3 days with readings. Lowest 3 percent on Oct 12; highest 97 percent on Oct 9.");
    expect(spokenBand([])).toBe("No readings.");
    expect(extremesOf([])).toBeNull();
    expect(extremesOf(dailyRanges([pt(1, 9, 50), pt(1, 10, 50)], []))).toBeNull();
  });

  it("smooths without overshooting a real low or high", () => {
    const xs = [0, 1, 2, 3, 4];
    const ys = [60, 97, 58, 3, 70];
    const f = monotoneAt(xs, ys);
    for (let i = 0; i < xs.length; i++) expect(f(xs[i])).toBeCloseTo(ys[i], 6);
    let max = -Infinity;
    let min = Infinity;
    for (let x = 0; x <= 4; x += 0.05) {
      max = Math.max(max, f(x));
      min = Math.min(min, f(x));
    }
    expect(max).toBeLessThanOrEqual(97 + 1e-9);
    expect(min).toBeGreaterThanOrEqual(3 - 1e-9);
  });

  it("draws one limit: the last step, else today's target on the current period, else none", () => {
    expect(limitValue([{ x0: 0, x1: 1, pct: 80 }, { x0: 1, x1: 2, pct: 70 }], true, 75)).toBe(70);
    expect(limitValue([], true, 70)).toBe(70);
    expect(limitValue([], false, 70)).toBeNull();
    expect(limitValue([], true, 100)).toBeNull();
    expect(limitValue([], true, null)).toBeNull();
  });
});
