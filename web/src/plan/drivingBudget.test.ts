import { describe, expect, it } from "vitest";
import { clampDrivingHours, drivingHoursLabel, drivingHoursOptions } from "./drivingBudget";

describe("drivingBudget", () => {
  it("keeps the limit in 4..14 on whole hours", () => {
    expect(clampDrivingHours(1)).toBe(4);
    expect(clampDrivingHours(20)).toBe(14);
    expect(clampDrivingHours(7.3)).toBe(7);
    expect(clampDrivingHours(Number.NaN)).toBe(8);
  });
  it("labels the hours", () => {
    expect(drivingHoursLabel(8)).toBe("8 h");
  });
  it("lists every hour from 4 to 14", () => {
    const o = drivingHoursOptions();
    expect(o[0]).toBe(4);
    expect(o.at(-1)).toBe(14);
    expect(o).toHaveLength(11);
  });
});
