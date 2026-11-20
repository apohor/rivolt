import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { styleURLs } from "./style";

const ORIGIN = "https://rivolt.test";

// A short drive and a long one, as the server's trimmed postcard route would carry them (polyline6).
function enc(points: [number, number][]): string {
  let prevLat = 0;
  let prevLon = 0;
  let out = "";
  const put = (v: number) => {
    let n = v < 0 ? ~(v << 1) : v << 1;
    while (n >= 0x20) {
      out += String.fromCharCode((0x20 | (n & 0x1f)) + 63);
      n >>= 5;
    }
    out += String.fromCharCode(n + 63);
  };
  for (const [lat, lon] of points) {
    const la = Math.round(lat * 1e6);
    const lo = Math.round(lon * 1e6);
    put(la - prevLat);
    put(lo - prevLon);
    prevLat = la;
    prevLon = lo;
  }
  return out;
}

describe("shareBasemapStyle", () => {
  it("has no symbol layer, no glyphs, no sprite, no terrain, and reads only our own tile route", async () => {
    const { shareBasemapStyle } = await import("./shareBasemap");
    const style = shareBasemapStyle(ORIGIN, "/api/maps/tiles/us.pmtiles");
    expect(style.layers.length).toBeGreaterThan(0);
    for (const l of style.layers) expect(l.type, l.id).not.toBe("symbol");
    expect(style.glyphs).toBeUndefined();
    expect(style.sprite).toBeUndefined();
    expect(Object.keys(style.sources)).toEqual(["protomaps"]);
    expect(style.layers.some((l) => l.type === "hillshade" || l.type === "raster")).toBe(false);
    const urls = styleURLs(style);
    expect(urls).toEqual([`pmtiles://${ORIGIN}/api/maps/tiles/us.pmtiles`]);
  });
});

describe("renderShareBasemap", () => {
  beforeEach(() => vi.resetModules());
  afterEach(() => {
    vi.resetModules();
    vi.doUnmock("./loader");
    vi.doUnmock("./webgl");
    vi.doUnmock("../lib/config");
    document.body.innerHTML = "";
  });

  async function run(segments: string[]) {
    let options: Record<string, unknown> | undefined;
    class FakeMap {
      constructor(o: Record<string, unknown>) {
        options = o;
        const c = document.createElement("canvas");
        c.width = 20;
        c.height = 10;
        this.canvas = c;
      }
      canvas: HTMLCanvasElement;
      on() {}
      once(_e: string, f: () => void) {
        queueMicrotask(f);
      }
      getCanvas() {
        return this.canvas;
      }
      project([lon, lat]: [number, number]) {
        return { x: lon, y: lat };
      }
      remove() {}
    }
    vi.doMock("./loader", () => ({ loadMapLibre: async () => ({ Map: FakeMap }) }));
    vi.doMock("./webgl", () => ({ webglAvailable: () => true }));
    vi.doMock("../lib/config", () => ({ tilesPMTilesURL: () => "/api/maps/tiles/us.pmtiles" }));
    // jsdom has no 2d canvas; the picture copy is not what is under test.
    HTMLCanvasElement.prototype.getContext = (() => ({ drawImage() {} })) as never;
    const { renderShareBasemap } = await import("./shareBasemap");
    const out = await renderShareBasemap(segments, 920, 300);
    return { out, options: options! };
  }

  for (const [name, pts] of [
    ["a short drive", [[34.0, -118.0], [34.01, -118.01]]],
    ["a long drive", [[34.0, -118.0], [36.0, -115.0], [37.5, -112.0]]],
  ] as [string, [number, number][]][]) {
    it(`${name}: fitted with 10% padding, never past zoom 12, no controls, no markers, no network but the style's`, async () => {
      const { out, options } = await run([enc(pts)]);
      expect(out).not.toBeNull();
      expect(options.fitBoundsOptions.maxZoom).toBe(12);
      const pad = options.fitBoundsOptions.padding;
      expect(pad.left).toBeGreaterThanOrEqual(920 * 0.1);
      expect(pad.right).toBeGreaterThanOrEqual(920 * 0.1);
      expect(pad.top).toBeGreaterThanOrEqual(300 * 0.1);
      expect(pad.bottom).toBeGreaterThanOrEqual(300 * 0.1);
      expect(options.interactive).toBe(false);
      expect(options.attributionControl).toBe(false);
      expect(options.transformRequest).toBeUndefined();
      expect(options.style.layers.some((l: { type: string }) => l.type === "symbol")).toBe(false);
      expect(document.body.children.length).toBe(0); // the off-screen host is gone
    });
  }

  it("keeps a gap between segments: two lines, never joined", async () => {
    const { out } = await run([enc([[34, -118], [34.1, -118.1]]), enc([[34.3, -118.3], [34.4, -118.4]])]);
    expect(out?.lines.length).toBe(2);
  });

  it("returns null with no tile archive", async () => {
    vi.doMock("../lib/config", () => ({ tilesPMTilesURL: () => "" }));
    vi.doMock("./webgl", () => ({ webglAvailable: () => true }));
    const { renderShareBasemap } = await import("./shareBasemap");
    expect(await renderShareBasemap([enc([[1, 1], [2, 2]])], 100, 100)).toBeNull();
  });
});
