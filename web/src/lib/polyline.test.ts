import { describe, expect, it } from "vitest";
import { decodePolyline } from "./polyline";

// Google's encoded-polyline format at precision 5, to build routes in tests (the test's own encoder). Plainly fake places near (10, 10).
function encode(pts: [number, number][]): string {
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
    const a = Math.round(la * 1e5);
    const b = Math.round(lo * 1e5);
    put(a - pl);
    put(b - pn);
    pl = a;
    pn = b;
  }
  return out;
}

describe("decodePolyline", () => {
  it("round-trips a route", () => {
    const pts: [number, number][] = [[10, 10], [10.001, 10.002], [-33.5, 151.25]];
    const got = decodePolyline(encode(pts));
    expect(got).toHaveLength(3);
    got.forEach((p, i) => {
      expect(p[0]).toBeCloseTo(pts[i][0], 5);
      expect(p[1]).toBeCloseTo(pts[i][1], 5);
    });
  });

  it("skips a stored point that is not on the globe and keeps the points after it right", () => {
    const withBad: [number, number][] = [[10, 10], [10.001, 10.001], [100.25, -970.1], [10.002, 10.002], [10.003, 10.003]];
    const got = decodePolyline(encode(withBad));
    expect(got).toHaveLength(4);
    expect(got.every(([la, lo]) => Math.abs(la) <= 90 && Math.abs(lo) <= 180)).toBe(true);
    expect(got[2][0]).toBeCloseTo(10.002, 5);
    expect(got[3][1]).toBeCloseTo(10.003, 5);
  });

  it("is empty for nothing", () => {
    expect(decodePolyline("")).toEqual([]);
    expect(decodePolyline(null)).toEqual([]);
  });
});
