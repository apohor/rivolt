import { describe, expect, it } from "vitest";
import { ENERGY_ESTIMATED_NOTE, energyKWhText, isEnergyEstimated } from "./energyEstimated";
import { chargeCostLine } from "./activityFeed";
import type { Charge } from "./api";

const charge = (p: Partial<Charge> = {}): Charge =>
  ({ ID: "c", StartedAt: "2026-10-20T01:00:00Z", EndedAt: "2026-10-20T05:00:00Z", FinalState: "charging_complete", EnergyAddedKWh: 12.34, Cost: 1.85, Currency: "USD", PricePerKWh: 0.15, MaxPowerKW: 7, CostSource: "home_rate", ...p }) as Charge;

describe("estimated energy", () => {
  it("reads the flag from either API's key", () => {
    expect(isEnergyEstimated({ EnergyEstimated: true })).toBe(true);
    expect(isEnergyEstimated({ energy_estimated: true })).toBe(true);
    expect(isEnergyEstimated({})).toBe(false);
    expect(isEnergyEstimated({ EnergyEstimated: false, energy_estimated: false })).toBe(false);
  });
  it("marks the kWh with a tilde, and never an unflagged one", () => {
    expect(energyKWhText(charge({ EnergyEstimated: true }))).toBe("~12.3 kWh");
    expect(energyKWhText(charge())).toBe("12.3 kWh");
    expect(energyKWhText(charge({ EnergyEstimated: true, EnergyAddedKWh: 0 }))).toBe("—");
  });
  it("makes the cost an estimate too, unless it is free or has no price", () => {
    const est = chargeCostLine(charge({ EnergyEstimated: true }));
    expect(est).toEqual({ text: "~$1.85", tone: "est" });
    expect(chargeCostLine(charge())).toEqual({ text: "$1.85", tone: "ok" });
    expect(chargeCostLine(charge({ EnergyEstimated: true, CostSource: "user", Cost: 0 })).text).toBe("Free");
    expect(chargeCostLine(charge({ EnergyEstimated: true, Cost: 0 })).text).toBe("Unpriced");
    // Never a double tilde.
    expect(chargeCostLine(charge({ EnergyEstimated: true, CostSource: "network_estimate", MaxPowerKW: 150, Cost: 20 })).text).toBe("~$20.00");
  });
  it("says only what DA's wording says, and never claims the figure is metered or exact", () => {
    expect(ENERGY_ESTIMATED_NOTE).toBe("Estimated from the battery's change. The energy count for this charge was lost.");
    expect(ENERGY_ESTIMATED_NOTE).not.toMatch(/exact|metered|measured|accurate/i);
  });
});
