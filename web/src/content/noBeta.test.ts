// Rivolt has a free tier and a Pro tier; it is not "in beta". The owner asked
// for the word to go (Nov 21). This fails if the old wording comes back in
// user-facing copy: web source and content, the README and docs people read,
// the App Store listing and the demo-video captions.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

const ROOT = join(__dirname, "..", "..", "..");
const OLD_WORDING = /during (its |the )?(public )?beta|public beta|closed beta|in beta\b|the beta\b|beta (seat|access|invite|account)|testflight beta/i;
// The owner's wording for the two plans is "free tier and Pro tier" (Nov 21), not "free tier and Rivolt Pro" or "a paid plan, Rivolt Pro".
const OLD_PLANS = /free tier and (a )?(paid )?(Rivolt )?Pro\b(?! tier)|paid plan, Rivolt Pro|paid (Rivolt )?Pro plan/i;

// Files that keep the old wording on purpose (class E: historical or frozen).
const ALLOW = new Set([
  "web/src/content/terms-2026-10-23.md", // the previous Terms stay reachable, unchanged
  "web/src/content/terms.md", // section 5 changes in its own PR, which needs the owner's approval; that PR removes this line
  "docs/PRO_LAUNCH_COPY.md", // the approved launch notice, byte-identical to internal/launchnotice/copy.go
]);

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name === "__fixtures__" || name === "node_modules") continue; // fixtures hold past release notes
      walk(p, out);
    } else if (/\.(tsx?|md)$/.test(name) && !/\.test\.tsx?$/.test(name)) out.push(p);
  }
  return out;
}

describe("no beta wording, and one wording for the plans, in user-facing copy", () => {
  it("does not say the service is, or was free during, a beta", () => {
    const files = [
      ...walk(join(ROOT, "web", "src")),
      join(ROOT, "README.md"),
      join(ROOT, "docs", "SIGNUP.md"),
      join(ROOT, "docs", "ROADMAP.md"),
      join(ROOT, "docs", "PRO_LAUNCH_COPY.md"),
      join(ROOT, "ios", "store", "listing.md"),
      join(ROOT, "tools", "demo-video", "captions.json"),
    ];
    const hits: string[] = [];
    for (const f of files) {
      const rel = relative(ROOT, f);
      if (ALLOW.has(rel)) continue;
      readFileSync(f, "utf8")
        .split("\n")
        .forEach((line, i) => {
          if (OLD_WORDING.test(line) || OLD_PLANS.test(line)) hits.push(`${rel}:${i + 1}: ${line.trim().slice(0, 120)}`);
        });
    }
    expect(hits).toEqual([]);
  });
});
