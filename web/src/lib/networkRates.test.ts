import { describe, expect, it } from "vitest";
import { memberSaving, networkRateSet } from "./networkRates";

describe("networkRates", () => {
  it("a blank (0) row is unset, and a guest or a member price makes it set", () => {
    expect(networkRateSet({ price_per_kwh: 0 })).toBe(false);
    expect(networkRateSet({ price_per_kwh: 0, member_price_per_kwh: 0 })).toBe(false);
    expect(networkRateSet({ price_per_kwh: 0.48 })).toBe(true);
    expect(networkRateSet({ price_per_kwh: 0, member_price_per_kwh: 0.36 })).toBe(true);
  });
  it("the saving shows only when both prices were entered", () => {
    expect(memberSaving({ price_per_kwh: 0.48, member_price_per_kwh: 0.36 })).toBeCloseTo(0.12);
    expect(memberSaving({ price_per_kwh: 0.48 })).toBe(0);
    expect(memberSaving({ price_per_kwh: 0, member_price_per_kwh: 0.36 })).toBe(0);
    expect(memberSaving({ price_per_kwh: 0.3, member_price_per_kwh: 0.4 })).toBe(0);
  });
});
