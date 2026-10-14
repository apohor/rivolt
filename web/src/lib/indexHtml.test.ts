import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// The server's CSP has no 'unsafe-inline' for scripts, so index.html
// must never carry an inline <script>.
describe("index.html CSP", () => {
  it("has no inline scripts", () => {
    const html = readFileSync("index.html", "utf8");
    const inline = [...html.matchAll(/<script\b([^>]*)>/g)].filter((m) => !/\bsrc=/.test(m[1]));
    expect(inline).toHaveLength(0);
    expect(html).toContain('src="/theme-init.js"');
  });
});
