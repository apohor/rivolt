import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import postcss from "postcss";
import { describe, expect, it } from "vitest";
// @ts-expect-error: a build-time plugin written as plain ESM
import prune, { KEEP, UNUSED_CONTROLS } from "../../scripts/postcss-prune-maplibre.mjs";

const run = (css: string, from: string) => postcss([prune()]).process(css, { from }).css;

const classes = (css: string) => new Set(css.match(/\.maplibregl-[a-z0-9-]+/g) ?? []);

describe("pruning the MapLibre stylesheet", () => {
  const fixture = `
    .maplibregl-ctrl-group { border-radius: 4px; }
    .maplibregl-ctrl button.maplibregl-ctrl-zoom-in .maplibregl-ctrl-icon { background-image: url("zoom-in"); }
    .maplibregl-ctrl-attrib.maplibregl-compact { margin: 0; }
    .maplibregl-popup-content { padding: 10px; }
    .maplibregl-marker { position: absolute; }
    a.maplibregl-ctrl-logo { background-image: url("data:image/svg+xml;charset=utf-8,<svg></svg>"); }
    .maplibregl-ctrl button.maplibregl-ctrl-geolocate .maplibregl-ctrl-icon { background-image: url("geo"); }
    .maplibregl-ctrl button.maplibregl-ctrl-fullscreen .maplibregl-ctrl-icon { background-image: url("fs"); }
    .maplibregl-ctrl button.maplibregl-ctrl-globe-enabled .maplibregl-ctrl-icon { background-image: url("globe"); }
    .maplibregl-ctrl button.maplibregl-ctrl-terrain .maplibregl-ctrl-icon { background-image: url("terrain"); }
    .maplibregl-ctrl-scale { border: 2px; }
    .maplibregl-ctrl button.maplibregl-ctrl-compass .maplibregl-ctrl-icon { background-image: url("compass"); }
    .maplibregl-user-location-dot, .maplibregl-user-location-dot::before { width: 15px; }
    .maplibregl-ctrl-zoom-out .maplibregl-ctrl-icon, .maplibregl-ctrl-shrink .maplibregl-ctrl-icon { color: red; }
    @media (forced-colors: active) { a.maplibregl-ctrl-logo { background: url("logo-hc"); } .maplibregl-ctrl-group { border: 1px; } }
    @media (forced-colors: active) { .maplibregl-ctrl-geolocate .maplibregl-ctrl-icon { fill: red; } }
  `;

  it("drops the controls Rivolt never creates and keeps the rest", () => {
    const out = run(fixture, "/x/node_modules/maplibre-gl/dist/maplibre-gl.css");
    for (const gone of ["ctrl-logo", "ctrl-geolocate", "ctrl-fullscreen", "ctrl-globe", "ctrl-terrain", "ctrl-scale", "ctrl-compass", "user-location", "ctrl-shrink"]) {
      expect(out, gone).not.toContain(gone);
    }
    for (const kept of ["ctrl-group", "ctrl-zoom-in", "ctrl-zoom-out", "ctrl-attrib", "popup-content", "maplibregl-marker"]) {
      expect(out, kept).toContain(kept);
    }
    // A selector list loses only its unused member, and a @media block with nothing left goes with them.
    expect(out).toMatch(/\.maplibregl-ctrl-zoom-out \.maplibregl-ctrl-icon\s*\{/);
    expect(out.match(/@media/g)?.length).toBe(1);
    expect(out).toContain("forced-colors");
    expect(out).toContain("border: 1px");
  });

  it("leaves every other stylesheet alone, even one that mentions a MapLibre class", () => {
    expect(run(fixture, "/x/src/index.css")).toBe(fixture);
    expect(run(fixture, "/x/src/maplibre-gl-theme.css.map")).toBe(fixture);
  });

  // The real stylesheet: only the unused controls' rules go (every other class survives), and it gets much smaller.
  it("on maplibre-gl's own stylesheet removes only the unused controls", () => {
    const file = join(process.cwd(), "node_modules/maplibre-gl/dist/maplibre-gl.css");
    if (!existsSync(file)) {
      // A fresh clone without the dependency installed; CI always has it, and there it must run.
      if (process.env.CI) throw new Error(`${file} is missing`);
      return;
    }
    const css = readFileSync(file, "utf8");
    const out = run(css, file);
    const before = classes(css);
    const after = classes(out);
    const unused = [...before].filter((c) => UNUSED_CONTROLS.test(c));
    expect(unused.length).toBeGreaterThan(5);
    for (const c of before) {
      if (UNUSED_CONTROLS.test(c)) expect(after.has(c), `${c} should be removed`).toBe(false);
      else expect(after.has(c), `${c} must survive`).toBe(true);
    }
    // The classes Rivolt's maps rely on (zoom buttons, attribution, popups, markers, the canvas).
    for (const c of [".maplibregl-ctrl-group", ".maplibregl-ctrl-zoom-in", ".maplibregl-ctrl-zoom-out", ".maplibregl-ctrl-attrib", ".maplibregl-popup", ".maplibregl-marker", ".maplibregl-canvas"]) {
      expect(after.has(c), c).toBe(true);
    }
    expect(out.length).toBeLessThan(css.length * 0.5);
  });

  // The credits are licence-required and foldCredits/map.css depend on the attribution control's structure: every rule that mentions the
  // attribution or the compact class survives the prune byte for byte, except the MapLibre logo's own compact rule.
  describe("the attribution rules", () => {
    const attribRules = (css: string) => {
      const out: string[] = [];
      postcss.parse(css).walkRules((r) => {
        if (/attrib|compact/.test(r.selector) && !/ctrl-logo/.test(r.selector)) out.push(`${r.parent?.type === "atrule" ? "@" + (r.parent as postcss.AtRule).name + " " + (r.parent as postcss.AtRule).params + " " : ""}${r.toString()}`);
      });
      return out;
    };

    it("are all kept, in the fixture", () => {
      const css = `
        .maplibregl-ctrl-attrib { background: rgba(255,255,255,.5); }
        .maplibregl-ctrl-attrib.maplibregl-compact { min-height: 20px; }
        .maplibregl-ctrl-attrib.maplibregl-compact-show .maplibregl-ctrl-attrib-inner { display: block; }
        .maplibregl-ctrl-attrib-button { background-image: url("i"); }
        @media (forced-colors: active) { .maplibregl-ctrl-attrib-button { background-image: url("hc"); } a.maplibregl-ctrl-logo { background: url("logo-hc"); } }
        a.maplibregl-ctrl-logo.maplibregl-compact { width: 14px; }
      `;
      const out = run(css, "/x/node_modules/maplibre-gl/dist/maplibre-gl.css");
      expect(attribRules(out)).toEqual(attribRules(css));
      expect(out).not.toContain("ctrl-logo");
      // Even a deny-list that is edited to be too broad cannot drop them.
      expect(KEEP(".maplibregl-ctrl-attrib-button")).toBe(true);
      expect(KEEP(".maplibregl-ctrl-attrib.maplibregl-compact-show .maplibregl-ctrl-attrib-inner")).toBe(true);
      expect(KEEP("a.maplibregl-ctrl-logo.maplibregl-compact")).toBe(false);
      expect(UNUSED_CONTROLS.test(".maplibregl-ctrl-attrib")).toBe(false);
    });

    it("are all kept, byte for byte, in maplibre-gl's own stylesheet", () => {
      const file = join(process.cwd(), "node_modules/maplibre-gl/dist/maplibre-gl.css");
      if (!existsSync(file)) {
        if (process.env.CI) throw new Error(`${file} is missing`);
        return;
      }
      const css = readFileSync(file, "utf8");
      const before = attribRules(css);
      expect(before.length).toBeGreaterThan(10);
      const after = new Set(attribRules(run(css, file)));
      for (const rule of before) expect(after.has(rule), `lost: ${rule.slice(0, 120)}`).toBe(true);
    });
  });
});
