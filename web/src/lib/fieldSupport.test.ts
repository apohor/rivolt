import { describe, expect, it } from "vitest";
import { fieldHidden, fieldLabel, fieldLevel } from "./fieldSupport";

const v = { field_support: { charge_time_remaining: "estimate", outside_temp: "unavailable", power_state: "best_effort" } } as const;

describe("fieldSupport", () => {
  it("labels an estimate and hides what is unavailable, going only by the map", () => {
    expect(fieldLabel(v, "charge_time_remaining", "Time to full")).toBe("Time to full (estimate)");
    expect(fieldLabel(v, "power_state", "Power")).toBe("Power");
    expect(fieldHidden(v, "outside_temp")).toBe(true);
    expect(fieldHidden(v, "power_state")).toBe(false);
  });
  it("treats a missing map or entry as reported", () => {
    expect(fieldLevel(undefined, "outside_temp")).toBe("reported");
    expect(fieldLevel({}, "outside_temp")).toBe("reported");
    expect(fieldHidden({ field_support: {} }, "trip_target")).toBe(false);
  });
});
