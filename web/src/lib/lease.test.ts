import { describe, expect, it } from "vitest";
import type { LeaseStatus } from "./api";
import { leaseBar, planStanding, validateLease } from "./lease";

const st = (o: Partial<LeaseStatus> = {}): LeaseStatus => ({
  used_mi: 21850, allowed_to_date_mi: 20130, ahead_behind_mi: -1720, days_total: 1095, days_elapsed: 700, days_left: 395,
  projected_total_mi: 39720, projected_return_odometer_mi: 39732, projected_overage_mi: 3720, projected_overage_cost: 930,
  miles_per_day_budget: 35.7, pace_mi_per_day: 31.2, ...o,
});

describe("leaseBar", () => {
  it("scales to the larger of the allowance and the projection", () => {
    const b = leaseBar(st(), 36000);
    expect(b.allowedPct).toBeCloseTo((36000 / 39720) * 100, 1);
    expect(b.projectedPct).toBe(100);
    expect(b.tone).toBe("watch"); // over the pro-rata plan, not yet over the total
  });
  it("is ok under plan, over past the total", () => {
    expect(leaseBar(st({ used_mi: 15000 }), 36000).tone).toBe("ok");
    expect(leaseBar(st({ used_mi: 37000 }), 36000).tone).toBe("over");
  });
  it("copes with an unknown odometer or projection", () => {
    const b = leaseBar(st({ used_mi: null, projected_total_mi: null }), 36000);
    expect(b.usedPct).toBe(0);
    expect(b.projectedPct).toBeNull();
  });
});

describe("planStanding", () => {
  it("says under, over or on plan in plain words", () => {
    expect(planStanding(st({ ahead_behind_mi: 1720.4 }))).toEqual({ text: "1,720 mi under plan", tone: "ok" });
    expect(planStanding(st({ ahead_behind_mi: -340 }))).toEqual({ text: "340 mi over plan", tone: "watch" });
    expect(planStanding(st({ ahead_behind_mi: 0.2 }))).toEqual({ text: "on plan", tone: "ok" });
    expect(planStanding(st({ ahead_behind_mi: null }))).toBeNull();
  });
});

describe("validateLease", () => {
  const ok = { start_date: "2024-11-03", end_date: "2027-11-03", start_odometer_mi: "12", allowed_miles_total: "36000", overage_per_mile: "0.25", currency: "USD" };
  it("accepts good terms", () => expect(validateLease(ok)).toEqual({}));
  it("flags each problem", () => {
    const e = validateLease({ ...ok, end_date: "2024-01-01", allowed_miles_total: "0", start_odometer_mi: "-1", overage_per_mile: "" });
    expect(Object.keys(e).sort()).toEqual(["allowed_miles_total", "end_date", "overage_per_mile", "start_odometer_mi"]);
    expect(validateLease({ ...ok, start_date: "" }).start_date).toBeTruthy();
  });
});
