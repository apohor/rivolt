import { describe, expect, it } from "vitest";
import { currentMonth, ledgerMonthFixture } from "./ledger";
import { buildStatementCardModel } from "./statementCard";

describe("buildStatementCardModel", () => {
  const m = ledgerMonthFixture(currentMonth());
  it("uses the server's totals and three Home/Public/Fast buckets", () => {
    const c = buildStatementCardModel(m);
    expect(c.buckets.map((b) => b.label).slice(0, 3)).toEqual(["Home", "Public", "Fast charging"]);
    expect(c.total).toContain(Math.floor(m.spend.total).toString());
  });
  it("falls back to AC / Fast charging without by_place_kind", () => {
    const old = structuredClone(m);
    delete old.energy.by_place_kind;
    expect(buildStatementCardModel(old).buckets.map((b) => b.label)).toEqual(["AC", "Fast charging"]);
  });
  it("carries no places, coordinates or ids", () => {
    const text = JSON.stringify(buildStatementCardModel(m));
    for (const r of m.receipts) {
      expect(text).not.toContain(r.receipt_id);
      expect(text).not.toContain(r.vehicle_id);
    }
    expect(text, text).not.toMatch(/-?\d{1,3}\.\d{4,}/); // no lat/lon-like numbers
    expect(text).not.toMatch(/street|avenue|\bst\b|address/i);
  });
});
