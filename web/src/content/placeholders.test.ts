// Launch-day placeholders in the Pro copy (Terms, Privacy, App Store listing)
// must be replaced with real dates before the copy ships. This test fails while
// any of them is still in a public content file.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const PLACEHOLDERS = ["LAUNCH_DATE", "FOUNDING_END_DATE", "TERMS_EFFECTIVE_EXISTING"];
const DIRS = [join(__dirname), join(__dirname, "..", "..", "..", "ios", "store")];

describe("Pro launch placeholders", () => {
  it("do not appear in public content", () => {
    const hits: string[] = [];
    for (const dir of DIRS) {
      for (const name of readdirSync(dir)) {
        if (!name.endsWith(".md")) continue;
        const text = readFileSync(join(dir, name), "utf8");
        for (const p of PLACEHOLDERS) if (text.includes(p)) hits.push(`${name}: ${p}`);
      }
    }
    expect(hits).toEqual([]);
  });
});
