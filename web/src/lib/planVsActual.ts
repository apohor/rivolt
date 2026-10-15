// Pure presentation model for Plan vs Actual (contract v1). Shows a
// difference only where the server gave one (it's null when either side
// is unknown), and never a total that is incomplete.

import type { PlanVsActual, PvaCandidate, PvaMetric, PvaStop } from "./api";

export type PvaState = "none" | "suggested" | "linked-full" | "linked-partial";

export function costMetric(p: PlanVsActual): PvaMetric | null {
  return p.comparison?.metrics.find((m) => m.metric === "cost") ?? null;
}

export function stateOf(p: PlanVsActual): PvaState {
  if (p.status === "none" || !p.comparison) {
    return p.status === "suggested" && p.candidates.length > 0 ? "suggested" : "none";
  }
  if (p.status === "suggested") return "suggested";
  const c = costMetric(p);
  return c && c.actual_complete === false ? "linked-partial" : "linked-full";
}

// The candidate a suggestion previews: the top (highest-confidence) one.
export function topCandidate(p: PlanVsActual): PvaCandidate | null {
  return p.candidates[0] ?? null;
}

const LABEL: Record<PvaMetric["metric"], string> = {
  cost: "Fast-charging cost",
  energy_kwh: "Energy used",
  dcfc_stops: "Fast-charging stops",
  arrival_soc: "Arrival battery",
  duration: "Duration",
};

export type Row = {
  key: PvaMetric["metric"];
  label: string;
  planned: string | null;
  actual: string | null;
  deltaText: string | null;
  // "good" = cheaper/quicker is just fine; "watch" = more cost/time.
  // Stops and arrival battery are neutral: an extra stop isn't a fault.
  tone: "good" | "watch" | "neutral";
  // Cost only.
  estimated: boolean;
  incomplete: { known: number; unpriced: number } | null;
  // Why it differed, most significant first.
  reasons: string[];
};

function money(v: number, cur: string): string {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: cur || "USD" }).format(v);
  } catch {
    return `${v.toFixed(2)} ${cur}`;
  }
}

export function hm(min: number): string {
  const h = Math.floor(min / 60);
  const m = Math.round(min % 60);
  return h > 0 ? `${h} h ${m} m` : `${m} min`;
}

function fmt(m: PvaMetric, v: number | null, cur: string): string | null {
  if (v == null) return null;
  switch (m.metric) {
    case "cost":
      return money(v, cur);
    case "energy_kwh":
      return `${v.toFixed(0)} kWh`;
    case "dcfc_stops":
      return `${v}`;
    case "arrival_soc":
      return `${Math.round(v)}%`;
    case "duration":
      return hm(v);
  }
}

function signed(n: number, unit: string, digits = 0): string {
  const s = n > 0 ? "+" : n < 0 ? "−" : "";
  return `${s}${Math.abs(n).toFixed(digits)}${unit}`;
}

function deltaText(m: PvaMetric, cur: string): string | null {
  if (m.delta == null || m.delta === 0) return null;
  switch (m.metric) {
    case "cost":
      return `${m.delta > 0 ? "+" : "−"}${money(Math.abs(m.delta), cur)}`;
    case "energy_kwh":
      return signed(m.delta, " kWh");
    case "dcfc_stops":
      return signed(m.delta, "");
    case "arrival_soc":
      return signed(m.delta, " pts");
    case "duration":
      return signed(m.delta, " min");
  }
}

function toneOf(m: PvaMetric): Row["tone"] {
  if (m.delta == null || m.delta === 0) return "neutral";
  if (m.metric === "cost" || m.metric === "duration") return m.delta < 0 ? "good" : "watch";
  return "neutral";
}

