import { describe, expect, it } from "vitest";
import type { Charge, Drive } from "./api";
import { MIN_LAST_WEEK_MILES, changeLine, yourWeek } from "./yourWeek";

// Wednesday 28 Oct 2026, noon, local. This week starts Monday 26 Oct; last week Monday 19 Oct.
const NOW = new Date(2026, 9, 28, 12, 0);
const at = (d: number, h = 9) => new Date(2026, 9, d, h, 0).toISOString();
const drive = (day: number, miles: number, h = 9) => ({ StartedAt: at(day, h), EndedAt: at(day, h + 1), DistanceMi: miles }) as unknown as Drive;
const charge = (day: number, kwh: number, extra: object = {}, h = 18) =>
  ({ StartedAt: at(day, h), EndedAt: at(day, h + 2), EnergyAddedKWh: kwh, Cost: 0, ...extra }) as unknown as Charge;

describe("your week", () => {
  it("is nothing until something is recorded this week", () => {
    expect(yourWeek([], [], NOW)).toBeNull();
    expect(yourWeek([drive(20, 50)], [charge(21, 20)], NOW)).toBeNull();
  });

  it("sums this week's miles, energy and cost (Monday to now)", () => {
    const w = yourWeek([drive(26, 40), drive(27, 60), drive(25, 99)], [charge(27, 30, { Cost: 9, Currency: "USD", cost_source_effective: "rivian" }), charge(20, 80)], NOW)!;
    expect(w.week.miles).toBe(100);
    expect(w.week.kWh).toBe(30);
    expect(w.week.cost).toBe(9);
    expect(w.week.currency).toBe("USD");
    expect(w.week.costEstimated).toBe(false);
  });

  it("calls the cost an estimate unless every priced charge was stated by Rivian or the owner", () => {
    const stated = yourWeek([drive(26, 10)], [charge(26, 10, { Cost: 4, Currency: "USD", cost_source_effective: "user" })], NOW)!;
    expect(stated.week.costEstimated).toBe(false);
    const home = yourWeek([drive(26, 10)], [charge(26, 10, { Cost: 4, Currency: "USD", cost_source_effective: "home_rate" })], NOW)!;
    expect(home.week.costEstimated).toBe(true);
    const onlyEstimated = yourWeek([drive(26, 10)], [charge(26, 10, { estimated_cost: 3, estimated_currency: "USD" })], NOW)!;
    expect(onlyEstimated.week.costEstimated).toBe(true);
    expect(onlyEstimated.week.cost).toBe(3);
  });

  it("compares miles with last week up to the same moment, not the whole week", () => {
    // Last week: 80 mi by Wednesday noon, plus 400 mi on the weekend that must not count against this week so far.
    const w = yourWeek([drive(26, 60), drive(19, 50), drive(21, 30), drive(24, 400)], [], NOW)!;
    expect(w.milesChangePct).toBe(-25); // 60 against 80
    // A drive after this moment last week (Wed 21:00 > Wed 12:00) is not in the comparison.
    const late = yourWeek([drive(26, 60), drive(19, 50), drive(21, 30, 20)], [], NOW)!;
    expect(late.milesChangePct).toBe(20); // 60 against 50
  });

  it("says nothing about a change when last week gives nothing to compare with", () => {
    expect(yourWeek([drive(26, 60)], [], NOW)!.milesChangePct).toBeNull();
    expect(yourWeek([drive(26, 60), drive(19, MIN_LAST_WEEK_MILES - 1)], [], NOW)!.milesChangePct).toBeNull();
    // Only charging this week: nothing to say about miles.
    expect(yourWeek([drive(19, 80)], [charge(27, 20)], NOW)!.milesChangePct).toBeNull();
  });

  it("puts the change in plain words", () => {
    expect(changeLine(12)).toBe("▲ 12% miles vs last week so far");
    expect(changeLine(-30)).toBe("▼ 30% miles vs last week so far");
    expect(changeLine(0)).toBe("About the same miles as last week so far");
    expect(changeLine(null)).toBeNull();
  });
});
