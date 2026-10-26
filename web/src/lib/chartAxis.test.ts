import { describe, expect, it } from "vitest";
import { dayName, niceAxis } from "./chartAxis";

describe("niceAxis", () => {
  it("rounds the top and the ticks, whole numbers, two or three intervals", () => {
    expect(niceAxis(509)).toEqual({ top: 600, ticks: [0, 200, 400, 600] });
    expect(niceAxis(500)).toEqual({ top: 500, ticks: [0, 250, 500] });
    expect(niceAxis(97)).toEqual({ top: 100, ticks: [0, 50, 100] });
    expect(niceAxis(42)).toEqual({ top: 50, ticks: [0, 25, 50] });
    expect(niceAxis(7)).toEqual({ top: 10, ticks: [0, 5, 10] });
    expect(niceAxis(1).ticks[0]).toBe(0);
  });
  it("never cuts the data off and never invents a tiny maximum", () => {
    for (const m of [0.4, 1, 3, 9, 23, 77, 136, 509, 999, 1500, 12345]) {
      const a = niceAxis(m);
      expect(a.top).toBeGreaterThanOrEqual(m);
      expect(a.ticks[a.ticks.length - 1]).toBe(a.top);
      expect(a.ticks.length).toBeLessThanOrEqual(5);
      expect(a.ticks.every(Number.isInteger)).toBe(true);
    }
    expect(niceAxis(0)).toEqual(niceAxis(1));
    expect(niceAxis(Number.NaN)).toEqual(niceAxis(1));
  });
});

describe("dayName", () => {
  it("names a day in short month and number, from a date or a day label", () => {
    expect(dayName("2026-10-01")).toBe(new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(new Date(2026, 9, 1)));
    expect(dayName(new Date(2026, 9, 11))).toContain("11");
    expect(dayName("2026-10-01")).not.toContain("10-01");
  });
});
