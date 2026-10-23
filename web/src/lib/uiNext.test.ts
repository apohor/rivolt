import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// The new look is the only one: every page, public ones included, starts with html[data-ui="next"]
// (the look's CSS layer), straight from index.html, before React mounts.
describe("the look's CSS layer", () => {
  it("is switched on in index.html", () => {
    const html = fs.readFileSync(path.join(__dirname, "../../index.html"), "utf8");
    expect(html).toMatch(/<html lang="en" data-ui="next">/);
  });
});
