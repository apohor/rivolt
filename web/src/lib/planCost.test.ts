import { describe, expect, it } from "vitest";
import type { TripCostEstimate } from "./api";
import { comparableSpend, estimateLabel, fmtRange, isOlderEstimate, safeTrackerUrl, spendView, stopCostText } from "./planCost";

const base = { currency: "USD", home_equivalent: 0, gas_equivalent: 0, gas_price_per_gallon: 0, gas_mpg: 0, home_rate_used: 0 };
const est = (o: Partial<TripCostEstimate>): TripCostEstimate => ({ ...base, ...o }) as TripCostEstimate;
const usd = (v: number) => `$${v.toFixed(2)}`;

describe("planCost", () => {
  it("a range collapses to one value when the ends match", () => {
    expect(fmtRange(10, 10, usd)).toBe("$10.00");
    expect(fmtRange(10, 12.5, usd)).toBe("$10.00–$12.50");
  });
  it("a stop with no price has no cost text, never $0", () => {
    expect(stopCostText({ network_slug: "x", network_name: "X", charger_name: "c", energy_kwh: 10, price: null, cost_low: null, cost_high: null }, usd)).toBeNull();
    expect(stopCostText({ network_slug: "x", network_name: "X", charger_name: "c", energy_kwh: 10, cost_low: 5, cost_high: 7 }, usd)).toBe("$5.00–$7.00");
  });
  it("spendView: none, all unpriced, a range, and a partial range", () => {
    expect(spendView(est({ estimate_basis: "none", priced_stops: 0, unpriced_stops: 0 })).kind).toBe("none");
    expect(spendView(est({ estimate_basis: "none", priced_stops: 0, unpriced_stops: 2 }))).toEqual({ kind: "unpriced", stops: 2 });
    expect(spendView(est({ estimate_basis: "observed", priced_stops: 2, unpriced_stops: 1, dcfc_spend_low: 10, dcfc_spend_high: 14 }))).toMatchObject({ kind: "range", low: 10, high: 14, priced: 2, unpriced: 1, older: false });
  });
  it("a plan saved before ranges is an older estimate, with its old figure", () => {
    const old = est({ dcfc_spend: 14.4 });
    expect(isOlderEstimate(old)).toBe(true);
    expect(spendView(old)).toMatchObject({ kind: "range", low: 14.4, high: 14.4, older: true });
    expect(isOlderEstimate(est({ estimate_basis: "observed" }))).toBe(false);
  });
  it("observed and mixed say 'typical observed price'; the user's own rate says plain estimate", () => {
    expect(estimateLabel(est({ estimate_basis: "observed" }))).toBe("estimate (typical observed price)");
    expect(estimateLabel(est({ estimate_basis: "mixed" }))).toBe("estimate (typical observed price)");
    expect(estimateLabel(est({ estimate_basis: "your_setting" }))).toBe("estimate");
    expect(estimateLabel(est({ estimate_basis: "your_receipts" }))).toBe("estimate");
  });
  it("routes are compared only when both are complete estimates", () => {
    expect(comparableSpend(est({ estimate_basis: "observed", priced_stops: 1, unpriced_stops: 0, dcfc_spend_low: 10, dcfc_spend_high: 14 }))).toBe(12);
    expect(comparableSpend(est({ estimate_basis: "observed", priced_stops: 1, unpriced_stops: 1, dcfc_spend_low: 10, dcfc_spend_high: 14 }))).toBeNull();
    expect(comparableSpend(est({ dcfc_spend: 14 }))).toBeNull();
    expect(comparableSpend(est({ estimate_basis: "none", priced_stops: 0, unpriced_stops: 0 }))).toBe(0);
  });

  it("only the tracker's own https host becomes a link", () => {
    expect(safeTrackerUrl("https://dcfctracker.com/reports/tesla")).not.toBeNull();
    for (const bad of ["javascript:alert(1)", "//evil.example/x", "http://dcfctracker.com/x", "https://dcfctracker.com.evil.example/x", "https://evil.example/dcfctracker.com/", "data:text/html,x", "", undefined, null]) {
      expect(safeTrackerUrl(bad as string | undefined | null)).toBeNull();
    }
  });
});
