import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import NowSlot, { slotAvailable, slotStats, type SlotInput } from "./NowSlot";
import type { NowView } from "../lib/nowState";

const charging = { kind: "charging", powerKw: 7.4, etaHours: 1.2, etaSource: "truck", limitPct: 80, energyKwh: 18.24, chargeFlavor: "home_l2", speedMph: null } as unknown as NowView;
const driving = { kind: "driving", speedMph: 62, powerKw: null } as unknown as NowView;
const session = { range_added_km: 98, total_charged_energy_kwh: 18.24, time_elapsed_seconds: 3900, kilometers_charged_per_hour: 61 };
const drive = { tripMi: 12.34, elapsed_sec: 1500, energy_used_kwh: 4.2, soc_used_pct: 5 };
const base = (v: NowView, extra: Partial<SlotInput> = {}): SlotInput => ({ view: v, nowMs: Date.UTC(2026, 0, 1, 12), unit: "mi", currency: "USD", ...extra });

describe("NowSlot", () => {
  it("charging shows power, charger, the limit, the session and the priced cost", () => {
    render(<NowSlot {...base(charging, { session, charge: { started_at: "2026-01-01T11:00:00Z", cost: 2.91, currency: "USD", cost_source: "home_rate", cost_is_estimate: true } })} />);
    expect(screen.getByTestId("now-slot-big").textContent).toContain("7.4");
    expect(screen.getByTestId("now-slot-charger").textContent).toBe("Home charger");
    expect(screen.getByTestId("now-slot-sub").textContent).toMatch(/^Reaches 80% at /);
    expect(screen.getByTestId("now-slot-small").textContent).toBe("1 h 12 m left · target 80%");
    expect(screen.getByTestId("now-slot-added").textContent).toContain("+18.2 kWh");
    expect(screen.getByTestId("now-slot-range-added").textContent).toContain("+60.9 mi");
    expect(screen.getByTestId("now-slot-elapsed").textContent).toContain("1 h 05");
    expect(screen.getByTestId("now-slot-cost-so-far").textContent).toMatch(/≈ .*2\.91.*at your home rate/);
    expect(screen.getByTestId("now-slot-plugged-in")).toBeTruthy();
  });
  it("a billed cost has no estimate mark; no cost shows no cost; no session falls back to the estimate", () => {
    const billed = slotStats(base(charging, { charge: { started_at: "x", cost: 5, cost_source: "rivian", cost_is_estimate: false } }));
    expect(billed.find((s) => s.k === "Cost so far")).toMatchObject({ v: "$5.00", note: "billed by Rivian" });
    const none = slotStats(base(charging, { charge: { started_at: "x", cost: null } }));
    expect(none.map((s) => s.k)).toEqual(["Added"]);
    expect(none[0].v).toBe("≈ +18.2 kWh");
  });
  it("driving shows gear, mode and the drive so far in the user's unit", () => {
    render(<NowSlot {...base(driving, { gear: "d", mode: "sport", drive })} />);
    expect(screen.getByTestId("now-slot-gear").textContent).toBe("D");
    expect(screen.getByTestId("now-slot-mode").textContent).toBe("Sport");
    const mi = slotStats(base(driving, { drive }));
    expect(mi.map((s) => s.k)).toEqual(["Distance", "Time", "Efficiency", "Energy used"]);
    expect(mi[0].v).toBe("12.3 mi");
    expect(mi[2].v).toBe("2.9 mi/kWh");
    const km = slotStats(base(driving, { drive, unit: "km" }));
    expect(km[0].v).toBe("19.9 km");
    expect(km[2].v).toBe("4.7 km/kWh");
    expect(slotStats(base(driving))).toEqual([]);
  });
  it("has no slot without the big figure or in other states", () => {
    expect(slotAvailable({ kind: "charging", powerKw: null } as unknown as NowView)).toBe(false);
    expect(slotAvailable({ kind: "parked" } as unknown as NowView)).toBe(false);
    expect(slotAvailable(charging)).toBe(true);
  });
});
