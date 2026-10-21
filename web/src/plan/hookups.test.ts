import { describe, expect, it } from "vitest";
import { HOOKUPS, clampKw, hookupById, hookupForKw, stayLine } from "./hookups";

describe("hookups", () => {
  it("every wording names Rivian's Portable Charger, never a mobile charger or connector", () => {
    const text = JSON.stringify(HOOKUPS);
    expect(text).toMatch(/Rivian Portable Charger/);
    expect(text).not.toMatch(/mobile (charger|connector)/i);
  });
  it("the 14-50 note says to carry it, with the 7.6 kW cap and the 7.2 kW plan", () => {
    const n = hookupById("nema_14_50")!.note;
    expect(n).toContain("Up to 7.6 kW with Rivian's Portable Charger; planned at 7.2 kW (estimate) for campground voltage. You need to carry the Portable Charger.");
  });
  it("the power is kept between 1.0 and 11.5 kW", () => {
    expect(clampKw(0.2)).toBe(1);
    expect(clampKw(20)).toBe(11.5);
    expect(clampKw(7.24)).toBe(7.2);
    expect(clampKw(NaN)).toBe(7.2);
  });
  it("an old save, known only by kW, maps to the nearest preset", () => {
    expect(hookupForKw(0)).toBe("none");
    expect(hookupForKw(7)).toBe("j1772");
    expect(hookupForKw(1.4)).toBe("outlet_120");
    expect(hookupForKw(2.9)).toBe("tt_30");
  });
  it("the overnight line says where, which hookup and that the kW is an estimate", () => {
    expect(stayLine("Mono Lake campground", "nema_14_50", 7.2)).toBe("Overnight at Mono Lake campground, NEMA 14-50 with the Rivian Portable Charger, about 7.2 kW (estimate)");
    expect(stayLine("A", "none", 0)).toBe("Overnight at A");
  });
});
