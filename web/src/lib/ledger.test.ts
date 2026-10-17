import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  _resetMoneyTabFlagForTests,
  dataQualityNotes,
  estimatedShare,
  fetchLedgerMonth,
  grammarFor,
  isValidMonth,
  ledgerMonthFixture,
  ledgerSummaryFixture,
  loadMoneyTabEnabled,
  shiftMonth,
  sourceLabel,
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

describe("grammarFor (contract §10.1)", () => {
  it.each([
    [{ cost: 5, cost_source: "home_rate", flags: [] }, "recorded"],
    [{ cost: 5, cost_source: "rivian", flags: [] }, "recorded"],
    [{ cost: 5, cost_source: "user", flags: [] }, "recorded"],
    [{ cost: 0, cost_source: "user", flags: [] }, "free"],
    [{ cost: 23, cost_source: "network_estimate", flags: [] }, "estimated"],
    [{ cost: 9, cost_source: "home_rate_misapplied", flags: ["estimate_pending"] }, "pending"],
    [{ cost: null, cost_source: "unknown", flags: ["estimate_pending"] }, "pending"],
    [{ cost: 1, cost_source: "home_rate", flags: ["home_rate_away"] }, "suspect"],
    [{ cost: null, cost_source: "unknown", flags: ["unpriced"] }, "unknown"],
  ] as const)("%o → %s", (r, g) => {
    expect(grammarFor({ ...r, flags: [...r.flags] })).toBe(g);
  });

  it("labels Billed, Free and network estimates", () => {
    expect(sourceLabel({ cost: 3, cost_source: "rivian", rate: 0.6, flags: [] }, "USD")).toMatch(/^Billed · \$0\.60\/kWh/);
    expect(sourceLabel({ cost: 0, cost_source: "user", rate: 0, flags: [] }, "USD")).toBe("Free · entered by you");
    expect(
      sourceLabel({ cost: 23, cost_source: "network_estimate", rate: 0.36, network: "Electrify America", flags: [] }, "USD"),
    ).toBe("Estimate · Electrify America · $0.36/kWh");
  });
});

describe("fixture", () => {
  const m = ledgerMonthFixture("2026-09");

  it("is internally consistent, server-shaped", () => {
    const b = m.spend.by_source;
    expect((b.rivian ?? 0) + (b.home_rate ?? 0) + (b.network_estimate ?? 0) + (b.user ?? 0)).toBeCloseTo(m.spend.total, 6);
    const receiptCost = m.receipts.reduce((a, r) => a + (r.cost ?? 0), 0);
    expect(receiptCost).toBeCloseTo(m.spend.total, 6);
    for (const split of [m.energy.by_kind, m.energy.by_place!]) {
      const lines = Object.values(split);
      expect(lines.reduce((a, l) => a + l.sessions, 0)).toBe(m.energy.sessions);
      expect(lines.reduce((a, l) => a + l.kwh, 0)).toBeCloseTo(m.energy.kwh_added, 6);
      expect(lines.reduce((a, l) => a + l.cost, 0)).toBeCloseTo(m.spend.total, 6);
    }
    expect(
      m.spend.priced_sessions + m.spend.unpriced_sessions + (m.spend.estimate_pending_sessions ?? 0),
    ).toBe(m.energy.sessions);
  });

  it("covers every price and quality state the page renders", () => {
    const sources = new Set(m.receipts.flatMap((r) => r.sessions.map((x) => x.cost_source)));
    for (const s of ["rivian", "home_rate", "network_estimate", "user", "home_rate_misapplied", "unknown"]) {
      expect(sources.has(s as never)).toBe(true);
    }
    const flags = new Set(m.receipts.flatMap((r) => r.flags));
    for (const f of ["fragments_folded", "estimate_pending", "home_rate_away", "unpriced", "null_energy", "continues_next_month"]) {
      expect(flags.has(f as never)).toBe(true);
    }
    expect(m.receipts.some((r) => r.cost_source === "mixed")).toBe(true);
  });

  it("contains no coordinates or addresses (privacy, C5)", () => {
    const json = JSON.stringify([m, ledgerSummaryFixture(12)]);
    expect(json).not.toMatch(/"(lat|lon|latitude|longitude|address)"/i);
  });
});

describe("counts and notes", () => {
  it("counts only in-total home-rate-away sessions as suspect (pending DCFC excluded)", () => {
    const m = ledgerMonthFixture("2026-09");
    expect(m.data_quality.home_rate_away).toBe(3); // 2 pending DCFC + 1 taper fragment
    expect(suspectSessionCount(m)).toBe(1);
  });

  it("uses the server's estimated_share, else by_source", () => {
    const m = ledgerMonthFixture("2026-09");
    expect(estimatedShare(m)).toBeCloseTo(m.spend.by_source.network_estimate! / m.spend.total, 6);
    const old = { ...m, spend: { ...m.spend, estimated_share: undefined } };
    expect(estimatedShare(old)).toBeCloseTo(m.spend.by_source.network_estimate! / m.spend.total, 6);
  });

  it("explains pending, suspect and unpriced sessions in plain words", () => {
    const notes = dataQualityNotes(ledgerMonthFixture("2026-09")).join(" ");
    expect(notes).toMatch(/^2 fast-charging sessions were priced at your home rate by an older version/);
    expect(notes).toMatch(/1 session away from home was priced at your home rate/);
    expect(notes).toMatch(/2 sessions have no price/);
    expect(notes).toMatch(/older sessions' price source was worked out/);
  });

  it("tolerates pre-§10 payloads (missing by_place, superseded, new spend keys)", () => {
    const m = ledgerMonthFixture("2026-09");
    const { superseded: _s, ...dq } = m.data_quality;
    const { by_place: _p, ...energy } = m.energy;
    const old = {
      ...m,
      energy,
      data_quality: dq,
      spend: { ...m.spend, by_source: { rivian: 1, home_rate: 2 }, estimated_share: undefined, estimate_pending_sessions: undefined },
    };
    expect(() => dataQualityNotes(old)).not.toThrow();
    expect(estimatedShare(old)).toBe(0);
  });

  it("mentions coverage only when it's low", () => {
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

describe("confirmed network estimates", () => {
  const base = { cost: 34.65, rate: 0.4, network: "Tesla Supercharger", flags: [] as never[], cost_source: "network_estimate" as const };
  it("reads as an estimate until confirmed, then as a recorded price with a Confirmed label", () => {
    expect(grammarFor({ ...base, network_state: "estimated" })).toBe("estimated");
    expect(sourceLabel({ ...base, network_state: "estimated" }, "USD")).toMatch(/^Estimated, confirm/);
    expect(grammarFor({ ...base, network_state: "confirmed" })).toBe("recorded");
    expect(sourceLabel({ ...base, network_state: "confirmed" }, "USD")).toBe("Confirmed · Tesla Supercharger · $0.40/kWh");
  });
  it("a receipt is confirmed only when every network-estimate session is", () => {
    const sess = (network_state: "confirmed" | "estimated") => ({ cost_source: "network_estimate" as const, flags: [] as never[], network_state });
    expect(grammarFor({ ...base, sessions: [sess("confirmed"), sess("confirmed")] })).toBe("recorded");
    expect(grammarFor({ ...base, sessions: [sess("confirmed"), sess("estimated")] })).toBe("estimated");
  });
});
