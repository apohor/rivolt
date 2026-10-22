// Writes the iPhone map styles (ios/Rivolt/Resources/Map/style-{light,dark}.json) from @protomaps/basemaps, the same
// package and flavors the web's drive map uses (web/src/components/DriveMap.tsx), with the web's boostContrast tweak.
// Run from web/ so the lockfile's version is used:  cd web && node ../ios/design/generate_map_style.mjs
//
// The styles reference nothing on the network but the signed-in server's tile archive: the source is
// "pmtiles://__RIVOLT_SERVER__/api/maps/tiles/us.pmtiles" (the app puts its server in), the glyphs are bundled
// (asset://Map/glyphs, Noto Sans from protomaps/basemaps-assets @ 028c18f713baecad011301ff7a69acc39bcc2ae7, OFL), and the icon layers are left
// out (no sprite in v1). ios/RivoltTests/MapStyleTests checks every URL in the result.
import { writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { layers, namedFlavor } = require("@protomaps/basemaps");
const version = require("@protomaps/basemaps/package.json").version;

// web/src/components/DriveMap.tsx boostContrast: building, park and landuse colours lifted so they read at street zoom.
function boostContrast(name, base) {
  if (name === "dark") {
    return { ...base, buildings: "#555555", park_a: "#1d3527", park_b: "#1d3527", wood_a: "#1d2d24", wood_b: "#1d2d24",
      pedestrian: "#2a2a2a", industrial: "#2c2a26", school: "#2c2828", hospital: "#2c2828" };
  }
  return { ...base, buildings: "#a8a8a8", park_a: "#cfe5cf", park_b: "#cfe5cf", pedestrian: "#dad7d2" };
}

const here = path.dirname(fileURLToPath(import.meta.url));
for (const name of ["light", "dark"]) {
  const flavor = boostContrast(name, namedFlavor(name));
  const style = {
    version: 8,
    name: `rivolt-${name}`,
    metadata: { generator: "ios/design/generate_map_style.mjs", "@protomaps/basemaps": version },
    glyphs: "asset://Map/glyphs/{fontstack}/{range}.pbf",
    sources: {
      protomaps: {
        type: "vector",
        url: "pmtiles://__RIVOLT_SERVER__/api/maps/tiles/us.pmtiles",
        attribution: "© OpenStreetMap contributors · Protomaps",
      },
    },
    layers: layers("protomaps", flavor, { lang: "en" }).filter((l) => !(l.layout && l.layout["icon-image"])),
  };
  const out = path.join(here, "..", "Rivolt", "Resources", "Map", `style-${name}.json`);
  writeFileSync(out, JSON.stringify(style, null, 1) + "\n");
  console.log(`wrote ${out}: ${style.layers.length} layers`);
}
