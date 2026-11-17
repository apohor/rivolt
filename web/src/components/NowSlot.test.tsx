import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import NowSlot, { slotAvailable, slotStats } from "./NowSlot";
import type { NowView } from "../lib/nowState";

const base = { kind: "charging", powerKw: 7.4, etaHours: 1.2, etaSource: "truck", limitPct: 80, energyKwh: 18.24, costSoFar: { amount: 2.91, basis: "home_rate" }, speedMph: null } as unknown as NowView;
const drive = { distance_mi: 12.34, elapsed_sec: 1500, energy_used_kwh: 4.2, soc_used_pct: 5 };

describe("NowSlot", () => {
  it("charging shows power, limit time and only the values that exist", () => {
    render(<NowSlot view={base} drive={null} nowMs={Date.UTC(2026, 0, 1, 12)} unit="mi" currency="USD" />);
    expect(screen.getByTestId("now-slot-big").textContent).toContain("7.4");
    expect(screen.getByTestId("now-slot-sub").textContent).toMatch(/^Reaches 80% at /);
    expect(screen.getByTestId("now-slot-added").textContent).toContain("18.2 kWh");
    expect(screen.getByTestId("now-slot-cost-so-far").textContent).toContain("2.91");
    const noCost = { ...base, costSoFar: null, energyKwh: null } as unknown as NowView;
    expect(slotStats(noCost, null, 0, "mi", "USD").map((s) => s.k)).toEqual(["Time left"]);
  });
  it("driving shows the drive so far in the user's unit", () => {
    const v = { kind: "driving", speedMph: 62, powerKw: null } as unknown as NowView;
    const mi = slotStats(v, drive, 0, "mi", "USD");
    expect(mi.map((s) => s.k)).toEqual(["Distance", "Time", "Efficiency", "Energy used"]);
    expect(mi[0].v).toBe("12.3 mi");
    expect(mi[2].v).toBe("2.9 mi/kWh");
    expect(slotStats(v, drive, 0, "km", "USD")[0].v).toBe("19.9 km");
    expect(slotStats(v, null, 0, "mi", "USD")).toEqual([]);
  });
  it("has no slot when the big figure is missing or the state is parked", () => {
    expect(slotAvailable({ kind: "charging", powerKw: null } as unknown as NowView)).toBe(false);
    expect(slotAvailable({ kind: "parked" } as unknown as NowView)).toBe(false);
    expect(slotAvailable(base)).toBe(true);
  });
});
