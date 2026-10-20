import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ReceiptRow } from "./money";
import { grammarFor, needsPrice, sourceLabel, type Receipt, type ReceiptSession } from "../lib/ledger";

const base: Omit<ReceiptSession, "charge_id" | "cost" | "cost_source" | "flags" | "peak_kw" | "kwh" | "price_per_kwh"> = {
  started_at: "2026-10-12T16:00:00Z",
  ended_at: "2026-10-12T16:40:00Z",
  soc_start: 10,
  soc_end: 60,
  final_state: "charging_complete",
  source: "live",
  place: "away",
};

// Contract §10 example: one plug-in at the same spot (no drive in
// between) with a misapplied DCFC, a billed RAN stop and an unpriced
// away L1 → cost_source "mixed", cost = the billed part only.
const mixed: Receipt = {
  receipt_id: "rcpt_s1",
  vehicle_id: "01-000000000",
  kind: "dcfc",
  place: "away",
  km_from_home: 412.3,
  started_at: "2026-10-12T16:00:00Z",
  ended_at: "2026-10-13T08:00:00Z",
  local_date: "2026-10-12",
  spans_days: 2,
  soc_start: 10,
  soc_end: 90,
  kwh: 150,
  peak_kw: 210,
  cost: 30,
  cost_source: "mixed",
  rate: 0.6,
  network: "Rivian Adventure Network",
  session_count: 3,
  unpriced_sessions: 1,
  flags: ["fragments_folded", "home_rate_on_dcfc", "home_rate_away", "estimate_pending", "unpriced"],
  sessions: [
    { ...base, charge_id: "s1", kwh: 70, peak_kw: 210, cost: 7.7, price_per_kwh: 0.11, kind: "dcfc",
      cost_source: "home_rate_misapplied", flags: ["home_rate_on_dcfc", "home_rate_away", "estimate_pending"] },
    { ...base, charge_id: "s2", kwh: 50, peak_kw: 180, cost: 30, price_per_kwh: 0.6, kind: "dcfc",
      cost_source: "rivian", network: "Rivian Adventure Network", flags: [] },
    { ...base, charge_id: "s3", kwh: 30, peak_kw: 1.4, cost: null, price_per_kwh: null, kind: "ac",
      cost_source: "unknown", flags: ["unpriced"] },
  ],
};

function renderRow(r: Receipt) {
  return render(
    <QueryClientProvider client={new QueryClient()}><MemoryRouter>
      <ul>
        <ReceiptRow r={r} currency="USD" />
      </ul>
    </MemoryRouter></QueryClientProvider>,
  );
}

describe("mixed receipt (contract §10)", () => {
  afterEach(cleanup);

  it("isn't 'likely wrong' just because its pending DCFC carries home_rate_away", () => {
    expect(grammarFor(mixed)).toBe("recorded");
    expect(needsPrice(mixed)).toBe(true);
    expect(sourceLabel(mixed, "USD")).toBe("Mixed prices · part pending, part unpriced");
  });

  it("shows the billed part and links Add price to the first session needing one", () => {
    renderRow(mixed);
    expect(screen.getByText("$30.00")).toBeInTheDocument();
    const add = screen.getByRole("link", { name: "Add price →" });
    expect(add).toHaveAttribute("href", "/charges/s1");
    expect(screen.queryByText("Fix price →")).toBeNull();
    // Rounded distance, no precise location.
    expect(screen.getByText("255 mi from home")).toBeInTheDocument();
  });

  it("offers Fix price for a home-rate session away from home that's in the total", () => {
    const away: Receipt = {
      ...mixed,
      receipt_id: "rcpt_a1",
      kind: "ac",
      peak_kw: 1.4,
      cost: 3.3,
      cost_source: "home_rate",
      rate: 0.11,
      session_count: 1,
      unpriced_sessions: 0,
      flags: ["home_rate_away"],
      sessions: [
        { ...base, charge_id: "a1", kwh: 30, peak_kw: 1.4, cost: 3.3, price_per_kwh: 0.11, kind: "ac",
          cost_source: "home_rate", flags: ["home_rate_away"] },
      ],
    };
    expect(grammarFor(away)).toBe("suspect");
    renderRow(away);
    expect(screen.getByRole("link", { name: "Fix price →" })).toHaveAttribute("href", "/charges/a1");
  });
});
