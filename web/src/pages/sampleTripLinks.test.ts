import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";

// The sample-trip pages (/sample-trips, /sample-trips/<slug>) read their data from JSON the server puts in the HTML it renders, which only a
// full page load fetches. A client-side <Link> into them lands on "This sample trip didn't load" (it did, from the landing page), so every
// link into them is a plain <a href>.
const walk = (dir: string): string[] =>
  readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? walk(p) : /\.(tsx|ts)$/.test(f) && !/\.test\./.test(f) ? [p] : [];
  });

describe("links into the sample-trip pages", () => {
  const src = resolve(__dirname, "..");
  const files = walk(src);
  it("sees the source", () => expect(files.length).toBeGreaterThan(50));
  it.each(files)("%s has no client-side <Link> to /sample-trips", (f) => {
    const text = readFileSync(f, "utf8");
    expect(/<(Link|NavLink)\b[^>]*\bto=\{?["'`]\/sample-trips/.test(text), `${f} links /sample-trips with a router Link`).toBe(false);
  });
});
