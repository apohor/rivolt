import { describe, expect, it } from "vitest";
import { bucketLabel, effUnit, effValue, hasValues, scaleMax } from "./efficiency";

describe("bucketLabel", () => {
  it("labels temperature buckets in the user's unit", () => {
    expect(bucketLabel("temp", { key: "a", hi_c: 0, mi_per_kwh: 1, drives: 5, miles: 1 }, "mi", "c")).toBe("<0°");
    expect(bucketLabel("temp", { key: "b", lo_c: 10, hi_c: 20, mi_per_kwh: 1, drives: 5, miles: 1 }, "mi", "c")).toBe("10–20°");
    expect(bucketLabel("temp", { key: "b", lo_c: 10, hi_c: 20, mi_per_kwh: 1, drives: 5, miles: 1 }, "mi", "f")).toBe("50–68°");
    expect(bucketLabel("temp", { key: "c", lo_c: 30, mi_per_kwh: 1, drives: 5, miles: 1 }, "mi", "c")).toBe("30°+");
  });
  it("labels speed buckets in the user's unit", () => {
    expect(bucketLabel("speed", { key: "a", hi_mph: 35, mi_per_kwh: 1, drives: 5, miles: 1 }, "mi", "c")).toBe("<35");
    expect(bucketLabel("speed", { key: "b", lo_mph: 55, hi_mph: 65, mi_per_kwh: 1, drives: 5, miles: 1 }, "mi", "c")).toBe("55–65");
    expect(bucketLabel("speed", { key: "b", lo_mph: 55, hi_mph: 65, mi_per_kwh: 1, drives: 5, miles: 1 }, "km", "c")).toBe("89–105");
    expect(bucketLabel("speed", { key: "c", lo_mph: 75, mi_per_kwh: 1, drives: 5, miles: 1 }, "mi", "c")).toBe("75+");
  });
  it("uses the server's name for a drive mode", () => {
    expect(bucketLabel("mode", { key: "everyday", label: "All-Purpose", mi_per_kwh: 1, drives: 5, miles: 1 }, "mi", "c")).toBe("All-Purpose");
    expect(bucketLabel("mode", { key: "sport", mi_per_kwh: 1, drives: 5, miles: 1 }, "mi", "c")).toBe("sport");
  });
});

describe("units and scale", () => {
  it("converts mi/kWh to km/kWh", () => {
    expect(effUnit("km")).toBe("km/kWh");
    expect(effValue(2, "km")).toBeCloseTo(3.2187, 3);
    expect(effValue(2, "mi")).toBe(2);
  });
  it("scales a little above the best value, never below 1", () => {
    expect(scaleMax([2, null, 3], 2.3)).toBeCloseTo(3.45, 2);
    expect(scaleMax([null], null)).toBeCloseTo(1.15, 2);
  });
  it("hasValues needs one non-null bucket", () => {
    expect(hasValues(undefined)).toBe(false);
    expect(hasValues([{ key: "a", mi_per_kwh: null, drives: 1, miles: 1 }])).toBe(false);
    expect(hasValues([{ key: "a", mi_per_kwh: 2, drives: 9, miles: 1 }])).toBe(true);
  });
});
