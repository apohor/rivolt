// Reads Playwright JSON reports (the CI reporter writes e2e-results/results.json in each shard):
//   node scripts/e2e-timings.mjs merge <out.json> <expected shards> <results.json>...
//       Each spec file's test seconds, every attempt counted (a retry takes runner time too); a spec in
//       e2e/ that ran no test in this project (the WebKit run is the phone specs only) is 0, so the
//       shard split doesn't count it. Writes nothing unless all <expected shards> reports are there:
//       a missing shard would read as zeros.
//   node scripts/e2e-timings.mjs summary <results.json>...
//       Markdown for the job summary: the counts, and each flaky test (passed only on a retry).
//   node scripts/e2e-timings.mjs flaky <results.json>...
//       The flaky tests, one "- file › title" line each (empty when none).
import fs from "node:fs";

const [mode, ...rest] = process.argv.slice(2);

function walk(suite, file, out) {
  const f = suite.file || file;
  for (const spec of suite.specs ?? []) {
    for (const t of spec.tests ?? []) out.push({ file: f, title: spec.title, test: t });
  }
  for (const s of suite.suites ?? []) walk(s, f, out);
  return out;
}
const read = (paths) =>
  paths.filter((p) => fs.existsSync(p)).map((p) => JSON.parse(fs.readFileSync(p, "utf8")));
const tests = (reports) => reports.flatMap((r) => (r.suites ?? []).flatMap((s) => walk(s, s.file, [])));
const base = (f) => f.split("/").pop();

if (mode === "merge") {
  const [out, expected, ...paths] = rest;
  const reports = read(paths);
  if (reports.length !== Number(expected)) {
    console.error(`have ${reports.length} of ${expected} shard reports: not writing timings`);
    process.exit(0);
  }
  const secs = {};
  for (const f of fs.readdirSync(new URL("../e2e/", import.meta.url)).filter((f) => f.endsWith(".pw.ts"))) secs[f] = 0;
  for (const { file, test } of tests(reports)) {
    const ms = (test.results ?? []).reduce((a, r) => a + (r.duration ?? 0), 0);
    secs[base(file)] = (secs[base(file)] ?? 0) + ms / 1000;
  }
  const sorted = Object.fromEntries(Object.entries(secs).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => [k, Math.round(v * 10) / 10]));
  fs.writeFileSync(out, JSON.stringify(sorted, null, 1) + "\n");
  console.log(`wrote ${out}: ${Object.keys(sorted).length} specs, ${Math.round(Object.values(sorted).reduce((a, b) => a + b, 0))} s`);
} else if (mode === "summary" || mode === "flaky") {
  const all = tests(read(rest));
  const flaky = all.filter((t) => t.test.status === "flaky").map((t) => `- ${base(t.file)} › ${t.title}`);
  if (mode === "flaky") {
    if (flaky.length) console.log(flaky.join("\n"));
  } else {
    const n = (s) => all.filter((t) => t.test.status === s).length;
    console.log(`**e2e:** ${n("expected")} passed, ${n("flaky")} flaky (passed only on a retry), ${n("unexpected")} failed, ${n("skipped")} skipped`);
    if (flaky.length) console.log(`\nFlaky:\n${flaky.join("\n")}`);
  }
} else {
  console.error("usage: e2e-timings.mjs merge <out> <expected> <results.json>... | summary <results.json>... | flaky <results.json>...");
  process.exit(2);
}
