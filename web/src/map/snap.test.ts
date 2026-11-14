import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../lib/config", () => ({
  ensureConfig: () => Promise.resolve(),
  valhallaBase: () => "/api/maps/valhalla",
}));

import { snapToRoads, type SnapPoint } from "./snap";

// Synthetic, not a recorded drive: a straight road east at ~13 m/s with a 60 s stop at a light halfway, one sample every 7 s.
const LAT0 = 40;
const LON0 = -100;
const M_PER_DEG_LON = 111320 * Math.cos((LAT0 * Math.PI) / 180);
function road(): SnapPoint[] {
  const pts: SnapPoint[] = [];
  let x = 0;
  let t = 1_000_000;
  for (let i = 0; i < 24; i++) {
    pts.push({ lat: LAT0, lon: LON0 + x / M_PER_DEG_LON, t, s: 30 });
    if (i === 11) t += 60; // the light: a dwell of a minute, the truck creeps 15 m
    else t += 7;
    x += i === 11 ? 15 : 90;
  }
  return pts;
}

function encode6(coords: [number, number][]): string {
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
  for (const [la, lo] of coords) {
    const a = Math.round(la * 1e6);
    const o = Math.round(lo * 1e6);
    out += enc(a - pl) + enc(o - po);
    pl = a;
    po = o;
  }
  return out;
}

let bodies: { shape: { lat: number; lon: number; time?: number }[] }[];
let reply: (shape: { lat: number; lon: number }[]) => [number, number][];

beforeEach(() => {
  bodies = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init: { body: string }) => {
      const b = JSON.parse(init.body);
      bodies.push(b);
      const geom = reply(b.shape);
      return {
        ok: true,
        json: async () => ({
          code: "Ok",
          matchings: [{ geometry: encode6(geom), confidence: 1 }],
          tracepoints: b.shape.map(() => ({ matchings_index: 0 })),
        }),
      };
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

describe("snapToRoads", () => {
  it("gives the matcher a time axis without the stop's dwell", async () => {
    reply = (shape) => shape.map((p) => [p.lat, p.lon]);
    await snapToRoads(road(), new AbortController().signal);
    const shape = bodies[0].shape;
    for (let i = 1; i < shape.length; i++) {
      const dt = (shape[i].time as number) - (shape[i - 1].time as number);
      const dLon = (shape[i].lon - shape[i - 1].lon) * M_PER_DEG_LON;
      // A minute spent 15 m further on is time the matcher could spend on a loop round the block.
      expect(dt > 10 && dLon < 30, `step ${i}: ${dt} s for ${dLon.toFixed(0)} m`).toBe(false);
    }
    expect(shape.length).toBe(24);
  });

  it("draws the recorded trace when the match invents a detour", async () => {
    // The matcher answers with the road plus a ~1 km loop into a side street.
    reply = (shape) => {
      const line = shape.map((p) => [p.lat, p.lon] as [number, number]);
      const at = Math.floor(line.length / 2);
      const [la, lo] = line[at];
      const loop: [number, number][] = [
        [la - 0.004, lo],
        [la - 0.004, lo + 0.004],
        [la, lo + 0.004],
      ];
      return [...line.slice(0, at + 1), ...loop, ...line.slice(at + 1)];
    };
    const plan = await snapToRoads(road(), new AbortController().signal);
    expect(plan).not.toBeNull();
    expect(plan!.segments.every((s) => s.raw)).toBe(true);
  });

  it("keeps a faithful match", async () => {
    reply = (shape) => shape.map((p) => [p.lat + 0.00002, p.lon]);
    const plan = await snapToRoads(road(), new AbortController().signal);
    expect(plan!.segments.some((s) => !s.raw)).toBe(true);
  });
});
