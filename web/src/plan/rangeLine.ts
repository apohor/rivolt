import type { VehicleState } from "../lib/api";
import { computeNow } from "../lib/nowState";

const KM_PER_MI = 1.609344;

// The one range line on the planner's front door: what the truck can drive from where it is. It reads the truck's own last frame
// through computeNow, so it is hidden (null) whenever the state is unknown, stale or offline: a guess would be worse than nothing.
export function rangeLine(state: VehicleState | undefined, unit: "mi" | "km", nowMs: number): string | null {
  if (!state) return null;
  const v = computeNow({ state, nowMs, asleepSinceMs: null, socAtChargeStart: null, peakKw: null, packKwh: null, homeRate: null, home: null });
  if (v.kind === "offline" || v.stale || v.rangeMi == null) return null;
  const n = Math.round(unit === "mi" ? v.rangeMi : v.rangeMi * KM_PER_MI);
  if (n <= 0) return null;
  const where = v.kind === "driving" ? "is now" : "is parked now";
  return `${n} ${unit} of range, from where your truck ${where}`;
}
