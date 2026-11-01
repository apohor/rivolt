import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// The MapLibre stylesheet is pruned at build time (scripts/postcss-prune-maplibre.mjs): the rules of the MapLibre logo and of the scale,
// geolocate, fullscreen, terrain, globe and compass controls are gone. A map that creates one of those controls would get unstyled buttons.
// This fails the build if shipped code does, so the prune and the maps cannot drift apart silently.

const files = (dir: string): string[] =>
  readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    if (statSync(p).isDirectory()) return files(p);
    return /\.(ts|tsx)$/.test(f) && !/\.(test|pw)\.(ts|tsx)$/.test(f) ? [p] : [];
  });

const src = files(join(process.cwd(), "src")).map((f) => ({ f, text: readFileSync(f, "utf8") }));

describe("the maps only create controls whose CSS survives the prune", () => {
  it("creates only the NavigationControl and the AttributionControl", () => {
    const made = new Set<string>();
    for (const { text } of src) for (const m of text.matchAll(/new\s+(?:[A-Za-z_$][\w$]*\.)?(\w+Control)\s*\(/g)) made.add(m[1]);
    expect([...made].sort()).toEqual(["AttributionControl", "NavigationControl"]);
  });

  it("never shows the MapLibre logo", () => {
    for (const { f, text } of src) {
      expect(text, f).not.toMatch(/maplibreLogo\s*:\s*true/);
      expect(text, f).not.toMatch(/\blogoPosition\b/);
      expect(text, f).not.toMatch(/maplibregl-ctrl-logo/);
    }
  });

  it("creates every NavigationControl with showCompass: false (the compass button's CSS is pruned)", () => {
    let n = 0;
    for (const { f, text } of src) {
      for (const m of text.matchAll(/new\s+(?:[A-Za-z_$][\w$]*\.)?NavigationControl\s*\(([^)]*)\)/g)) {
        n++;
        expect(m[1], `${f}: ${m[0]}`).toMatch(/showCompass\s*:\s*false/);
        expect(m[1], `${f}: ${m[0]}`).not.toMatch(/visualizePitch\s*:\s*true/);
      }
    }
    expect(n, "the scan found no NavigationControl: it no longer sees the maps").toBeGreaterThanOrEqual(3);
  });
});
