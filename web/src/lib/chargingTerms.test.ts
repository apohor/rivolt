import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { rangePerHour } from "./chargeRange";

// People read "Level 2" and "fast charging", not the trade shorthand. Every quoted string and piece of JSX text in the web app and the
// iPhone app is checked for a bare "L2", "L1" or "DCFC"; code identifiers, comments and the admin pages are not user-visible.
// "DCFC Tracker" is the name of a source, so it stays.
const ROOT = join(__dirname, "..", "..", "..");

function files(dir: string, ext: RegExp, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (/node_modules|Resources|design|Tests$|admin/i.test(name)) continue;
      files(p, ext, out);
    } else if (ext.test(name) && !/\.test\.|ThemeTokens|Admin/.test(name)) out.push(p);
  }
  return out;
}

function visibleStrings(src: string, swift: boolean): string[] {
  const out: string[] = [];
  const stripped = src.replace(/\/\*[\s\S]*?\*\//g, "").split("\n").map((l) => l.replace(/^\s*\/\/.*$/, "").replace(/\s\/\/ .*$/, ""));
  for (const line of stripped) {
    for (const m of line.matchAll(/"((?:[^"\\]|\\.)*)"|`((?:[^`\\]|\\.)*)`/g)) out.push(m[1] ?? m[2] ?? "");
    if (!swift) for (const m of line.matchAll(/>([^<>{}]+)</g)) out.push(m[1]);
  }
  return out;
}

const BARE = /\b(L2|L1|DCFC)\b(?! Tracker)/;

describe("user-visible charging terms", () => {
  it("never shows a bare L2, L1 or DCFC", () => {
    const hits: string[] = [];
    for (const [dir, ext, swift] of [
      [join(ROOT, "web", "src"), /\.(ts|tsx)$/, false],
      [join(ROOT, "ios", "Rivolt"), /\.swift$/, true],
    ] as const) {
      for (const f of files(dir, ext)) {
        for (const s of visibleStrings(readFileSync(f, "utf8"), swift)) if (BARE.test(s)) hits.push(`${f.slice(ROOT.length + 1)}: ${s.slice(0, 80)}`);
      }
    }
    expect(hits).toEqual([]);
  });
});

describe("rangePerHour", () => {
  it("turns kilowatts into an about-range with the page's two efficiencies", () => {
    expect(rangePerHour(7, "mi")).toBe("about 13–16 mi of range per hour");
    expect(rangePerHour(7, "km")).toBe("about 20–26 km of range per hour");
  });
});
