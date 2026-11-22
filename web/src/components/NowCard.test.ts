import { describe, expect, it } from "vitest";
import { stateRefetchMs } from "./NowCard";
import type { VehicleState } from "../lib/api";

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
