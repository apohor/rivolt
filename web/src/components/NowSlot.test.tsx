import { describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach } from "vitest";
import NowSlot, { pickFix, slotAvailable, slotShown, slotView, slotStats, weatherView, type SlotInput } from "./NowSlot";
import type { NowView } from "../lib/nowState";
import { nowChip } from "./NowHeroV2";
import { clock } from "../lib/nowState";

const charging = { kind: "charging", powerKw: 7.4, etaHours: 1.2, etaSource: "truck", limitPct: 80, energyKwh: 18.24, chargeFlavor: "home_l2", speedMph: null } as unknown as NowView;
const driving = { kind: "driving", speedMph: 62, powerKw: null } as unknown as NowView;
const session = { range_added_km: 98, total_charged_energy_kwh: 18.24, time_elapsed_seconds: 3900, kilometers_charged_per_hour: 61 };
const drive = { tripMi: 12.34, elapsed_sec: 1500, energy_used_kwh: 4.2, effMiPerKwh: 12.34 / 4.2 };
const base = (v: NowView, extra: Partial<SlotInput> = {}): SlotInput => ({ view: v, nowMs: Date.UTC(2026, 0, 1, 12), unit: "mi", currency: "USD", ...extra });

afterEach(cleanup);

describe("one finish time", () => {
  // 30.006 minutes left: the slot says "31 min left", so the chip and "Reaches" both say 31 minutes from now, to the minute.
  it("the chip, the slot's Reaches line and the shown minutes name the same instant", () => {
    const nowMs = Date.UTC(2026, 0, 1, 12, 0, 20);
    const view = { ...charging, etaHours: 30.006 / 60 } as unknown as NowView;
    const { unmount } = render(<NowSlot {...base(view, { nowMs })} />);
    const when = clock(nowMs + 31 * 60_000);
    expect(screen.getByTestId("now-slot-small").textContent).toMatch(/^31 min left/);
    expect(screen.getByTestId("now-slot-sub").textContent).toBe(`Reaches 80% at ${when}`);
    expect(nowChip(view, null, nowMs, "", false)).toBe(`Charging · 7.4 kW · done by ${when}`);
    unmount();
  });
});

