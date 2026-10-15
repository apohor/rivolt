import { describe, expect, it } from "vitest";
import { formatDistance } from "./preferences";

describe("formatDistance", () => {
  it("converts km to miles with privacy-safe rounding", () => {
    expect(formatDistance(412.3, "mi")).toBe("255 mi");
    expect(formatDistance(100, "mi")).toBe("60 mi");
    expect(formatDistance(30, "mi")).toBe("19 mi");
    expect(formatDistance(0.4, "mi")).toBe("0 mi");
  });
  it("keeps the km rounding", () => {
    expect(formatDistance(412.3, "km")).toBe("410 km");
    expect(formatDistance(37.6, "km")).toBe("38 km");
  });
  it("shows one decimal under 10 only when fine", () => {
    expect(formatDistance(4.26, "km", { fine: true })).toBe("4.3 km");
    expect(formatDistance(4.26, "mi", { fine: true })).toBe("2.6 mi");
    expect(formatDistance(4.26, "km")).toBe("4 km");
  });
  it("handles missing values", () => {
    expect(formatDistance(null, "mi")).toBe("—");
  });
});
