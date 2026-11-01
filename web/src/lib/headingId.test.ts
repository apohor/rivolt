import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { headingId } from "./headingId";

describe("legal page heading ids", () => {
  it("keeps the bare number for numbered headings, so every /terms#N link still lands", () => {
    expect(headingId("2. What Rivolt is")).toBe("2");
    expect(headingId("11. Plans and billing")).toBe("11");
    expect(headingId("5.1 not a heading number")).toBe("5");
  });

  it("slugs plain headings", () => {
    expect(headingId("Free and Pro")).toBe("free-and-pro");
    expect(headingId("Common questions")).toBe("common-questions");
    expect(headingId("What's new?")).toBe("whats-new");
    expect(headingId("Café & Crème")).toBe("cafe-creme");
    expect(headingId("Day 1: connect")).toBe("day-1-connect");
  });

  it("gives no id when there is nothing to anchor to", () => {
    expect(headingId("")).toBeUndefined();
    expect(headingId("???")).toBeUndefined();
  });

  // Across the shipped documents: a numbered heading still gets exactly its number, and no two "##" headings of one document share an
  // id (getElementById would take the first and the second link would land in the wrong place).
  const dir = join(__dirname, "..", "content");
  for (const f of readdirSync(dir).filter((n) => n.endsWith(".md"))) {
    it(`${f}: numbered ids unchanged, ids unique`, () => {
      const heads = readFileSync(join(dir, f), "utf8")
        .split("\n")
        .filter((l) => /^## /.test(l))
        .map((l) => l.slice(3).trim());
      const ids = heads.map((h) => headingId(h)).filter((x): x is string => !!x);
      for (const h of heads) {
        const m = h.match(/^(\d+)\./);
        if (m) expect(headingId(h)).toBe(m[1]);
      }
      expect(new Set(ids).size, `duplicate heading ids in ${f}: ${ids.join(", ")}`).toBe(ids.length);
    });
  }
});
