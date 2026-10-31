import { describe, expect, it } from "vitest";
import { buildStyle, plainStyle, styleURLs, TERRAIN_ATTRIBUTION, type StyleInput } from "./style";

const ORIGIN = "https://rivolt.test";
const input = (over: Partial<StyleInput> = {}): StyleInput => ({
  flavor: "dark",
  origin: ORIGIN,
  tilesPath: "/api/maps/tiles/us.pmtiles",
  glyphsPath: "/api/maps/glyphs/{fontstack}/{range}.pbf",
  dem: { url: "/api/maps/dem/{z}/{x}/{y}.png", minZoom: 3, maxZoom: 10 },
  ...over,
});

describe("buildStyle", () => {
  for (const flavor of ["dark", "light"] as const) {
    it(`${flavor}: every URL is on the page's own origin, with no sprite and no icon`, () => {
      const style = buildStyle(input({ flavor }));
      const urls = styleURLs(style);
      expect(urls.length).toBe(3); // glyphs, the archive, the terrain tiles
      for (const u of urls) {
        // pmtiles://<origin>/path or <origin>/path, never another host
        const bare = u.replace(/^pmtiles:\/\//, "");
        expect(bare.startsWith(ORIGIN + "/"), u).toBe(true);
      }
      expect(style.sprite).toBeUndefined();
      for (const l of style.layers) {
        const layout = ((l as { layout?: Record<string, unknown> }).layout ?? {}) as Record<string, unknown>;
        for (const k of Object.keys(layout)) expect(k.startsWith("icon-"), `${l.id}: ${k}`).toBe(false);
      }
      // The serialised style names no other host at all.
      const text = JSON.stringify(style);
      const hosts = new Set([...text.matchAll(/https?:\/\/([^/"'<>\s\\]+)/g)].map((m) => m[1]));
      // protomaps.com and openstreetmap.org appear only as links inside the attribution text, never as something fetched.
      for (const h of hosts) expect([ORIGIN.replace("https://", ""), "protomaps.com", "openstreetmap.org"], h).toContain(h);
    });
  }

  it("draws the hillshade, and credits the terrain, only when terrain is advertised", () => {
    const withDEM = buildStyle(input());
    expect(withDEM.layers.some((l) => l.id === "hillshade")).toBe(true);
    expect((withDEM.sources.dem as { attribution?: string }).attribution).toBe(TERRAIN_ATTRIBUTION);
    expect(JSON.stringify(withDEM)).toContain("USGS");

    const without = buildStyle(input({ dem: null }));
    expect(without.layers.some((l) => l.id === "hillshade")).toBe(false);
    expect(without.sources.dem).toBeUndefined();
    expect(styleURLs(without).length).toBe(2);
    expect(JSON.stringify(without)).not.toContain("Terrain:");
  });

  it("puts the hillshade above the land and under the water", () => {
    const ids = buildStyle(input()).layers.map((l) => l.id);
    expect(ids.indexOf("hillshade")).toBeGreaterThan(0);
    expect(ids.indexOf("hillshade")).toBeLessThan(ids.indexOf("water"));
  });

  it("is quiet: no POIs, buildings, shields or minor road names, and towns only", () => {
    const style = buildStyle(input());
    const ids = style.layers.map((l) => l.id);
    for (const dropped of ["pois", "buildings", "roads_shields", "address_label", "roads_labels_minor"]) {
      expect(ids.some((id) => id.startsWith(dropped)), dropped).toBe(false);
    }
    const loc = style.layers.find((l) => l.id === "places_locality") as { filter?: unknown } | undefined;
    expect(JSON.stringify(loc?.filter)).toContain("population_rank");
  });

  it("without glyphs it has no label layers and fetches no font", () => {
    const s = buildStyle(input({ glyphsPath: "" }));
    expect(s.glyphs).toBeUndefined();
    expect(s.layers.some((l) => l.type === "symbol")).toBe(false);
    expect(styleURLs(s).every((u) => !u.includes("{fontstack}"))).toBe(true);
  });

  it("the plain style has a background only and no URL at all", () => {
    for (const f of ["dark", "light"] as const) {
      const p = plainStyle(f);
      expect(styleURLs(p)).toEqual([]);
      expect(p.layers).toHaveLength(1);
    }
  });

  it("dark and light differ, and the terrain zooms come from the server", () => {
    const d = buildStyle(input({ flavor: "dark" }));
    const l = buildStyle(input({ flavor: "light" }));
    expect(JSON.stringify(d.layers)).not.toEqual(JSON.stringify(l.layers));
    const dem = buildStyle(input({ dem: { url: "/api/maps/dem/{z}/{x}/{y}.png", minZoom: 4, maxZoom: 9 } })).sources.dem as { minzoom: number; maxzoom: number };
    expect([dem.minzoom, dem.maxzoom]).toEqual([4, 9]);
  });
});
