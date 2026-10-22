import { describe, expect, it } from "vitest";
import { dayTicks, pctLabel } from "./SocTrendChart";

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
