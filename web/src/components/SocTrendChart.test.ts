import { describe, expect, it } from "vitest";
import { dailyRanges, dayTicks, extremesOf, limitSegment, limitSteps, monotoneAt, pctLabel, spokenBand } from "./SocTrendChart";

const at = (day: number, hour: number) => new Date(2026, 9, day, hour, 0, 0).getTime();
const pt = (day: number, hour: number, y: number) => ({ x: at(day, hour), y });

describe("SocTrendChart axis and labels", () => {
  it("labels a month on the 1st, 11th, 21st and its last day, through its end, as Miles per day", () => {
    const t = dayTicks(new Date(2026, 9, 1).getTime(), new Date(2026, 10, 1).getTime(), 5);
    // Short month and day in the reader's language ("Oct 1"), not "10-01".
    const name = (m: number, d: number) => new Date(2026, m, d).toLocaleDateString(undefined, { month: "short", day: "numeric" });
    expect(t.map((d) => d.label)).toEqual([name(9, 1), name(9, 11), name(9, 21), name(9, 31)]);
    expect(t[0].label).not.toContain("10-01");
    expect(t[3].x).toBe(new Date(2026, 9, 31).getTime());
    expect(dayTicks(new Date(2026, 8, 1).getTime(), new Date(2026, 9, 1).getTime(), 5).map((d) => d.label)).toEqual([name(8, 1), name(8, 11), name(8, 21), name(8, 30)]);
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

  it("draws one stretch of limit from its last change, never one line across the whole period", () => {
    const OCT = new Date(2026, 9, 1).getTime();
    const NOV = new Date(2026, 10, 1).getTime();
    const DAYMS = 86_400_000;
    const ch = (day: number, limit: number | null) => ({ x0: at(day, 8), x1: at(day, 10), limit });
    // Limits 70, 100, 70, 85 and 80 over the month, then today's target 70.
    const charges = [ch(3, 70), ch(8, 100), ch(9, 70), ch(12, 85), ch(19, 80)];
    const now = at(22, 12);
    const steps = limitSteps(charges, OCT, NOV, now, 70);
    const seg = limitSegment(steps, true, 70, OCT, now)!;
    expect(seg.pct).toBe(70);
    expect(seg.x1).toBe(now);
    expect(seg.x0 - OCT).toBeGreaterThan((NOV - OCT) / 2);
    expect(seg.x1 - seg.x0).toBeLessThan((NOV - OCT) / 2);
    // An earlier period: the last limit known in it, to its end.
    const later = at(42, 12);
    const past = limitSegment(limitSteps(charges, OCT, NOV, later, null), false, null, OCT, later)!;
    expect(past.pct).toBe(80);
    expect(past.x1).toBe(NOV);
    expect(past.x1 - past.x0).toBeLessThan((NOV - OCT) / 2);
    // No stored limits: today's target over the last two days, current period only; none otherwise.
    const none = limitSegment([], true, 70, OCT, now)!;
    expect(none.x1 - none.x0).toBe(2 * DAYMS);
    expect(limitSegment([], false, 70, OCT, now)).toBeNull();
    expect(limitSegment([], true, 100, OCT, now)).toBeNull();
    expect(limitSegment([], true, null, OCT, now)).toBeNull();
  });
});
