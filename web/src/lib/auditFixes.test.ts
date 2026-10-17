import { describe, expect, it } from "vitest";
import { shareByEnergy } from "./analytics";
import { barDayCount } from "./period";
import { currentMonth, ledgerMonthFixture, spendThroughDay } from "./ledger";
import { formatWaypointTime } from "../plan/format";

describe("shareByEnergy", () => {
  it("is the kWh share, as whole percents that add up to 100", () => {
    // Demo month: 210.7 / 8.7 / 37.9 kWh -> 82 / 3 / 15 (not the 94 / 3 / 3 session share).
    expect(shareByEnergy([210.7, 8.7, 37.9])).toEqual([82, 3, 15]);
    for (const k of [[1, 1, 1], [0.4, 0.4, 0.2], [33.3, 33.3, 33.4], [5, 0, 0], [10, 20, 30, 40]]) {
      expect(shareByEnergy(k).reduce((a, b) => a + b, 0), JSON.stringify(k)).toBe(100);
    }
    expect(shareByEnergy([0, 0, 0])).toEqual([0, 0, 0]);
  });
});

describe("barDayCount", () => {
  it("counts calendar days inclusive, whatever the time of day", () => {
    // Year chart began on 12-31: Jan 1 00:00 to Oct 17 12:00 is 290 days, not 291.
    expect(barDayCount(new Date(2026, 0, 1, 0, 0), new Date(2026, 9, 17, 12, 0))).toBe(290);
    expect(barDayCount(new Date(2026, 9, 1), new Date(2026, 9, 1, 23, 59))).toBe(1);
    expect(barDayCount(new Date(2026, 2, 1), new Date(2026, 2, 31, 12))).toBe(31); // across DST
  });
});

describe("spendThroughDay", () => {
  it("sums receipts through a day of the month", () => {
    const m = ledgerMonthFixture(currentMonth());
    const all = spendThroughDay(m, 31);
    expect(spendThroughDay(m, 0)).toBe(0);
    expect(spendThroughDay(m, 10)).toBeLessThanOrEqual(all);
    expect(all).toBeGreaterThan(0);
  });
});

describe("formatWaypointTime", () => {
  it("rounds to the nearest minute when asked", () => {
    const t = "2026-10-17T15:37:40Z";
    const plain = formatWaypointTime(t, 0);
    const rounded = formatWaypointTime(t, 0, true);
    expect(plain).not.toBe(rounded);
    expect(formatWaypointTime("2026-10-17T15:37:20Z", 0, true)).toBe(plain);
  });
});
