import { describe, expect, it } from "vitest";
import { placeLine, stateRefetchMs } from "./NowCard";
import type { VehicleState } from "../lib/api";
import type { NowView } from "../lib/nowState";

const away = { place: "away", kmFromHome: 4.26 } as unknown as NowView;

describe("placeLine", () => {
  it("renders miles by default unit and km on request", () => {
    expect(placeLine(away, "mi")).toBe("Parked · 2.6 mi from home");
    expect(placeLine(away, "km")).toBe("Parked · 4.3 km from home");
  });
});

describe("stateRefetchMs", () => {
  const st = (o: Partial<VehicleState>) => o as VehicleState;
  it("reads every 2 s in gear and every 15 s otherwise", () => {
    expect(stateRefetchMs(st({ gear: "D" }), false)).toBe(2_000);
    expect(stateRefetchMs(st({ gear: "R" }), false)).toBe(2_000);
    expect(stateRefetchMs(st({ gear: "P" }))).toBe(15_000);
    expect(stateRefetchMs(st({ gear: "P", speed_kph: 3 }))).toBe(15_000);
    expect(stateRefetchMs(st({ charger_state: "charging_active" }))).toBe(15_000);
    expect(stateRefetchMs(undefined)).toBe(15_000);
  });
  it("keeps the demo on 15 s even in gear", () => {
    expect(stateRefetchMs(st({ gear: "D" }), true)).toBe(15_000);
  });
});
