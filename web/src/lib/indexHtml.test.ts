import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// The server's CSP has no 'unsafe-inline' for scripts, so index.html
// must never carry an inline <script>.
describe("index.html CSP", () => {
  it("has no inline scripts", () => {
    const html = readFileSync("index.html", "utf8");
    const inline = [...html.matchAll(/<script\b([^>]*)>/g)].filter((m) => !/\bsrc=/.test(m[1]));
    expect(inline).toHaveLength(0);
    expect(html).toMatch(/src="\/theme-init\.js(\?v=\d+)?"/);
  });

  // The asset guard must be a blocking classic script ahead of the module
  // entry, or it can't see the entry bundle fail to load.
  it("loads asset-guard.js before the app's module entry", () => {
    const html = readFileSync("index.html", "utf8");
    const guard = html.search(/<script src="\/asset-guard\.js(\?v=\d+)?"><\/script>/);
    const entry = html.search(/<script type="module"/);
    expect(guard).toBeGreaterThan(-1);
    expect(entry).toBeGreaterThan(guard);
  });
});
