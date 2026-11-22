import { describe, expect, it } from "vitest";
import type { LiveSession } from "../lib/api";
import type { NowView } from "../lib/nowState";
import { allEstimated, freshCharge, liveStats, shownCharge, type SlotInput } from "./NowSlot";

const view = { kind: "charging", soc: 52, rangeMi: 200, powerKw: 11, etaHours: 1.5, etaSource: "estimate", energyKwh: 16.2, limitPct: 80, chargeFlavor: "home_l2" } as unknown as NowView;
const nowMs = Date.UTC(2026, 0, 1, 12);
const charge = { started_at: "2026-01-01T11:40:00Z", soc_start: 40, cost: 2.13, currency: "USD", cost_source: "rivian", cost_is_estimate: false };
const input = (extra: Partial<SlotInput> = {}): SlotInput => ({ view, nowMs, unit: "mi", currency: "USD", ...extra });
const ls = (o: Partial<LiveSession>) => ({ active: true, current_price: "", current_currency: "USD", is_free_session: false, ...o }) as LiveSession;
const full = { range_added_km: 80, total_charged_energy_kwh: 20, time_elapsed_seconds: 1200, kilometers_charged_per_hour: 240 };
const keys = (s: { k: string }[]) => s.map((x) => x.k);

describe("freshCharge", () => {
  it("Rivian's latest price replaces the charge row's snapshot, so Now and Live say the same cost", () => {
    const c = freshCharge(charge, ls({ current_price: "2.27" }));
    expect(c).toMatchObject({ cost: 2.27, currency: "USD", cost_source: "rivian", cost_is_estimate: false, started_at: charge.started_at, soc_start: 40 });
  });
  it("a zero price, a free session or no session leaves the charge as it was", () => {
    expect(freshCharge(charge, ls({ current_price: "0" }))).toBe(charge);
    expect(freshCharge(charge, ls({ current_price: "3", is_free_session: true }))).toBe(charge);
    expect(freshCharge(charge, null)).toBe(charge);
    expect(freshCharge(charge, ls({ current_price: "9.5", active: false }))).toBe(charge); // the previous plug-in's price, not this charge's
    expect(freshCharge(null, null)).toBeNull();
  });
  it("a price with no recorded charge still makes a priced charge", () => {
    expect(freshCharge(null, ls({ current_price: "2.27" }))).toMatchObject({ cost: 2.27, cost_source: "rivian" });
  });
});

describe("shownCharge", () => {
  const est = { started_at: "2026-01-01T11:40:00Z", cost: 25.92, currency: "USD", cost_source: "home_rate", cost_is_estimate: true };
  const rivian = (o: Partial<LiveSession> = {}) => ls({ is_rivian_charger: true, ...o });
  it("a home charge keeps its home-rate estimate", () => {
    expect(shownCharge(est, ls({ active: false }), "home_l2")).toBe(est);
    expect(shownCharge(est, null, "home_l2")).toBe(est);
  });
  it("a fast charge or AC away never shows a home-rate cost, with the session read pending, failed or answered", () => {
    for (const f of ["dcfc", "away_ac"] as const) for (const s of [null, rivian(), ls({})]) expect(shownCharge(est, s, f)?.cost).toBeUndefined();
  });
  it("the first minute of a fast charge (place and peak not known): no cost until the session says the charger is not a Rivian one", () => {
    expect(shownCharge(est, null, null)?.cost).toBeUndefined(); // read pending, or failed: no cost
    expect(shownCharge(est, rivian({ active: true }), null)?.cost).toBeUndefined();
    expect(shownCharge(est, ls({ active: false, is_rivian_charger: false }), null)).toBe(est); // a charge on someone else's charger at an unknown place keeps the old behaviour
  });
  it("Rivian's price wins over the gate at any flavor, and a non-estimated cost is left alone", () => {
    expect(shownCharge(est, rivian({ current_price: "9.5" }), "dcfc")).toMatchObject({ cost: 9.5, cost_source: "rivian", cost_is_estimate: false });
    expect(shownCharge(charge, null, "dcfc")).toBe(charge);
    expect(shownCharge(null, null, "dcfc")).toBeNull();
  });
  it("the card shows no Cost row for the gated charge", () => {
    const g = shownCharge(est, null, "dcfc");
    expect(keys(liveStats(input({ session: { ...full }, charge: g }), 1500))).not.toContain("Cost so far");
  });
});

describe("liveStats", () => {
  it("a Rivian session with every figure: the truck's own, none an estimate, time left from the truck", () => {
    const s = liveStats(input({ session: { ...full, ...{} }, charge }), 1500);
    expect(keys(s)).toEqual(["Added", "Range added", "Rate", "Elapsed", "Cost so far", "Remaining"]);
    expect(s.find((x) => x.k === "Remaining")).toMatchObject({ v: "25 min" });
    expect(s.some((x) => x.est)).toBe(false);
    expect(s.find((x) => x.k === "Cost so far")?.note).toBe("billed by Rivian");
  });
  it("a session with only a price: the other figures come from the truck's readings, estimated, the price is not", () => {
    const s = liveStats(input({ session: { range_added_km: 0, total_charged_energy_kwh: 0, time_elapsed_seconds: 0, kilometers_charged_per_hour: 0 }, charge: freshCharge(charge, ls({ current_price: "2.27" })) }), 0);
    expect(s.find((x) => x.k === "Added")).toMatchObject({ est: true, v: "+16.2 kWh" });
    expect(s.find((x) => x.k === "Elapsed")).toMatchObject({ v: "20 min" });
    expect(s.find((x) => x.k === "Elapsed")?.est).toBeFalsy();
    expect(s.find((x) => x.k === "Rate")?.est).toBe(true);
    expect(s.find((x) => x.k === "Remaining")).toMatchObject({ v: "1 h 30 min", est: true });
    expect(s.find((x) => x.k === "Cost so far")).toMatchObject({ v: "$2.27" });
    expect(allEstimated(s)).toBe(false);
  });
  it("no session at all (home AC): every worked-out figure is an estimate, so the group is all estimated", () => {
    const s = liveStats(input({ session: null, charge: { ...charge, cost: 1.8, cost_source: "home_rate", cost_is_estimate: true } }), 0);
    expect(keys(s)).toEqual(["Added", "Range added", "Rate", "Elapsed", "Cost so far", "Remaining"]);
    expect(allEstimated(s)).toBe(true);
    expect(s.find((x) => x.k === "Cost so far")?.note).toBe("estimate · at your home rate");
  });
  it("an estimated cost never renders without a source: a missing or unknown one says the rate is not known", () => {
    const note = (cost_source?: string) => liveStats(input({ session: null, charge: { ...charge, cost: 1.8, cost_source, cost_is_estimate: true } }), 0).find((x) => x.k === "Cost so far")?.note;
    expect(note(undefined)).toBe("estimate · rate not known");
    expect(note("something_new")).toBe("estimate · rate not known");
    expect(note("network_estimate")).toBe("estimate · network rate");
    expect(liveStats(input({ session: null, charge: { ...charge, cost_source: undefined, cost_is_estimate: false } }), 0).find((x) => x.k === "Cost so far")?.note).toBeUndefined();
  });
  it("a fast charge with no session and no recorded charge: only what the battery says, no Plugged in", () => {
    const s = liveStats(input({ session: null, charge: null, view: { ...view, chargeFlavor: "dcfc", etaHours: 0.4 } as unknown as NowView }), 0);
    expect(keys(s)).toEqual(["Added", "Remaining"]);
    expect(s[1]).toMatchObject({ v: "24 min", est: true });
  });
});