describe("NowSlot", () => {
  it("the truck's own Rivian-charger flag names the charger, as the Live page does", () => {
    render(<NowSlot {...base(charging, { session: { ...session, is_rivian_charger: true } })} />);
    expect(screen.getByTestId("now-slot-charger").textContent).toBe("Rivian charger");
  });
  it("charging shows power, charger, the limit, the session and the priced cost", () => {
    render(<NowSlot {...base(charging, { session, charge: { started_at: "2026-01-01T11:00:00Z", cost: 2.91, currency: "USD", cost_source: "home_rate", cost_is_estimate: true } })} />);
    expect(screen.getByTestId("now-slot-big").textContent).toContain("7.4");
    expect(screen.getByTestId("now-slot-charger").textContent).toBe("Home charger");
    expect(screen.getByTestId("now-slot-sub").textContent).toMatch(/^Reaches 80% at /);
    expect(screen.getByTestId("now-slot-small").textContent).toBe("1 h 12 min left · target 80%");
    expect(screen.getByTestId("now-slot-added").textContent).toContain("+18.2 kWh");
    expect(screen.getByTestId("now-slot-range-added").textContent).toContain("+60.9 mi");
    expect(screen.getByTestId("now-slot-elapsed").textContent).toContain("1 h 05");
    expect(screen.getByTestId("now-slot-cost-so-far").textContent).toMatch(/estimated .*2\.91.*estimate · at your home rate/);
    expect(screen.getByTestId("now-slot-added").textContent).not.toMatch(/≈|est/);
    expect(screen.getByTestId("now-slot-plugged-in")).toBeTruthy();
  });
  it("unkeyed card: no sign; one heading when every figure but the clock facts is ours, else a small est. on the derived cells", () => {
    const v = { ...charging, soc: 60, rangeMi: 150 } as unknown as NowView;
    const charge = { started_at: "2026-01-01T11:00:00Z", soc_start: 40, cost: 2.91, currency: "USD", cost_source: "home_rate", cost_is_estimate: true };
    const all = render(<NowSlot {...base(v, { charge })} />);
    expect(all.container.textContent).not.toMatch(/≈/);
    expect(screen.getByTestId("now-slot-estimated").textContent).toBe("Estimated so far");
    expect(screen.getByTestId("now-slot-range-added").textContent).toMatch(/estimated .*mi$/);
    expect(screen.getByTestId("now-slot-cost-so-far").textContent).toMatch(/estimate · at your home rate/);
    all.unmount();
    const mixed = render(<NowSlot {...base(v, { session: { total_charged_energy_kwh: 18.2, time_elapsed_seconds: 3900 }, charge })} />);
    expect(screen.queryByTestId("now-slot-estimated")).toBeNull();
    expect(screen.getByTestId("now-slot-range-added").textContent).toMatch(/estimated .*est\.$/);
    expect(screen.getByTestId("now-slot-added").textContent).not.toMatch(/est/);
    mixed.unmount();
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
    expect(none[0].v).toBe("+18.2 kWh");
  });
  it("without a session figure (or a zero) range added and rate are the battery's gain, marked est.", () => {
    const v = { ...charging, soc: 60, rangeMi: 150 } as unknown as NowView;
    const charge = { started_at: "2026-01-01T11:00:00Z", soc_start: 40 };
    // 20 points of 60 at 150 mi: 50 mi, over the hour since the start.
    const est = slotStats(base(v, { charge }));
    expect(est.find((s) => s.k === "Range added")?.v).toBe("+50.0 mi");
    expect(est.find((s) => s.k === "Rate")?.v).toBe("50.0 mi/h");
    const zero = slotStats(base(v, { charge, session: { ...session, range_added_km: 0, kilometers_charged_per_hour: 0 } }));
    expect(zero.find((s) => s.k === "Rate")?.v).toMatch(/^\d/);
    // Under 5 min old there is no rate; the truck's own figures carry no mark.
    expect(slotStats(base(v, { charge: { ...charge, started_at: "2026-01-01T11:57:00Z" } })).some((s) => s.k === "Rate")).toBe(false);
    expect(slotStats(base(v, { charge, session })).find((s) => s.k === "Range added")?.v).toBe("+60.9 mi");
    expect(slotStats(base(charging, { charge: { started_at: "2026-01-01T11:00:00Z" } })).some((s) => s.k === "Range added")).toBe(false);
  });
  describe("the keyed card's figures (no sign, one heading or small marks)", () => {
    const v = { ...charging, soc: 60, rangeMi: 150 } as unknown as NowView;
    const started = "2026-01-01T11:00:00Z";
    const figures = (extra: Partial<SlotInput>) => {
      render(<NowSlot {...base(v, { soft: true, ...extra })} />);
      return screen.getByTestId("now-slot-figures");
    };
    it("all estimated: one heading, no mark on any cell, no sign anywhere, the cost keeps estimate and its source, estimated for screen readers", () => {
      const el = figures({ charge: { started_at: started, soc_start: 40, cost: 2.91, currency: "USD", cost_source: "home_rate", cost_is_estimate: true } });
      expect(screen.getByTestId("now-slot-estimated").textContent).toBe("Estimated so far");
      expect(el.textContent).not.toMatch(/≈|est\./);
      expect(screen.getByTestId("now-slot-added").textContent).toMatch(/estimated \+18\.2 kWh/);
      expect(screen.getByTestId("now-slot-cost-so-far").textContent).toMatch(/estimated .*2\.91.*estimate · at your home rate/);
      expect(screen.getByTestId("now-slot-elapsed").textContent).not.toMatch(/estimated/);
    });
    it("mixed: no heading; derived cells carry est., the truck's figures none, the cost keeps the word", () => {
      figures({ session: { ...session, range_added_km: 98, kilometers_charged_per_hour: 0 }, charge: { started_at: started, soc_start: 40, cost: 2.91, currency: "USD", cost_source: "home_rate", cost_is_estimate: true } });
      expect(screen.queryByTestId("now-slot-estimated")).toBeNull();
      expect(screen.getByTestId("now-slot-range-added").textContent).not.toMatch(/est/);
      expect(screen.getByTestId("now-slot-rate").textContent).toMatch(/estimated .*est\.$/);
      expect(screen.getByTestId("now-slot-cost-so-far").textContent).toMatch(/estimated .*2\.91.*estimate · at your home rate/);
      expect(screen.getByTestId("now-slot-figures").textContent).not.toMatch(/≈/);
    });
    it("clock cells only (elapsed, plugged in): nothing is estimated, so no heading and no mark", () => {
      render(<NowSlot {...base({ ...v, energyKwh: null, rangeMi: null } as unknown as NowView, { soft: true, charge: { started_at: started } })} />);
      const el = screen.getByTestId("now-slot-figures");
      expect(screen.queryByTestId("now-slot-estimated")).toBeNull();
      expect(screen.getByTestId("now-slot-elapsed")).toBeTruthy();
      expect(el.textContent).not.toMatch(/≈|est|estimated/);
    });
    it("all exact: no heading and no mark", () => {
      const el = figures({ session, charge: { started_at: started, cost: 5, cost_source: "rivian", cost_is_estimate: false } });
      expect(screen.queryByTestId("now-slot-estimated")).toBeNull();
      expect(el.textContent).not.toMatch(/≈|est|estimated/);
    });
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

describe("the drive graph's frame before a line", () => {
  const started = Date.UTC(2026, 0, 1, 12);
  it("says the drive just started in its first minutes, and that there are no readings later", async () => {
    const { DriveGraph } = await import("./DriveGraph");
    const early = render(<DriveGraph series={null} startedMs={started} nowMs={started + 60_000} unit="mi" nowPct={70} />);
    expect(screen.getByTestId("now-drive-just-started").textContent).toBe("Drive just started");
    early.unmount();
    render(<DriveGraph series={null} startedMs={started} nowMs={started + 40 * 60_000} unit="mi" nowPct={70} />);
    expect(screen.getByTestId("now-drive-just-started").textContent).toBe("No speed readings yet");
  });
});
