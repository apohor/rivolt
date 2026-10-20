import type { TripCostEstimate, TripCostStop } from "./api";

// The planner's network-rate costs. A stop's cost is a range from its price (or null: "price shown at the charger", never
// $0); the route total sums the priced stops and says how many were left out. A plan saved before ranges has only the old
// dcfc_spend and no estimate_basis: it is shown as an older estimate.

export const NO_PRICE_TEXT = "Price shown at the charger.";

export const isOlderEstimate = (c: TripCostEstimate | undefined): boolean => !!c && c.estimate_basis === undefined;

export type SpendView =
  | { kind: "none" } // no fast-charging stops
  | { kind: "unpriced"; stops: number } // stops, but none has a price
  | { kind: "range"; low: number; high: number; priced: number; unpriced: number; older: boolean };

export function spendView(c: TripCostEstimate | undefined): SpendView {
  if (!c) return { kind: "none" };
  if (isOlderEstimate(c)) {
    const v = c.dcfc_spend ?? 0;
    return v > 0 ? { kind: "range", low: v, high: v, priced: 0, unpriced: 0, older: true } : { kind: "none" };
  }
  const priced = c.priced_stops ?? 0;
  const unpriced = c.unpriced_stops ?? 0;
  if (priced === 0) return unpriced > 0 ? { kind: "unpriced", stops: unpriced } : { kind: "none" };
  return { kind: "range", low: c.dcfc_spend_low ?? 0, high: c.dcfc_spend_high ?? c.dcfc_spend_low ?? 0, priced, unpriced, older: false };
}

// One value when the ends round to the same amount, else "low–high".
export function fmtRange(low: number, high: number, fmt: (v: number) => string): string {
  const a = fmt(low);
  const b = fmt(high);
  return a === b ? a : `${a}–${b}`;
}

// "estimate (typical observed price)" when any stop's price is the tracker's; plain "estimate" for the user's own rate or receipts.
export function estimateLabel(c: TripCostEstimate): string {
  return c.estimate_basis === "observed" || c.estimate_basis === "mixed" ? "estimate (typical observed price)" : "estimate";
}

// A stop's cost as text: a range from its price, or null when it has none.
export function stopCostText(s: TripCostStop | undefined, fmt: (v: number) => string): string | null {
  if (!s || s.cost_low == null || s.cost_high == null) return null;
  return fmtRange(s.cost_low, s.cost_high, fmt);
}

// The midpoint of a complete estimate, for comparing routes; null when any stop is unpriced or the plan is an older one.
export function comparableSpend(c: TripCostEstimate | undefined): number | null {
  const v = spendView(c);
  if (v.kind === "none") return 0;
  if (v.kind === "unpriced" || v.older || v.unpriced > 0) return null;
  return (v.low + v.high) / 2;
}

// "1 of 3 stops priced" under a partial total.
export const partialText = (priced: number, unpriced: number): string =>
  `${priced} of ${priced + unpriced} ${priced + unpriced === 1 ? "stop" : "stops"} priced; the rest show their price at the charger`;

// The price's report link comes from plan JSON, which a saved trip or a restore can carry: only the tracker's own https host
// becomes a link; anything else shows the label as plain text.
export const safeTrackerUrl = (u: string | undefined | null): string | null => (u && /^https:\/\/dcfctracker\.com\//.test(u) ? u : null);
