import { describe, expect, it } from "vitest";
import { clampDrivingHours, drivingHoursLabel, drivingHoursOptions } from "./drivingBudget";

describe("drivingBudget", () => {
  it("keeps the limit in 3..12 on half hours", () => {
    expect(clampDrivingHours(1)).toBe(3);
    expect(clampDrivingHours(20)).toBe(12);
    expect(clampDrivingHours(7.3)).toBe(7.5);
    expect(clampDrivingHours(Number.NaN)).toBe(8);
  });
  it("labels whole and half hours", () => {
    expect(drivingHoursLabel(8)).toBe("8 h");
    expect(drivingHoursLabel(7.5)).toBe("7.5 h");
  });
  it("lists every half hour from 3 to 12", () => {
    const o = drivingHoursOptions();
    expect(o[0]).toBe(3);
    expect(o.at(-1)).toBe(12);
    expect(o).toHaveLength(19);
  });
});
