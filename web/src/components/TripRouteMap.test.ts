import { describe, expect, it } from "vitest";
import { decodePolyline6 } from "./TripRouteMap";

// Encode (precision 6) the way Valhalla/the backend does, so the test pins
// the decoder to the stored-geometry format the demo presets use.
function encode(pts: [number, number][]): string {
  let out = "";
  let pl = 0;
  let po = 0;
  const enc = (v: number) => {
    let n = v < 0 ? ~(v << 1) : v << 1;
    let s = "";
    while (n >= 0x20) {
      s += String.fromCharCode((0x20 | (n & 0x1f)) + 63);
      n >>= 5;
    }
    return s + String.fromCharCode(n + 63);
  };
  for (const [la, lo] of pts) {
    const a = Math.round(la * 1e6);
    const b = Math.round(lo * 1e6);
    out += enc(a - pl) + enc(b - po);
    pl = a;
    po = b;
  }
  return out;
}

describe("decodePolyline6", () => {
  it("round-trips a precision-6 route", () => {
    const pts: [number, number][] = [
      [34.052235, -118.243683],
      [34.9, -117.02],
      [36.169941, -115.139832],
    ];
    const got = decodePolyline6(encode(pts));
    expect(got).toHaveLength(3);
    got.forEach(([la, lo], i) => {
      expect(la).toBeCloseTo(pts[i][0], 6);
      expect(lo).toBeCloseTo(pts[i][1], 6);
    });
  });
  it("returns an empty list for nothing", () => {
    expect(decodePolyline6("")).toEqual([]);
    expect(decodePolyline6(null)).toEqual([]);
  });
});
