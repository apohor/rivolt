import { describe, expect, it } from "vitest";
import { reservePct, underReserve } from "./reserve";

describe("reserve", () => {
  it("reads the field", () => {
    expect(reservePct("20")).toBe(20);
    expect(reservePct(" 15 ")).toBe(15);
    expect(reservePct("")).toBeNull();
    expect(reservePct("0")).toBeNull();
    expect(reservePct("abc")).toBeNull();
    expect(reservePct("150")).toBeNull();
  });
  it("is under only when the arrival is known and below it", () => {
    expect(underReserve(12, 20)).toBe(true);
    expect(underReserve(20, 20)).toBe(false);
    expect(underReserve(24, 20)).toBe(false);
    expect(underReserve(0, 20)).toBe(false);
    expect(underReserve(5, null)).toBe(false);
  });
});
