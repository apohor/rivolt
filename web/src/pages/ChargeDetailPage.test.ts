import { describe, expect, it } from "vitest";
import type { Charge } from "../lib/api";
import { isFreeCharge } from "./ChargeDetailPage";

const base = { Cost: 0 } as Charge;

describe("isFreeCharge", () => {
  it("is Free only for an explicit user price of zero", () => {
    expect(isFreeCharge({ ...base, CostSource: "user" })).toBe(true);
    expect(isFreeCharge({ ...base })).toBe(false); // unpriced, not free
    expect(isFreeCharge({ ...base, CostSource: "user", Cost: 4 })).toBe(false);
    expect(isFreeCharge({ ...base, CostSource: "home_rate" })).toBe(false);
  });
});
