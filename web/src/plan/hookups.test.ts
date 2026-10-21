import { describe, expect, it } from "vitest";
import { HOOKUPS, clampKw, hookupById, hookupForKw, hookupNote, stayLine } from "./hookups";

describe("hookups", () => {
  it("has the five choices, with the Portable Charger's name", () => {
    expect(HOOKUPS.map((h) => h.id)).toEqual(["j1772", "nema_14_50", "tt_30", "outlet_120", "none"]);
    expect(hookupById("nema_14_50")!.label).toContain("Rivian Portable Charger");
  });
  it("the 14-50 note says to carry it, with the 7.6 kW cap and the 7.2 kW plan", () => {
    expect(hookupNote("nema_14_50", 7.2)).toBe(
      "Up to 7.6 kW with Rivian's Portable Charger; planned at 7.2 kW (estimate) for campground voltage. You need to carry the Portable Charger.",
    );
  });
  it("J1772 says the power varies, planned at the shown kW", () => {
    expect(hookupNote("j1772", 7)).toBe("The station's power varies; planned at 7 kW (estimate).");
  });
  it("TT-30 needs an adapter and plans at 1.4 kW", () => {
    expect(hookupById("tt_30")!.kw).toBe(1.4);
    expect(hookupNote("tt_30", 1.4)).toContain("needs an adapter not included with the truck");
  });
  it("keeps the power inside the hookup's range (the server rejects outside it)", () => {
    expect(clampKw(0.2, "j1772")).toBe(1);
    expect(clampKw(20, "j1772")).toBe(11.5);
    expect(clampKw(9, "nema_14_50")).toBe(7.6);
    expect(clampKw(7.24, "nema_14_50")).toBe(7.2);
    expect(clampKw(5, "tt_30")).toBe(1.4);
    expect(clampKw(5, "none")).toBe(0);
    expect(clampKw(NaN, "nema_14_50")).toBe(7.2);
  });
  it("hotel stops without a hookup keep 0 to 19.2 kW", () => {
    expect(clampKw(19, undefined)).toBe(19);
    expect(clampKw(25, undefined)).toBe(19.2);
  });
  it("a power with no hookup maps to the nearest preset", () => {
    expect(hookupForKw(0)).toBe("none");
    expect(hookupForKw(7)).toBe("j1772");
    expect(hookupForKw(1.4)).toBe("outlet_120");
  });
  it("the overnight line names the hookup and calls the power an estimate", () => {
    expect(stayLine("Mono Lake campground", "nema_14_50", 7.2)).toBe("Overnight at Mono Lake campground, NEMA 14-50 with the Rivian Portable Charger, about 7.2 kW (estimate)");
    expect(stayLine("X", "none", 0)).toBe("Overnight at X");
  });
});
