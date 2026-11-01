// DIAGNOSTIC (Maps 10, not for merge): who owns the bytes of each big chunk, from the source maps. Prints, per chunk over 40 kB raw, the
// biggest packages / source dirs by their share of the minified bytes and that share of the chunk's gzip size.
import { readdirSync, readFileSync, existsSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { join } from "node:path";
import { SourceMapConsumer } from "source-map-js";

const dist = process.argv[2] || "../internal/web/dist";
const dir = join(dist, "assets");
function owner(src) {
  const m = src.match(/node_modules\/((?:@[^/]+\/)?[^/]+)/);
  if (m) return m[1];
  const c = src.match(/(core-js\/(?:modules|internals)\/[^/]+)/);
  if (c) return c[1];
  const s = src.match(/src\/([^/]+(?:\/[^/]+)?)/);
  return s ? "src/" + s[1].replace(/\.[a-z]+$/, "") : src.slice(0, 40);
}
for (const f of readdirSync(dir).filter((f) => /\.m?js$/.test(f))) {
  const code = readFileSync(join(dir, f), "utf8");
  if (code.length < 8000 || !existsSync(join(dir, f + ".map"))) continue;
  const gz = gzipSync(code, { level: 9 }).length;
  const lines = code.split("\n");
  const maps = [];
  new SourceMapConsumer(JSON.parse(readFileSync(join(dir, f + ".map"), "utf8"))).eachMapping((m) => maps.push(m));
  const by = new Map();
  for (let i = 0; i < maps.length; i++) {
    const m = maps[i];
    const next = maps[i + 1];
    const lineLen = (lines[m.generatedLine - 1] || "").length;
    const end = next && next.generatedLine === m.generatedLine ? next.generatedColumn : lineLen;
    const n = Math.max(0, end - m.generatedColumn);
    const o = m.source ? owner(m.source) : "(none)";
    by.set(o, (by.get(o) || 0) + n);
  }
  console.log(`\n== ${f}  ${(code.length / 1024).toFixed(0)} kB raw, ${(gz / 1024).toFixed(1)} kB gzip`);
  for (const [o, n] of [...by].sort((a, b) => b[1] - a[1]).slice(0, f.startsWith("polyfills") ? 80 : 10)) {
    console.log(`   ${o.padEnd(52)} ${(n / 1024).toFixed(1).padStart(8)} kB raw  ~${((n / code.length) * (gz / 1024)).toFixed(1).padStart(6)} kB gzip`);
  }
}
