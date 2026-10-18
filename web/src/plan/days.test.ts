import { describe, expect, it } from "vitest";
import { buildDays } from "./days";

const S = (n: string, lat = 0): { lat: number; lon: number; label: string } => ({ lat, lon: -100, label: n });
const O = { l2Kw: 7, until: "08:00", maxSocPct: 80 };
const dest = S("Home", 50);

describe("buildDays", () => {
  it("one overnight: two days, the overnight ends day 1", () => {
    const d = buildDays([S("Hotel", 10)], [true], dest, () => O);
    expect(d).toHaveLength(2);
    expect(d[0]).toMatchObject({ end: { lat: 10, label: "Hotel" }, overnight: { l2_kw: 7, until: "08:00", max_soc_pct: 80, name: "Hotel" } });
    expect(d[0].vias).toBeUndefined();
    expect(d[1]).toMatchObject({ end: { lat: 50, label: "Home" } });
    expect(d[1].overnight).toBeUndefined();
  });

  it("keeps the plain stops: they become the vias of the day they fall in (no longer dropped)", () => {
    const stops = [S("Lunch", 5), S("Hotel", 10), S("Coffee", 20), S("Hotel 2", 30), S("Fuel", 40)];
    const d = buildDays(stops, [false, true, false, true, false], dest, () => O);
    expect(d).toHaveLength(3);
    expect(d[0].vias?.map((v) => v.label)).toEqual(["Lunch"]);
    expect(d[0].end.label).toBe("Hotel");
    expect(d[1].vias?.map((v) => v.label)).toEqual(["Coffee"]);
    expect(d[1].end.label).toBe("Hotel 2");
    expect(d[2].vias?.map((v) => v.label)).toEqual(["Fuel"]);
    expect(d[2].end.label).toBe("Home");
  });

  it("the overnight options and the reserve are sent per day; no charger is an explicit 0", () => {
    const d = buildDays([S("Hotel", 10)], [true], dest, () => ({ l2Kw: 0, until: "07:30", maxSocPct: 90 }), 15);
    expect(d[0].overnight).toMatchObject({ l2_kw: 0, until: "07:30", max_soc_pct: 90 });
    expect(d.map((x) => x.target_arrival_soc_pct)).toEqual([15, 15]);
    expect(buildDays([S("Hotel", 10)], [true], dest, () => O).every((x) => x.target_arrival_soc_pct === undefined)).toBe(true);
  });

  it("with no overnight at all it is one day", () => {
    const d = buildDays([S("Lunch", 5)], [false], dest, () => O);
    expect(d).toHaveLength(1);
    expect(d[0].vias?.map((v) => v.label)).toEqual(["Lunch"]);
  });

  it("gives each night its own settings", () => {
    const stops = [S("A", 10), S("B", 20)];
    const days = buildDays(stops, [true, true], dest, (x) => (x.label === "A" ? { l2Kw: 7, until: "08:00", maxSocPct: 80 } : { l2Kw: 0, until: "06:30", maxSocPct: 100 }));
    expect(days[0].overnight).toMatchObject({ l2_kw: 7, until: "08:00", max_soc_pct: 80 });
    expect(days[1].overnight).toMatchObject({ l2_kw: 0, until: "06:30", max_soc_pct: 100 });
  });
});
