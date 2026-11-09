import { describe, expect, it } from "vitest";
import { offRouteLine } from "./tripPure";

describe("offRouteLine", () => {
  it("says how far a station is from the route in the user's unit", () => {
    expect(offRouteLine(5.2, "mi")).toBe("3.2 mi off route");
    expect(offRouteLine(5.2, "km")).toBe("5.2 km off route");
    expect(offRouteLine(16, "mi")).toBe("9.9 mi off route");
    expect(offRouteLine(30, "km")).toBe("30 km off route");
    expect(offRouteLine(100, "mi")).toBe("62 mi off route");
  });
  it("calls a station on the line on the route, and says nothing without a distance", () => {
    expect(offRouteLine(0.05, "mi")).toBe("On the route");
    expect(offRouteLine(undefined, "mi")).toBe("");
    expect(offRouteLine(Number.NaN, "km")).toBe("");
    expect(offRouteLine(-1, "km")).toBe("");
  });
});
