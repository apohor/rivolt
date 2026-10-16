import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// e2e/QUARANTINE.md caps the quarantine at 3 specs: a fourth means something
// isn't being fixed. Quarantined tests stay in the repo as test.fixme(...)
// that name the file, so the same cap applies to them.
const E2E = path.join(process.cwd(), "e2e");

describe("e2e quarantine", () => {
  const doc = fs.readFileSync(path.join(E2E, "QUARANTINE.md"), "utf8");
  const section = doc.split(/^## /m).find((s) => s.startsWith("Quarantine")) ?? "";
  const rows = section
    .split("\n")
    .filter((l) => l.startsWith("|"))
    .slice(2) // header and separator
    .filter((l) => !/^\|\s*_none_/.test(l));

  it("lists at most 3 specs", () => {
    expect(rows.length).toBeLessThanOrEqual(3);
  });

  it("has at most 3 test.fixme that point at the quarantine file", () => {
    const hits = fs
      .readdirSync(E2E)
      .filter((f) => f.endsWith(".pw.ts"))
      .flatMap((f) => fs.readFileSync(path.join(E2E, f), "utf8").split("\n").filter((l) => /test\.fixme\(/.test(l) && l.includes("QUARANTINE.md")));
    expect(hits.length).toBeLessThanOrEqual(3);
  });
});
