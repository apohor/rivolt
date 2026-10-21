import { describe, expect, it } from "vitest";
import type { Charge } from "../lib/api";
import { chargeCostHint, isFreeCharge } from "./ChargeDetailPage";

const base = { Cost: 0 } as Charge;

describe("isFreeCharge", () => {
  it("is Free only for an explicit user price of zero", () => {
    expect(isFreeCharge({ ...base, CostSource: "user" })).toBe(true);
    expect(isFreeCharge({ ...base })).toBe(false); // unpriced, not free
    expect(isFreeCharge({ ...base, CostSource: "user", Cost: 4 })).toBe(false);
    expect(isFreeCharge({ ...base, CostSource: "home_rate" })).toBe(false);
  });
});

describe("chargeCostHint", () => {
  const billed = { ...base, Cost: 29, Currency: "USD", PricePerKWh: 0.967, EnergyAddedKWh: 30, cost_source_effective: "rivian" } as Charge;
  it("says a Rivian-billed cost was billed by Rivian, with its rate", () => {
    expect(chargeCostHint(billed)).toBe("billed by Rivian · at 0.967 USD/kWh");
  });
  it("says only the source when there is no rate", () => {
    expect(chargeCostHint({ ...billed, PricePerKWh: 0, EnergyAddedKWh: 0 })).toBe("billed by Rivian");
  });
  it("leaves other priced charges as they were", () => {
    expect(chargeCostHint({ ...billed, cost_source_effective: "home_rate" })).toBe("at 0.967 USD/kWh");
    expect(chargeCostHint({ ...billed, cost_source_effective: "user" })).toBe("at 0.967 USD/kWh");
  });
});
