// Prints one chromium shard's spec files, balanced by duration rather than count:
//   node scripts/e2e-shard.mjs <shard> <total>       (from web/)
//   node scripts/e2e-shard.mjs --check <total>       (every spec in exactly one shard; prints the loads)
// e2e/shard-timings.json holds each spec file's test seconds from a recent CI run (refresh it when
// the split drifts: sum the "(N.Ns)" of each file in the e2e job logs). A spec not in it counts as the
// median. Files go biggest first onto the lightest shard (longest-processing-time first), so no shard
// ends up with the heavy files by chance as Playwright's count-based --shard can.
import fs from "node:fs";

const args = process.argv.slice(2);
const check = args[0] === "--check";
const shard = check ? 0 : Number(args[0]);
const total = Number(check ? args[1] : args[1]);
if (!Number.isInteger(total) || total < 1 || (!check && (!Number.isInteger(shard) || shard < 1 || shard > total))) {
  console.error("usage: e2e-shard.mjs <shard> <total> | --check <total>");
  process.exit(2);
}

const times = JSON.parse(fs.readFileSync(new URL("../e2e/shard-timings.json", import.meta.url), "utf8"));
const files = fs.readdirSync(new URL("../e2e/", import.meta.url)).filter((f) => f.endsWith(".pw.ts")).sort();
const known = Object.values(times).sort((a, b) => a - b);
const median = known.length ? known[Math.floor(known.length / 2)] : 10;

const items = files.map((f) => [f, times[f] ?? median]).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
const load = Array(total).fill(0);
const out = Array.from({ length: total }, () => []);
for (const [f, t] of items) {
  let k = 0;
  for (let j = 1; j < total; j++) if (load[j] < load[k]) k = j;
  load[k] += t;
  out[k].push(`e2e/${f}`);
}

if (check) {
  const all = out.flat();
  if (new Set(all).size !== files.length || all.length !== files.length) {
    console.error("a spec is missing or in two shards");
    process.exit(1);
  }
  console.log(load.map((l, i) => `shard ${i + 1}: ${out[i].length} files, ~${Math.round(l)} s of tests`).join("\n"));
} else {
  console.log(out[shard - 1].join(" "));
}
