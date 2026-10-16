import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// e2e/QUARANTINE.md caps the quarantine at 3 specs: a fourth means something
// isn't being fixed. A quarantined test stays in the repo as test.fixme(...)
// (or an unconditional test.skip) with a comment naming that file. A skip or
// fixme without the reference is a silent bypass, so it fails here too.
// Conditional skips, test.skip(cond, "reason"), are not quarantine.
const E2E = path.join(process.cwd(), "e2e");
const MAX = 3;

// Declaration-style skips: test.fixme(...), test.describe.skip(...),
// test.skip() / test.skip("title", ...). A first argument that is anything
// else is the conditional form.
const SKIPPED = /\btest(?:\.describe)?\.(?:fixme|skip)\(\s*(?:\)|["'`]|async|\()|\btest\.fixme\(|\btest\.describe\.(?:fixme|skip)\(/;

function skipped(): { file: string; line: number; linked: boolean }[] {
  const out: { file: string; line: number; linked: boolean }[] = [];
  for (const f of fs.readdirSync(E2E).filter((n) => n.endsWith(".pw.ts"))) {
    const lines = fs.readFileSync(path.join(E2E, f), "utf8").split("\n");
    lines.forEach((l, i) => {
      if (!SKIPPED.test(l)) return;
      const near = [lines[i - 1] ?? "", l, lines[i + 1] ?? ""].join("\n");
      out.push({ file: f, line: i + 1, linked: near.includes("QUARANTINE.md") });
    });
  }
  return out;
}

describe("e2e quarantine", () => {
  const doc = fs.readFileSync(path.join(E2E, "QUARANTINE.md"), "utf8");
  const section = doc.split(/^## /m).find((s) => s.startsWith("Quarantine")) ?? "";
  const rows = section
    .split("\n")
    .filter((l) => l.startsWith("|"))
    .slice(2) // header and separator
    .filter((l) => !/^\|\s*_none_/.test(l));

  it("lists at most 3 specs", () => {
    expect(rows.length).toBeLessThanOrEqual(MAX);
  });

  it("has no skipped or fixme test without a QUARANTINE.md reference", () => {
    const bare = skipped().filter((s) => !s.linked).map((s) => `${s.file}:${s.line}`);
    expect(bare, "add a QUARANTINE.md entry and reference, or remove the skip").toEqual([]);
  });

  it("has at most 3 skipped or fixme tests in total", () => {
    expect(skipped().length).toBeLessThanOrEqual(MAX);
  });
});
