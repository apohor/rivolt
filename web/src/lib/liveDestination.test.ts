import { describe, expect, it } from "vitest";
import { destinationLine, destinationRoute, type LiveDestination } from "./liveDestination";

// Plainly fake places near (10, 10). The polyline is precision 6, as the endpoint serves it.
function encode6(pts: [number, number][]): string {
  let out = "";
  let pl = 0;
  let pn = 0;
  const put = (v: number) => {
    let u = v < 0 ? ~(v << 1) : v << 1;
    while (u >= 0x20) {
      out += String.fromCharCode((0x20 | (u & 0x1f)) + 63);
      u >>= 5;
    }
    out += String.fromCharCode(u + 63);
  };
  for (const [la, lo] of pts) {
    const a = Math.round(la * 1e6);
    const b = Math.round(lo * 1e6);
    put(a - pl);
    put(b - pn);
    pl = a;
    pn = b;
  }
  return out;
}

const base: LiveDestination = { name: "Work", lat: 10.01, lon: 10.02, eta: "2026-11-16T17:40:00Z", miles_left: 12.4, route_precision: 6 };

describe("destinationLine", () => {
  it("says miles left and the arrival time", () => {
    const s = destinationLine(base, "mi");
    expect(s).toMatch(/^To Work: 12 mi left, arrive \d{1,2}:\d{2}/);
  });
  it("converts to km for the metric setting", () => {
    expect(destinationLine({ ...base, miles_left: 10 }, "km")).toContain("16 km left");
    expect(destinationLine(base, "km")).not.toContain(" mi");
  });
  it("keeps a decimal under ten", () => {
    expect(destinationLine({ ...base, miles_left: 4.26 }, "mi")).toContain("4.3 mi left");
  });
  it("leaves the distance out when the car does not know it", () => {
    const s = destinationLine({ ...base, miles_left: undefined }, "mi");
    expect(s).toMatch(/^To Work: arrive /);
    expect(s).not.toContain("left");
  });
  it("is just the name when the time is unreadable", () => {
    expect(destinationLine({ ...base, miles_left: undefined, eta: "soon" }, "mi")).toBe("To Work");
  });
});

describe("destinationRoute", () => {
  it("decodes the precision-6 line", () => {
    const pts: [number, number][] = [[10, 10], [10.0123, 10.0456], [10.02, 10.03]];
    const got = destinationRoute({ ...base, route: encode6(pts) });
    expect(got).toHaveLength(3);
    got.forEach((p, i) => { expect(p[0]).toBeCloseTo(pts[i][0], 6); expect(p[1]).toBeCloseTo(pts[i][1], 6); });
  });
  it("is empty without a route", () => {
    expect(destinationRoute(base)).toEqual([]);
  });
});
