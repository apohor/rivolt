import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import NowSlot, { asleepFigures, pickFix, slotAvailable, slotShown, slotView, slotStats, weatherView, type SlotInput } from "./NowSlot";
import type { NowView } from "../lib/nowState";

const charging = { kind: "charging", powerKw: 7.4, etaHours: 1.2, etaSource: "truck", limitPct: 80, energyKwh: 18.24, chargeFlavor: "home_l2", speedMph: null } as unknown as NowView;
const driving = { kind: "driving", speedMph: 62, powerKw: null } as unknown as NowView;
const session = { range_added_km: 98, total_charged_energy_kwh: 18.24, time_elapsed_seconds: 3900, kilometers_charged_per_hour: 61 };
const drive = { tripMi: 12.34, elapsed_sec: 1500, energy_used_kwh: 4.2, effMiPerKwh: 12.34 / 4.2 };
const base = (v: NowView, extra: Partial<SlotInput> = {}): SlotInput => ({ view: v, nowMs: Date.UTC(2026, 0, 1, 12), unit: "mi", currency: "USD", ...extra });

describe("NowSlot", () => {
  it("charging shows power, charger, the limit, the session and the priced cost", () => {
    render(<NowSlot {...base(charging, { session, charge: { started_at: "2026-01-01T11:00:00Z", cost: 2.91, currency: "USD", cost_source: "home_rate", cost_is_estimate: true } })} />);
    expect(screen.getByTestId("now-slot-big").textContent).toContain("7.4");
    expect(screen.getByTestId("now-slot-charger").textContent).toBe("Home charger");
    expect(screen.getByTestId("now-slot-sub").textContent).toMatch(/^Reaches 80% at /);
    expect(screen.getByTestId("now-slot-small").textContent).toBe("1 h 12 min left · target 80%");
    expect(screen.getByTestId("now-slot-added").textContent).toContain("+18.2 kWh");
    expect(screen.getByTestId("now-slot-range-added").textContent).toContain("+60.9 mi");
    expect(screen.getByTestId("now-slot-elapsed").textContent).toContain("1 h 05");
    expect(screen.getByTestId("now-slot-cost-so-far").textContent).toMatch(/≈ .*2\.91.*at your home rate/);
    expect(screen.getByTestId("now-slot-plugged-in")).toBeTruthy();
  });
  it("the time left rounds up to the whole minute, as the iPhone's", () => {
    const left = (hours: number, etaSource: string) => {
      const { container, unmount } = render(<NowSlot {...base({ ...charging, etaHours: hours, etaSource } as unknown as NowView)} />);
      const t = container.querySelector('[data-testid="now-slot-small"]')?.textContent;
      unmount();
      return t;
    };
    expect(left(0.4878, "estimate")).toBe("30 min left (est.) · target 80%");
    expect(left(29 / 60, "truck")).toBe("29 min left · target 80%");
  });
  it("a billed cost has no estimate mark; no cost shows no cost; no session falls back to the estimate", () => {
    const billed = slotStats(base(charging, { charge: { started_at: "x", cost: 5, cost_source: "rivian", cost_is_estimate: false } }));
    expect(billed.find((s) => s.k === "Cost so far")).toMatchObject({ v: "$5.00", note: "billed by Rivian" });
    const none = slotStats(base(charging, { charge: { started_at: "x", cost: null } }));
    expect(none.map((s) => s.k)).toEqual(["Added"]);
    expect(none[0].v).toBe("≈ +18.2 kWh");
  });
  it("without a session figure (or a zero) range added and rate are the battery's gain, marked ≈", () => {
    const v = { ...charging, soc: 60, rangeMi: 150 } as unknown as NowView;
    const charge = { started_at: "2026-01-01T11:00:00Z", soc_start: 40 };
    // 20 points of 60 at 150 mi: 50 mi, over the hour since the start.
    const est = slotStats(base(v, { charge }));
    const withPack = slotStats(base(v, { soft: true, packC: 30, tempUnit: "f", charge })).map((r) => r.k);
    expect(withPack).toContain("Pack temperature");
    expect(withPack.indexOf("Pack temperature")).toBe(withPack.indexOf("Rate") + 1);
    expect(slotStats(base(v, { soft: true, packC: null })).some((r) => r.k === "Pack temperature")).toBe(false);
    expect(slotStats(base(v, { packC: 30 })).some((r) => r.k === "Pack temperature")).toBe(false);

    expect(est.find((s) => s.k === "Range added")?.v).toBe("≈ +50.0 mi");
    expect(est.find((s) => s.k === "Rate")?.v).toBe("≈ 50.0 mi/h");
    const zero = slotStats(base(v, { charge, session: { ...session, range_added_km: 0, kilometers_charged_per_hour: 0 } }));
    expect(zero.find((s) => s.k === "Rate")?.v).toMatch(/^≈ /);
    // Under 5 min old there is no rate; the truck's own figures carry no mark.
    expect(slotStats(base(v, { charge: { ...charge, started_at: "2026-01-01T11:57:00Z" } })).some((s) => s.k === "Rate")).toBe(false);
    expect(slotStats(base(v, { charge, session })).find((s) => s.k === "Range added")?.v).toBe("+60.9 mi");
    expect(slotStats(base(charging, { charge: { started_at: "2026-01-01T11:00:00Z" } })).some((s) => s.k === "Range added")).toBe(false);
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

describe("parked slot weather", () => {
  it("shows only what the server sent", () => {
    expect(weatherView({ temp_c: 20 }, "c", "mi")).toEqual({ temp: "20°C", condition: null, icon: null, wind: null });
    const w = weatherView({ temp_c: 20, condition: "partly_cloudy", wind_kph: 16 }, "f", "mi");
    expect(w).toEqual({ temp: "68°F", condition: "Partly cloudy here now", icon: "partly_cloudy", wind: "Wind 10 mph" });
  });
  it("a clear sky is Sunny by day, Clear by night", () => {
    expect(weatherView({ temp_c: 20, condition: "clear" }, "c", "mi", true)).toMatchObject({ condition: "Sunny here now", icon: "clear" });
    expect(weatherView({ temp_c: 20, condition: "clear" }, "c", "mi", false)).toMatchObject({ condition: "Clear here now", icon: "night" });
  });
  it("a parked truck has a slot only with a fix", () => {
    const v = { kind: "parked" } as unknown as NowView;
    expect(slotAvailable(v)).toBe(false);
    expect(slotAvailable(v, { lat: 1, lon: 2 })).toBe(true);
  });
  it("a sleeping truck shows its last fix on the map like a parked one", () => {
    const v = { kind: "asleep" } as unknown as NowView;
    expect(slotAvailable(v)).toBe(false);
    expect(slotAvailable(v, { lat: 1, lon: 2 })).toBe(true);
  });
  it("the map's fix falls back to the server's last recorded position when the live state has none", () => {
    const last = { lat: 3, lon: 4 };
    expect(pickFix(undefined, 0, 0, last)).toEqual(last);
    expect(pickFix(undefined, Number.NaN, 5, last)).toEqual(last);
    expect(pickFix(undefined, 1, 2, last)).toEqual({ lat: 1, lon: 2 });
    expect(pickFix({ lat: 9, lon: 8 }, 1, 2, last)).toEqual({ lat: 9, lon: 8 });
    expect(pickFix(undefined, 0, 0, null)).toBeNull();
  });
  it("a sleeping truck with only a last fix shows the map and the two sleep figures", () => {
    const v = { kind: "asleep" } as unknown as NowView;
    const fix = pickFix(undefined, 0, 0, { lat: 3, lon: 4 });
    const { container } = render(<NowSlot {...base(v, { fix, asleep: { since: "2026-01-01T09:45:00Z", todayS: 18 * 3600 + 40 * 60 } })} />);
    const q = (id: string) => container.querySelector(`[data-testid="${id}"]`);
    expect(q("now-slot")?.getAttribute("data-kind")).toBe("asleep");
    expect(q("now-slot-asleep-for")?.textContent).toContain("2 h 15 min");
    expect(q("now-slot-asleep-today")?.textContent).toContain("18 h 40 min");
  });
  it("sleep figures show only what the server sent, and only while asleep", () => {
    const now = Date.UTC(2026, 0, 1, 12);
    expect(asleepFigures(null, now)).toEqual([]);
    expect(asleepFigures({ since: null, todayS: 3600 }, now)).toEqual([{ k: "Asleep today", v: "1 h 00 min" }]);
    const { container } = render(<NowSlot {...base({ kind: "parked" } as unknown as NowView, { fix: { lat: 1, lon: 2 }, asleep: { since: "2026-01-01T09:45:00Z", todayS: 60 } })} />);
    expect(container.querySelector('[data-testid="now-slot-asleep"]')).toBeNull();
  });
  it("a sleeping truck keeps its slot past the stale and offline limits; an offline truck that is not asleep keeps the picture", () => {
    const fix = { lat: 1, lon: 2 };
    const asleep = (extra: object) => ({ kind: "asleep", sleeping: true, stale: false, ...extra }) as unknown as NowView;
    expect(slotShown(asleep({}), fix)).toBe(true);
    // about 31 min: stale
    expect(slotShown(asleep({ stale: true, ageSec: 31 * 60 }), fix)).toBe(true);
    // about 3 h: the view reads offline, but the newest frame says asleep
    const long = { kind: "offline", sleeping: true, stale: false, ageSec: 3 * 3600 } as unknown as NowView;
    expect(slotShown(long, fix)).toBe(true);
    expect(slotView(long).kind).toBe("asleep");
    expect(slotShown(long, null)).toBe(false);
    // plugged in and asleep, stale
    expect(slotShown({ kind: "plugged", sleeping: true, stale: true } as unknown as NowView, fix)).toBe(true);
    // not asleep: stale or offline hides it, as before
    expect(slotShown({ kind: "offline", sleeping: false, stale: false } as unknown as NowView, fix)).toBe(false);
    expect(slotShown({ kind: "parked", sleeping: false, stale: true } as unknown as NowView, fix)).toBe(false);
    expect(slotShown({ kind: "plugged", sleeping: false, stale: true } as unknown as NowView, fix)).toBe(false);
    // charging and driving are unchanged
    expect(slotShown({ ...charging, stale: true } as unknown as NowView, fix)).toBe(false);
    expect(slotShown({ ...driving, kind: "offline", stale: false } as unknown as NowView, fix)).toBe(false);
    expect(slotShown({ ...charging, stale: false } as unknown as NowView, null)).toBe(true);
  });
  it("without a fix nothing renders", () => {
    const { container } = render(<NowSlot {...base({ kind: "parked" } as unknown as NowView)} />);
    expect(container.firstChild).toBeNull();
  });
});
