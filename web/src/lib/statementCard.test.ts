import { describe, expect, it } from "vitest";
import { currentMonth, ledgerMonthFixture } from "./ledger";
import { buildStatementCardModel } from "./statementCard";

describe("buildStatementCardModel", () => {
  const m = ledgerMonthFixture(currentMonth());
  it("uses the server's totals and three Home/Public/Fast buckets", () => {
    const c = buildStatementCardModel(m);
    expect(c.buckets.map((b) => b.label).slice(0, 3)).toEqual(["Home", "Away", "Fast charging"]);
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

  it("says when sessions are excluded from the total", () => {
    const x = structuredClone(m);
    x.spend.unpriced_sessions = 1;
    x.spend.estimate_pending_sessions = 4;
    expect(buildStatementCardModel(x).notIncluded).toBe("+ 5 sessions not yet priced");
    x.spend.unpriced_sessions = 0;
    x.spend.estimate_pending_sessions = 0;
    expect(buildStatementCardModel(x).notIncluded).toBeNull();
  });

  it("marks a sample-truck card so it can't pass for real data", () => {
    expect(buildStatementCardModel(m, { sample: true }).footnote).toBe("Sample truck · generated data");
    expect(buildStatementCardModel(m).footnote).toBe("Charging cost, tracked with Rivolt");
  });
});
