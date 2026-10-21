import { describe, expect, it } from "vitest";
import { costCalc, fastPriceShown, workedExample } from "./chargingCost";

describe("worked example", () => {
  const w = workedExample();
  it("matches the page copy", () => {
    expect(w.kwh).toBe(435);
    expect(w.monthly).toBe(65);
    expect(w.centsPerMile).toBe(6.5);
    expect(w.coldKwh).toBe(556);
    expect(w.coldMonthly).toBe(83);
    expect(w.offpeakMonthly).toBe(44);
    expect(w.halfOffpeakMonthly).toBe(54);
    expect(w.fastStop).toBe(14);
    expect(w.fastStopAtHome).toBe(4);
    expect([w.yearKwh, w.yearHome, w.yearOffpeak]).toEqual([4350, 653, 435]);
    expect([w.fastYearKwh, w.fastYear]).toEqual([174, 84]);
  });
});

describe("fastPriceShown", () => {
  it("shows the tracker price for twelve months, then hides it", () => {
    expect(fastPriceShown(new Date("2026-10-20T12:00:00Z"))).toBe(true);
    expect(fastPriceShown(new Date("2027-10-14T12:00:00Z"))).toBe(true);
    expect(fastPriceShown(new Date("2027-10-16T12:00:00Z"))).toBe(false);
    expect(fastPriceShown(new Date("2026-10-20T12:00:00Z"), "bogus")).toBe(false);
  });
});

describe("costCalc", () => {
  const base = { ratePerKwh: 0.15, milesPerMonth: 1000, milesPerKwh: 2.3 };
  it("home only: cost and cents per mile", () => {
    const r = costCalc(base);
    expect(r.ok && Math.round(r.total * 100) / 100).toBe(65.22);
    expect(r.ok && Math.round(r.perMile * 1000) / 1000).toBe(0.065);
    expect(r.ok && r.saving).toBeNull();
  });
  it("an off-peak share lowers the home cost and reports the saving", () => {
    const r = costCalc({ ...base, offpeakRate: 0.1, offpeakSharePct: 100 });
    expect(r.ok && Math.round(r.total * 100) / 100).toBe(43.48);
    expect(r.ok && Math.round((r.saving ?? 0) * 100) / 100).toBe(21.74);
  });
  it("fast-charge energy is priced at its own price and not at the home rate", () => {
    const r = costCalc({ ...base, fastKwh: 100, fastPrice: 0.48 });
    expect(r.ok && Math.round(r.fastCost)).toBe(48);
    expect(r.ok && Math.round(r.homeCost * 100) / 100).toBe(50.22);
  });
  it("names what is missing or out of range", () => {
    expect(!costCalc({ ...base, ratePerKwh: NaN }).ok && (costCalc({ ...base, ratePerKwh: NaN }) as { errors: object }).errors).toHaveProperty("ratePerKwh");
    const e = (i: Parameters<typeof costCalc>[0]) => (costCalc(i) as { errors: Record<string, string> }).errors;
    expect(e({ ...base, milesPerKwh: 0 })).toHaveProperty("milesPerKwh");
    expect(e({ ...base, milesPerMonth: 0 })).toHaveProperty("milesPerMonth");
    expect(e({ ...base, offpeakRate: 0.1 })).toHaveProperty("offpeakSharePct");
    expect(e({ ...base, offpeakSharePct: 50 })).toHaveProperty("offpeakRate");
    expect(e({ ...base, offpeakRate: 0.1, offpeakSharePct: 101 })).toHaveProperty("offpeakSharePct");
    expect(e({ ...base, fastKwh: 20 })).toHaveProperty("fastPrice");
  });
});
