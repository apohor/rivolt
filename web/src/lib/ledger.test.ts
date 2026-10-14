import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  _resetMoneyTabFlagForTests,
  dataQualityNotes,
  fetchLedgerMonth,
  grammarFor,
  homePublicSplit,
  isValidMonth,
  ledgerMonthFixture,
  ledgerSummaryFixture,
  loadMoneyTabEnabled,
  shiftMonth,
  suspectSessionCount,
} from "./ledger";

function mockFetch(status: number, body: unknown) {
  const fn = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => "application/json" },
    text: async () => (typeof body === "string" ? body : JSON.stringify(body)),
  });
  vi.stubGlobal("fetch", fn);
  return fn;
}

describe("month helpers", () => {
  it("shifts across year boundaries", () => {
    expect(shiftMonth("2026-01", -1)).toBe("2025-12");
    expect(shiftMonth("2026-12", 1)).toBe("2027-01");
  });
  it("validates YYYY-MM", () => {
    expect(isValidMonth("2026-10")).toBe(true);
    expect(isValidMonth("2026-13")).toBe(false);
    expect(isValidMonth("26-10")).toBe(false);
    expect(isValidMonth(null)).toBe(false);
  });
});

describe("grammarFor", () => {
  it("maps provenance to recorded / suspect / unknown", () => {
    expect(grammarFor({ cost: 5, cost_source: "home_rate", flags: [] })).toBe("recorded");
    expect(grammarFor({ cost: 5, cost_source: "rivian", flags: [] })).toBe("recorded");
    expect(grammarFor({ cost: 5, cost_source: "home_rate", flags: ["home_rate_on_dcfc"] })).toBe("suspect");
    expect(grammarFor({ cost: 5, cost_source: "home_rate", flags: ["home_rate_away"] })).toBe("suspect");
    expect(grammarFor({ cost: null, cost_source: "unknown", flags: ["unpriced"] })).toBe("unknown");
  });
});

describe("fixture", () => {
  const m = ledgerMonthFixture("2026-09");

  it("matches its own totals (server-shaped, additive)", () => {
    const receiptCost = m.receipts.reduce((a, r) => a + (r.cost ?? 0), 0);
    expect(receiptCost).toBeCloseTo(m.spend.total, 6);
    expect(m.energy.by_kind.ac.kwh + m.energy.by_kind.dcfc.kwh).toBeCloseTo(m.energy.kwh_added, 6);
    expect(m.spend.priced_sessions + m.spend.unpriced_sessions).toBe(m.energy.sessions);
  });

  it("covers the data-quality states the page must render", () => {
    const flags = new Set(m.receipts.flatMap((r) => r.flags));
    for (const f of ["fragments_folded", "home_rate_on_dcfc", "unpriced", "null_energy", "continues_next_month"]) {
      expect(flags.has(f as never)).toBe(true);
    }
    expect(m.receipts.some((r) => r.cost_source === "rivian")).toBe(true);
  });

  it("contains no coordinates or addresses (privacy, C5)", () => {
    const json = JSON.stringify([m, ledgerSummaryFixture(12)]);
    expect(json).not.toMatch(/"(lat|lon|latitude|longitude|address)"/i);
  });
});

describe("homePublicSplit", () => {
  it("splits AC into home and public when receipts reconcile", () => {
    const m = ledgerMonthFixture("2026-09");
    const s = homePublicSplit(m)!;
    expect(s).not.toBeNull();
    expect(s.home.kwh + s.public.kwh).toBeCloseTo(m.energy.by_kind.ac.kwh, 6);
    expect(s.public.sessions).toBe(2);
  });

  it("falls back (null) when receipts don't match the server's AC total", () => {
    const m = ledgerMonthFixture("2026-09");
    const broken = { ...m, energy: { ...m.energy, by_kind: { ...m.energy.by_kind, ac: { ...m.energy.by_kind.ac, kwh: 1 } } } };
    expect(homePublicSplit(broken)).toBeNull();
  });
});

describe("quality notes", () => {
  it("counts under-priced sessions once even when both flags are set", () => {
    const m = ledgerMonthFixture("2026-09");
    expect(m.data_quality.home_rate_on_dcfc + m.data_quality.home_rate_away).toBe(6);
    expect(suspectSessionCount(m)).toBe(3);
    expect(dataQualityNotes(m).join(" ")).toMatch(/^3 sessions were priced at your home rate/);
  });

  it("says nothing about coverage when it's high", () => {
    const m = ledgerMonthFixture("2026-09");
    expect(dataQualityNotes(m).join(" ")).not.toMatch(/of miles driven/);
    const low = { ...m, driving: { ...m.driving, coverage: 0.06 } };
    expect(dataQualityNotes(low).join(" ")).toMatch(/6% of miles driven/);
  });
});

describe("fetchers and flag", () => {
  beforeEach(() => {
    _resetMoneyTabFlagForTests();
    Object.defineProperty(window, "location", {
      value: { pathname: "/money", search: "", assign: vi.fn() },
      writable: true,
    });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("sends month, tz and vehicle_id", async () => {
    const fn = mockFetch(200, ledgerMonthFixture("2026-10"));
    const r = await fetchLedgerMonth("2026-10", { vehicleID: "01-1", tz: "America/Chicago" });
    expect(r.source).toBe("api");
    const url = fn.mock.calls[0][0] as string;
    expect(url).toContain("/api/ledger/monthly?");
    expect(url).toContain("month=2026-10");
    expect(url).toContain("tz=America%2FChicago");
    expect(url).toContain("vehicle_id=01-1");
  });

  it("falls back to the labelled fixture when the endpoint is missing", async () => {
    mockFetch(404, "not found");
    const r = await fetchLedgerMonth("2026-10");
    expect(r.source).toBe("fixture");
  });

  it("surfaces real server errors", async () => {
    mockFetch(500, "db down");
    await expect(fetchLedgerMonth("2026-10")).rejects.toThrow("db down");
  });

  it("reads the per-user flag from /api/ledger/enabled, failing closed", async () => {
    mockFetch(200, { enabled: true });
    expect(await loadMoneyTabEnabled()).toBe(true);
    _resetMoneyTabFlagForTests();
    mockFetch(404, "not found");
    expect(await loadMoneyTabEnabled()).toBe(false);
  });
});
