// Sums the gzip size of every shipped asset (js, mjs, css) in a build, and with a baseline file compares:
//   node scripts/bundle-total.mjs [distDir]                    prints the total
//   node scripts/bundle-total.mjs [distDir] --baseline FILE KB  fails when the total is more than KB over the baseline's
// bundle-baseline.json records the total of the last build before the MapLibre swap (Leaflet era), the number the Maps budget is against, and the
// initial (non-lazy) size: what index.html loads before any page, which must not grow.
import { readdirSync, readFileSync, appendFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { join } from "node:path";

const args = process.argv.slice(2);
const dist = args[0] && !args[0].startsWith("--") ? args[0] : "../internal/web/dist";
const dir = join(dist, "assets");
let total = 0;
for (const f of readdirSync(dir)) {
  if (!/\.(js|mjs|css)$/.test(f)) continue;
  total += gzipSync(readFileSync(join(dir, f)), { level: 9 }).length;
}
const kb = total / 1024;
console.log(`total shipped assets: ${kb.toFixed(1)} KB gzip`);

// What index.html loads up front: its scripts, stylesheets and preloads, and the static imports of those.
function initialKB() {
  const html = readFileSync(join(dist, "index.html"), "utf8");
  const q = [...html.matchAll(/(?:src|href)="(\/assets\/[^"]+\.(?:js|mjs|css))"/g)].map((m) => m[1]);
  const seen = new Set();
  while (q.length) {
    const f = q.pop();
    if (seen.has(f)) continue;
    seen.add(f);
    if (!/\.m?js$/.test(f)) continue;
    const src = readFileSync(join(dist, f), "utf8");
    for (const m of src.matchAll(/(?:import|export)[^"'`;]*?from\s*"(\.\/[^"]+)"|import\s*"(\.\/[^"]+)"/g)) q.push("/assets/" + (m[1] || m[2]).replace("./", ""));
  }
  let t = 0;
  for (const f of seen) {
    try {
      t += gzipSync(readFileSync(join(dist, f)), { level: 9 }).length;
    } catch {
      /* a path that is not a file */
    }
  }
  return t / 1024;
}
const initial = initialKB();
console.log(`initial (loaded before any page): ${initial.toFixed(1)} KB gzip`);

const i = args.indexOf("--baseline");
if (i < 0) process.exit(0);
const base = JSON.parse(readFileSync(args[i + 1], "utf8"));
const budget = Number(args[i + 2]);
const delta = kb - base.totalGzipKB;
const line = `net change vs the pre-MapLibre baseline (${base.commit}): ${delta >= 0 ? "+" : ""}${delta.toFixed(1)} KB gzip (budget +${budget} KB); initial ${initial.toFixed(1)} KB vs ${base.initialGzipKB} KB`;
console.log(line);
if (initial > base.initialGzipKB) {
  console.error("the initial (non-lazy) bundle grew: the map must load lazily, on map pages only");
  process.exit(1);
}
if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `### Bundle\n\n${kb.toFixed(1)} KB gzip total; ${line}\n\n`);
if (delta > budget) {
  console.error(`over the +${budget} KB budget`);
  process.exit(1);
}
