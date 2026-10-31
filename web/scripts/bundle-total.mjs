// Sums the gzip size of every shipped asset (js, mjs, css) in a build, and with a baseline file compares:
//   node scripts/bundle-total.mjs [distDir]                    prints the total
//   node scripts/bundle-total.mjs [distDir] --baseline FILE KB  fails when the total is more than KB over the baseline's
// bundle-baseline.json records the total of the last build before the MapLibre swap (Leaflet era), the number the +360 KB budget is against.
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

const i = args.indexOf("--baseline");
if (i < 0) process.exit(0);
const base = JSON.parse(readFileSync(args[i + 1], "utf8"));
const budget = Number(args[i + 2]);
const delta = kb - base.totalGzipKB;
const line = `net change vs the pre-MapLibre baseline (${base.commit}): ${delta >= 0 ? "+" : ""}${delta.toFixed(1)} KB gzip (budget +${budget} KB)`;
console.log(line);
if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `### Bundle\n\n${kb.toFixed(1)} KB gzip total; ${line}\n\n`);
if (delta > budget) {
  console.error(`over the +${budget} KB budget`);
  process.exit(1);
}
