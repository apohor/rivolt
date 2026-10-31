// Prints the gzip size of the lazy MapLibre map chunk and fails above the cap.
//   node scripts/map-bundle-size.mjs [distDir] [capKB]
// The map chunk is everything a map page loads on top of the app: the maplibre-gl library, its worker and
// stylesheet, and the app's own map code (the files that contain the "rivolt-route" or "rivolt-chargers" source ids). The dist dir
// defaults to the one `npm run build` writes (../internal/web/dist).
import { readdirSync, readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { join } from "node:path";

const dist = process.argv[2] || "../internal/web/dist";
const capKB = Number(process.argv[3] || 480);
const dir = join(dist, "assets");

const files = readdirSync(dir).filter((f) => /\.(js|mjs|css)$/.test(f));
const mapFiles = files.filter((f) => f.startsWith("maplibre-gl") || readFileSync(join(dir, f), "utf8").includes("rivolt-route") || readFileSync(join(dir, f), "utf8").includes("rivolt-chargers"));
if (!mapFiles.length) {
  console.error(`no map chunk found in ${dir}`);
  process.exit(1);
}

let total = 0;
for (const f of mapFiles.sort()) {
  const n = gzipSync(readFileSync(join(dir, f)), { level: 9 }).length;
  total += n;
  console.log(`  ${f.padEnd(44)} ${(n / 1024).toFixed(1).padStart(7)} KB gzip`);
}
const kb = total / 1024;
console.log(`map chunk total: ${kb.toFixed(1)} KB gzip (cap ${capKB} KB)`);
if (process.env.GITHUB_STEP_SUMMARY) {
  const { appendFileSync } = await import("node:fs");
  appendFileSync(process.env.GITHUB_STEP_SUMMARY, `### Lazy map chunk\n\n${kb.toFixed(1)} KB gzip (cap ${capKB} KB)\n\n`);
}
if (kb > capKB) {
  console.error(`map chunk is over the ${capKB} KB gzip cap`);
  process.exit(1);
}
