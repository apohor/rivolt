import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import { iosOnlyFixtures } from "./fixtures.contract";

// Every server fixture is either assigned to a web type in fixtures.contract.ts (which `tsc` checks) or listed in iosOnlyFixtures
// with a reason. A fixture that is neither would never be checked against the web.
const dir = resolve(__dirname, "../../../ios/RivoltTests/Fixtures");
const source = readFileSync(resolve(__dirname, "fixtures.contract.ts"), "utf8");

describe("web types against the server fixtures", () => {
  const fixtures = readdirSync(dir).filter((f) => f.endsWith(".json"));

  it("sees the fixtures", () => {
    expect(fixtures.length).toBeGreaterThan(30);
  });

  it.each(fixtures)("%s is checked or listed with a reason", (f) => {
    const imported = source.includes(`/Fixtures/${f}"`);
    const listed = f in iosOnlyFixtures;
    expect(imported || listed, `${f} is neither imported nor listed in iosOnlyFixtures`).toBe(true);
    expect(imported && listed, `${f} is both imported and listed`).toBe(false);
  });

  it("lists no fixture that does not exist", () => {
    for (const f of Object.keys(iosOnlyFixtures)) expect(fixtures, f).toContain(f);
  });
});
