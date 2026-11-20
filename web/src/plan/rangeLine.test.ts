import { describe, expect, it } from "vitest";
import type { VehicleState } from "../lib/api";
import { rangeLine } from "./rangeLine";

const NOW = Date.parse("2026-11-20T12:00:00Z");
const at = (secAgo: number) => new Date(NOW - secAgo * 1000).toISOString();
const state = (over: Partial<VehicleState> = {}): VehicleState =>
  ({ at: at(20), battery_level_pct: 80, distance_to_empty: 372, power_state: "ready", gear_status: "park", charger_state: "", latitude: 34, longitude: -118, ...over }) as unknown as VehicleState;

describe("rangeLine", () => {
  it("says the range from the truck's own frame, in the chosen unit", () => {
    expect(rangeLine(state(), "mi", NOW)).toBe("231 mi of range, from where your truck is parked now");
    expect(rangeLine(state(), "km", NOW)).toBe("372 km of range, from where your truck is parked now");
  });
  it("is hidden with no state, no range or an old frame", () => {
    expect(rangeLine(undefined, "mi", NOW)).toBeNull();
    expect(rangeLine(state({ distance_to_empty: 0 }), "mi", NOW)).toBeNull();
    expect(rangeLine(state({ at: at(3 * 24 * 3600) }), "mi", NOW)).toBeNull();
  });
});