// Copy for reason codes that need no placeholder, used only when the
// server sent no `detail` (PM copy, plan-vs-actual-copy.md). Codes that
// need a network, rate or stop name render only from the server's detail.
const CODE_COPY: Record<string, string> = {
  extra_stop: "You made an extra fast-charge stop.",
  skipped_stop: "You skipped a planned stop.",
  estimated: "Part of this total is estimated from the network's price, not billed.",
  unpriced: "Some charges aren't priced yet, so the total is incomplete.",
  energy_partial: "One drive wasn't fully recorded, so energy may be off.",
};

// "Mostly because …": at most two reasons, largest amount first,
// informational (no amount) after. A line is dropped when there's
// neither a server detail nor placeholder-free copy.
function reasonsOf(m: PvaMetric): string[] {
  const text = (r: PvaMetric["reasons"][number]) => r.detail || CODE_COPY[r.code] || null;
  const have = m.reasons.map((r) => ({ r, t: text(r) })).filter((x) => x.t);
  const amount = have
    .filter((x) => x.r.amount != null)
    .sort((a, b) => Math.abs(b.r.amount ?? 0) - Math.abs(a.r.amount ?? 0));
  const info = have.filter((x) => x.r.amount == null);
  return [...amount, ...info].slice(0, 2).map((x) => x.t as string);
}

// costHeader is the one-line verdict above the table (PM copy). Null
// when there is nothing honest to say yet.
export function costHeader(c: NonNullable<PlanVsActual["comparison"]>): string | null {
  const m = c.metrics.find((x) => x.metric === "cost");
  if (!m) return null;
  if (m.reasons.some((r) => r.code === "currency_mismatch")) {
    return "Can't compare. The plan and your charges use different currencies.";
  }
  if (m.actual_complete === false) {
    const known = m.actual_known != null ? `${money(m.actual_known, c.currency)} so far; ` : "";
    return `Not final yet. ${known}${m.unpriced_sessions ?? 0} charges aren't priced yet.`;
  }
  if (m.actual == null) return null;
  const paid = `${money(m.actual, c.currency)}${(m.actual_sources?.network_estimate ?? 0) > 0 ? " (estimated)" : ""}`;
  if (m.planned == null) return `No cost in the plan. You paid ${paid}.`;
  const within = m.planned > 0 && Math.abs(m.actual - m.planned) / m.planned <= 0.1;
  if (within) return `On plan. Planned ${money(m.planned, c.currency)}, paid ${paid}.`;
  const d = money(Math.abs(m.actual - m.planned), c.currency);
  return `${d} ${m.actual > m.planned ? "over" : "under"} plan. Planned ${money(m.planned, c.currency)}, paid ${paid}.`;
}

export function rows(c: NonNullable<PlanVsActual["comparison"]>): Row[] {
  return c.metrics.map((m) => {
    const src = m.actual_sources ?? {};
    const estimated = m.metric === "cost" && (src.network_estimate ?? 0) > 0;
    const incomplete =
      m.metric === "cost" && m.actual_complete === false
        ? { known: m.actual_known ?? 0, unpriced: m.unpriced_sessions ?? 0 }
        : null;
    return {
      key: m.metric,
      label: LABEL[m.metric],
      planned: fmt(m, m.planned, c.currency),
      // An incomplete cost is never shown as "the actual".
      actual: incomplete ? null : fmt(m, m.actual, c.currency),
      deltaText: incomplete ? null : deltaText(m, c.currency),
      tone: incomplete ? "neutral" : toneOf(m),
      estimated,
      incomplete,
      reasons: incomplete ? [] : reasonsOf(m),
    };
  });
}

export const STOP_LABEL: Record<PvaStop["match"], string> = {
  same_station: "Same station",
  same_network: "Same network, different station",
  other_network: "Different network",
  skipped: "Skipped",
  extra: "Extra stop",
};

export function drivenText(c: NonNullable<PlanVsActual["comparison"]>, miles?: number | null): string {
  const d = new Date(c.leg.started_at);
  const when = Number.isNaN(d.getTime()) ? "" : d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return `Driven ${when}${miles ? ` · ${Math.round(miles)} mi` : ""}`.trim();
}
