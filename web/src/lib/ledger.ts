// Ledger client + types for the Money tab, per ledger contract v1
// (frozen; additive changes only).
//
// The server owns every money and energy total. The page renders the
// numbers it gets; it doesn't re-sum receipts into totals. Money is
// the *charging* ledger (what was paid for energy). Driving cost is
// shown only as the server's spend ÷ miles, never as a second sum.

import { useEffect, useState } from "react";
import { api, ApiError } from "./api";

export const LEDGER_CONTRACT_VERSION = 1;

// ---- types (contract §3–§4) -----------------------------------------

// v1 cost sources. `rivian` really means "a recorded price that isn't
// the current home rate" (Rivian-billed, a manual edit, or an older
// home rate), so the UI labels it "recorded price", not "Rivian".
export type CostSource = "rivian" | "home_rate" | "unknown";
export type ReceiptCostSource = CostSource | "mixed";

export type ChargeKind = "ac" | "dcfc";
export type Place = "home" | "away" | "unknown";

export type ReceiptFlag =
  | "fragments_folded"
  | "home_rate_on_dcfc"
  | "home_rate_away"
  | "unpriced"
  | "null_energy"
  | "open_session"
  | "abandoned"
  | "continues_next_month"
  | "continued_from_previous_month"
  | "currency_mismatch";

export type ReceiptSession = {
  charge_id: string;
  started_at: string;
  ended_at: string;
  soc_start: number | null;
  soc_end: number | null;
  kwh: number | null;
  peak_kw: number | null;
  cost: number | null;
  price_per_kwh: number | null;
  cost_source: CostSource;
  final_state: string;
  source: string;
  flags: ReceiptFlag[];
};

export type Receipt = {
  receipt_id: string;
  vehicle_id: string;
  kind: ChargeKind;
  place: Place;
  km_from_home: number | null;
  started_at: string;
  ended_at: string;
  local_date: string;
  spans_days: number;
  soc_start: number | null;
  soc_end: number | null;
  kwh: number;
  peak_kw: number | null;
  cost: number | null;
  cost_source: ReceiptCostSource;
  rate: number | null;
  session_count: number;
  unpriced_sessions: number;
  flags: ReceiptFlag[];
  sessions: ReceiptSession[];
};

export type KindTotals = {
  sessions: number;
  kwh: number;
  cost: number;
  peak_kw: number | null;
};

export type MonthSummary = {
  version: number;
  period: string;
  tz: string;
  tz_source: "query" | "default_utc";
  range: { start: string; end: string };
  currency: string;
  vehicle_id: string | null;
  is_current_month: boolean;
  first_activity_at: string | null;
  last_activity_at: string | null;
  spend: {
    total: number;
    by_source: { rivian: number; home_rate: number };
    priced_sessions: number;
    unpriced_sessions: number;
    unpriced_kwh: number;
    unpriced_home_rate_estimate: number | null;
  };
  energy: {
    sessions: number;
    kwh_added: number;
    null_energy_sessions: number;
    by_kind: Record<ChargeKind, KindTotals>;
  };
  driving: {
    drives: number;
    miles: number;
    miles_with_energy: number;
    kwh_used_known: number;
    coverage: number | null;
    cost_per_mile: number | null;
    cost_per_mile_basis: "spend_over_miles";
  };
  data_quality: {
    cost_source_inferred: boolean;
    superseded_excluded: number;
    drives_superseded_excluded: number;
    open_sessions: number;
    home_rate_on_dcfc: number;
    home_rate_away: number;
    currency_mismatch_excluded: number;
  };
};

export type LedgerMonth = MonthSummary & {
  receipts: Receipt[];
  data_quality: MonthSummary["data_quality"] & {
    superseded: { charge_id: string; superseded_by: string }[];
  };
};

export type LedgerSummary = {
  version: number;
  tz: string;
  currency: string;
  months: MonthSummary[];
};

export type Sourced<T> = { data: T; source: "api" | "fixture" };

// ---- feature flag (contract §1) -------------------------------------

// money_tab_enabled is a per-user flag, so it can't ride on the
// install-wide /api/config the trip-planner flag uses. Discovery is
// GET /api/ledger/enabled (200 → on, 404 → off). Same hook shape as
// useTripPlannerEnabled: one fetch per page load, module-cached,
// fail-closed (false) until it resolves, re-render when it does.
let flagCache: boolean | null = null;
let flagInflight: Promise<boolean> | null = null;

export function loadMoneyTabEnabled(): Promise<boolean> {
  if (!flagInflight) {
    flagInflight = api
      .get<{ enabled?: boolean }>("/api/ledger/enabled")
      .then((r) => !!r?.enabled)
      .catch(() => false)
      .then((v) => {
        flagCache = v;
        return v;
      });
  }
  return flagInflight;
}

export function useMoneyTabEnabled(): boolean {
  const [enabled, setEnabled] = useState<boolean>(flagCache ?? false);
  useEffect(() => {
    let cancelled = false;
    loadMoneyTabEnabled().then((v) => {
      if (!cancelled) setEnabled(v);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return enabled;
}

// Test-only: reset the module cache between cases.
export function _resetMoneyTabFlagForTests() {
  flagCache = null;
  flagInflight = null;
}

// ---- fetchers -------------------------------------------------------

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function query(params: Record<string, string | undefined>): string {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) qs.set(k, v);
  return qs.toString();
}

// The ledger routes 404 when the flag is off *or* before the backend
// ships them. `allowFixture` (?fixture=1, or the backend not deployed
// yet) swaps in the typed fixture and labels the result so the page
// shows a "Sample data" banner rather than passing it off as real.
function isMissingEndpoint(e: unknown): boolean {
  return e instanceof ApiError && (e.status === 404 || e.status === 501);
}

export async function fetchLedgerMonth(
  month: string,
  opts: { vehicleID?: string; tz?: string; fixture?: boolean; signal?: AbortSignal } = {},
): Promise<Sourced<LedgerMonth>> {
  if (opts.fixture) return { data: ledgerMonthFixture(month), source: "fixture" };
  try {
    const data = await api.get<LedgerMonth>(
      `/api/ledger/monthly?${query({ month, tz: opts.tz ?? browserTimeZone(), vehicle_id: opts.vehicleID })}`,
      opts.signal,
    );
    return { data, source: "api" };
  } catch (e) {
    if (isMissingEndpoint(e)) return { data: ledgerMonthFixture(month), source: "fixture" };
    throw e;
  }
}

export async function fetchLedgerSummary(
  opts: { months?: number; vehicleID?: string; tz?: string; fixture?: boolean; signal?: AbortSignal } = {},
): Promise<Sourced<LedgerSummary>> {
  const months = opts.months ?? 12;
  if (opts.fixture) return { data: ledgerSummaryFixture(months), source: "fixture" };
  try {
    const data = await api.get<LedgerSummary>(
      `/api/ledger/summary?${query({ months: String(months), tz: opts.tz ?? browserTimeZone(), vehicle_id: opts.vehicleID })}`,
      opts.signal,
    );
    return { data, source: "api" };
  } catch (e) {
    if (isMissingEndpoint(e)) return { data: ledgerSummaryFixture(months), source: "fixture" };
    throw e;
  }
}

// ---- period helpers -------------------------------------------------

export function currentMonth(now = new Date()): string {
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;
}

export function shiftMonth(month: string, delta: number): string {
  const [y, m] = month.split("-").map(Number);
  const d = new Date(y, m - 1 + delta, 1);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
}

export function monthLabel(month: string): string {
  const [y, m] = month.split("-").map(Number);
  return new Date(y, m - 1, 1).toLocaleDateString(undefined, { month: "long", year: "numeric" });
}

export function isValidMonth(s: string | null | undefined): s is string {
  return !!s && /^\d{4}-(0[1-9]|1[0-2])$/.test(s);
}

// ---- presentation helpers -------------------------------------------

export function formatMoney(v: number, currency: string, digits = 2): string {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || "USD",
      minimumFractionDigits: digits,
      maximumFractionDigits: digits,
    }).format(v);
  } catch {
    return `${v.toFixed(digits)} ${currency}`;
  }
}

// The three visual treatments (spec §4.2), mapped onto v1's sources:
//   recorded  - solid figure (home rate or a recorded/billed price)
//   suspect   - solid figure + amber "likely under-priced" (home rate
//               stamped on DCFC or away charging; contract §5)
//   unknown   - "—" + "Add price"
export type CostGrammar = "recorded" | "suspect" | "unknown";

const SUSPECT_FLAGS: ReceiptFlag[] = ["home_rate_on_dcfc", "home_rate_away"];

export function grammarFor(r: { cost: number | null; cost_source: ReceiptCostSource; flags: ReceiptFlag[] }): CostGrammar {
  if (r.cost == null || r.cost_source === "unknown") return "unknown";
  if (r.flags.some((f) => SUSPECT_FLAGS.includes(f))) return "suspect";
  return "recorded";
}

export function sourceLabel(
  r: { cost_source: ReceiptCostSource; rate: number | null; flags: ReceiptFlag[] },
  currency: string,
): string {
  const rate = r.rate != null ? ` · ${formatMoney(r.rate, currency, 2)}/kWh` : "";
  if (r.flags.includes("home_rate_on_dcfc")) return `Home rate on a fast charge${rate} · likely under-priced`;
  if (r.flags.includes("home_rate_away")) return `Home rate away from home${rate} · likely under-priced`;
  switch (r.cost_source) {
    case "home_rate":
      return `Home rate${rate}`;
    case "rivian":
      return `Recorded price${rate}`;
    case "mixed":
      return "Mixed prices";
    case "unknown":
      return "Price unknown";
  }
}

// homePublicSplit splits the server's AC total into home and public
// (away/unknown) using receipt places, but only when the receipts
// reconcile with energy.by_kind.ac. Otherwise it returns null and the
// page falls back to the server's own AC/DCFC split, so the bar can
// never disagree with the server's totals.
export type PlaceSplit = { home: KindTotals; public: KindTotals; dcfc: KindTotals };

export function homePublicSplit(m: LedgerMonth, tolerance = 0.05): PlaceSplit | null {
  const empty = (): KindTotals => ({ sessions: 0, kwh: 0, cost: 0, peak_kw: null });
  const home = empty();
  const pub = empty();
  for (const r of m.receipts) {
    if (r.kind !== "ac") continue;
    const b = r.place === "home" ? home : pub;
    b.sessions += r.session_count;
    b.kwh += r.kwh;
    b.cost += r.cost ?? 0;
    b.peak_kw = Math.max(b.peak_kw ?? 0, r.peak_kw ?? 0) || b.peak_kw;
  }
  const ac = m.energy.by_kind.ac;
  const ok =
    Math.abs(home.kwh + pub.kwh - ac.kwh) <= tolerance &&
    Math.abs(home.cost + pub.cost - ac.cost) <= tolerance &&
    home.sessions + pub.sessions === ac.sessions;
  return ok ? { home, public: pub, dcfc: m.energy.by_kind.dcfc } : null;
}

// suspectSessionCount counts sessions priced at the home rate while
// fast charging or away (contract §5). The two data_quality counters
// can overlap on the same session, so count from receipts and fall
// back to the larger counter when receipts aren't available.
export function suspectSessionCount(m: LedgerMonth): number {
  if (m.receipts.length === 0) return Math.max(m.data_quality.home_rate_on_dcfc, m.data_quality.home_rate_away);
  let n = 0;
  for (const r of m.receipts) {
    for (const x of r.sessions) {
      if (x.flags.some((f) => SUSPECT_FLAGS.includes(f))) n += 1;
    }
  }
  return n;
}

// Plain-English quality notes from data_quality + coverage (spec MQ*).
export function dataQualityNotes(m: LedgerMonth): string[] {
  const n: string[] = [];
  const dq = m.data_quality;
  const s = (k: number, one: string, many: string) => (k === 1 ? one : many);
  // A fast charge away from home can carry both flags, so count
  // sessions, not flag totals.
  const suspect = suspectSessionCount(m);
  if (suspect > 0) {
    n.push(
      `${suspect} ${s(suspect, "session was", "sessions were")} priced at your home rate while fast charging or away from home, so the total is likely too low. Add the real price to fix it.`,
    );
  }
  if (m.spend.unpriced_sessions > 0) {
    n.push(
      `${m.spend.unpriced_sessions} ${s(m.spend.unpriced_sessions, "session has", "sessions have")} no price and ${s(m.spend.unpriced_sessions, "isn't", "aren't")} in the total.`,
    );
  }
  if (dq.superseded_excluded > 0) {
    n.push(`${dq.superseded_excluded} duplicate charging ${s(dq.superseded_excluded, "session was", "sessions were")} excluded.`);
  }
  if (dq.drives_superseded_excluded > 0) {
    n.push(`${dq.drives_superseded_excluded} duplicate ${s(dq.drives_superseded_excluded, "drive was", "drives were")} excluded.`);
  }
  if (m.energy.null_energy_sessions > 0) {
    n.push(
      `${m.energy.null_energy_sessions} ${s(m.energy.null_energy_sessions, "session has", "sessions have")} no energy recorded and ${s(m.energy.null_energy_sessions, "is", "are")} left out of kWh totals.`,
    );
  }
  if (dq.open_sessions > 0) {
    n.push(`${dq.open_sessions} ${s(dq.open_sessions, "session is", "sessions are")} still charging; totals will change.`);
  }
  if (dq.currency_mismatch_excluded > 0) {
    n.push(`${dq.currency_mismatch_excluded} ${s(dq.currency_mismatch_excluded, "session", "sessions")} in another currency ${s(dq.currency_mismatch_excluded, "is", "are")} not in the total.`);
  }
  if (m.driving.coverage != null && m.driving.miles > 0 && m.driving.coverage < 0.9) {
    n.push(`Energy is known for ${Math.round(m.driving.coverage * 100)}% of miles driven.`);
  }
  if (dq.cost_source_inferred) {
    n.push("Price sources are inferred from your current home rate.");
  }
  return n;
}

// ---- fixtures -------------------------------------------------------

// A typed month shaped like the replay corpus: home L2 charges with
// folded top-ups, a DCFC road trip stamped at the home rate (the v1
// known limitation), two unpriced away sessions, a null-energy row
// and one dedupe. NO coordinates, addresses or place names: places
// are home/away and distances are rounded.
export function ledgerMonthFixture(month: string): LedgerMonth {
  const [y, mo] = month.split("-").map(Number);
  const tz = "America/Chicago";
  // Local CDT wall clock → UTC (fixture only; fixed -5h offset).
  const at = (day: number, h: number, min = 0) =>
    new Date(Date.UTC(y, mo - 1, day, h + 5, min)).toISOString();
  const homeRate = 0.11;
  const vid = "01-000000000";
  let seq = 0;
  const sess = (
    start: string,
    end: string,
    o: Partial<ReceiptSession> & Pick<ReceiptSession, "kwh" | "peak_kw" | "soc_start" | "soc_end">,
  ): ReceiptSession => {
    seq += 1;
    const cost = o.cost !== undefined ? o.cost : o.kwh != null ? +(o.kwh * homeRate).toFixed(4) : null;
    return {
      charge_id: `live_${vid}_c_fx${String(seq).padStart(3, "0")}`,
      started_at: start,
      ended_at: end,
      price_per_kwh: cost != null ? (o.price_per_kwh ?? homeRate) : null,
      cost_source: cost == null ? "unknown" : (o.cost_source ?? "home_rate"),
      final_state: "charging_complete",
      source: "live",
      flags: [],
      ...o,
      cost,
    };
  };
  const receipt = (
    place: Place,
    km: number | null,
    sessions: ReceiptSession[],
    extraFlags: ReceiptFlag[] = [],
  ): Receipt => {
    const first = sessions[0];
    const last = sessions[sessions.length - 1];
    const priced = sessions.filter((x) => x.cost != null);
    const cost = priced.length ? priced.reduce((a, x) => a + (x.cost ?? 0), 0) : null;
    const pricedKwh = priced.reduce((a, x) => a + (x.kwh ?? 0), 0);
    const sources = new Set(sessions.map((x) => x.cost_source));
    const peak = Math.max(...sessions.map((x) => x.peak_kw ?? 0));
    const flags = new Set<ReceiptFlag>(extraFlags);
    if (sessions.length > 1) flags.add("fragments_folded");
    for (const x of sessions) x.flags.forEach((f) => flags.add(f));
    if (!priced.length) flags.add("unpriced");
    const startDay = new Date(first.started_at);
    const endDay = new Date(last.ended_at);
    const days = Math.max(1, Math.round((Date.UTC(endDay.getUTCFullYear(), endDay.getUTCMonth(), endDay.getUTCDate()) - Date.UTC(startDay.getUTCFullYear(), startDay.getUTCMonth(), startDay.getUTCDate())) / 86_400_000) + 1);
    return {
      receipt_id: `rcpt_${first.charge_id}`,
      vehicle_id: vid,
      kind: peak >= 50 ? "dcfc" : "ac",
      place,
      km_from_home: km,
      started_at: first.started_at,
      ended_at: last.ended_at,
      local_date: `${month}-${String(new Date(new Date(first.started_at).getTime() - 5 * 3_600_000).getUTCDate()).padStart(2, "0")}`,
      spans_days: days,
      soc_start: first.soc_start,
      soc_end: last.soc_end,
      kwh: sessions.reduce((a, x) => a + (x.kwh ?? 0), 0),
      peak_kw: peak || null,
      cost,
      cost_source: sources.size === 1 ? [...sources][0] : "mixed",
      rate: cost != null && pricedKwh > 0 ? cost / pricedKwh : null,
      session_count: sessions.length,
      unpriced_sessions: sessions.length - priced.length,
      flags: [...flags],
      sessions,
    };
  };

  const onDcfc: ReceiptFlag[] = ["home_rate_on_dcfc", "home_rate_away"];
  const receipts: Receipt[] = [
    receipt("home", 0, [
      sess(at(2, 22), at(3, 4, 34), { kwh: 44.3, peak_kw: 7.6, soc_start: 27.4, soc_end: 62.8 }),
      sess(at(3, 4, 45), at(3, 6, 5), { kwh: 8.6, peak_kw: 7.6, soc_start: 62.8, soc_end: 70 }),
    ]),
    receipt("home", 0, [sess(at(8, 19, 13), at(9, 3, 15), { kwh: 51.8, peak_kw: 7.6, soc_start: 28.1, soc_end: 70 })]),
    receipt("home", 0, [
      sess(at(12, 7, 10), at(12, 7, 40), { kwh: null, peak_kw: 7.5, soc_start: 64, soc_end: 66, flags: ["null_energy"] }),
    ]),
    receipt("home", 0, [
      sess(at(17, 17, 34), at(17, 21, 42), { kwh: 26.0, peak_kw: 7.6, soc_start: 20.6, soc_end: 42.2 }),
      sess(at(17, 21, 54), at(18, 3, 11), { kwh: 34.1, peak_kw: 7.6, soc_start: 42.2, soc_end: 69.9 }),
      ...[18, 19, 19, 19, 20, 20, 20, 20].map((d, i) =>
        sess(at(d, [23, 5, 13, 19, 0, 4, 10, 15][i], 10), at(d, [23, 5, 13, 19, 0, 4, 10, 15][i], 20), {
          kwh: 0.6, peak_kw: 7.5, soc_start: 69.4, soc_end: 70,
        }),
      ),
    ]),
    receipt("home", 0, [sess(at(26, 20, 5), at(27, 7, 0), { kwh: 72.8, peak_kw: 7.6, soc_start: 38.4, soc_end: 96.8 })]),
    receipt("away", 300, [sess(at(27, 10, 28), at(27, 11, 11), { kwh: 83.9, peak_kw: 211.4, soc_start: 17.2, soc_end: 85, flags: [...onDcfc] })]),
    receipt("away", 530, [sess(at(27, 13, 35), at(27, 14, 15), { kwh: 64.2, peak_kw: 128.5, soc_start: 19.6, soc_end: 71.9, flags: [...onDcfc] })]),
    receipt("away", 640, [sess(at(27, 21, 41), at(28, 11, 0), { kwh: 15.5, peak_kw: 1.4, soc_start: 37.8, soc_end: 50.4, cost: null })]),
    receipt("away", 640, [sess(at(28, 18, 31), at(30, 8, 2), { kwh: 38.2, peak_kw: 1.4, soc_start: 27.2, soc_end: 58.3, cost: null })]),
    receipt("away", 530, [sess(at(30, 11, 46), at(30, 12, 16), { kwh: 62.9, peak_kw: 179.6, soc_start: 22.3, soc_end: 73, cost: 37.74, price_per_kwh: 0.6, cost_source: "rivian" })]),
    receipt("away", 300, [sess(at(30, 14, 50), at(30, 15, 29), { kwh: 86.4, peak_kw: 215.2, soc_start: 2.6, soc_end: 72.8, flags: [...onDcfc] })]),
    receipt("home", 0, [sess(at(30, 19, 50), at(30, 23, 59), { kwh: 45.6, peak_kw: 7.6, soc_start: 11.7, soc_end: 49.4 })], ["continues_next_month"]),
  ];
  receipts.sort((a, b) => b.started_at.localeCompare(a.started_at));

  // Totals computed once here, the way the server would.
  const all = receipts.flatMap((r) => r.sessions.map((x) => ({ ...x, kind: r.kind })));
  const by_kind: Record<ChargeKind, KindTotals> = {
    ac: { sessions: 0, kwh: 0, cost: 0, peak_kw: null },
    dcfc: { sessions: 0, kwh: 0, cost: 0, peak_kw: null },
  };
  const by_source = { rivian: 0, home_rate: 0 };
  let priced = 0;
  let unpricedKwh = 0;
  let nullEnergy = 0;
  for (const x of all) {
    const k = by_kind[x.kind];
    k.sessions += 1;
    k.kwh += x.kwh ?? 0;
    k.cost += x.cost ?? 0;
    k.peak_kw = Math.max(k.peak_kw ?? 0, x.peak_kw ?? 0);
    if (x.kwh == null) nullEnergy += 1;
    if (x.cost == null) {
      unpricedKwh += x.kwh ?? 0;
      continue;
    }
    priced += 1;
    if (x.cost_source === "rivian") by_source.rivian += x.cost;
    else by_source.home_rate += x.cost;
  }
  const total = by_source.rivian + by_source.home_rate;
  const miles = 1284.3;
  const now = new Date();
  return {
    version: LEDGER_CONTRACT_VERSION,
    period: month,
    tz,
    tz_source: "query",
    range: { start: new Date(Date.UTC(y, mo - 1, 1, 5)).toISOString(), end: new Date(Date.UTC(y, mo, 1, 5)).toISOString() },
    currency: "USD",
    vehicle_id: null,
    is_current_month: month === currentMonth(now),
    first_activity_at: at(2, 7, 31),
    last_activity_at: at(30, 19, 50),
    spend: {
      total,
      by_source,
      priced_sessions: priced,
      unpriced_sessions: all.length - priced,
      unpriced_kwh: unpricedKwh,
      unpriced_home_rate_estimate: +(unpricedKwh * homeRate).toFixed(4),
    },
    energy: { sessions: all.length, kwh_added: by_kind.ac.kwh + by_kind.dcfc.kwh, null_energy_sessions: nullEnergy, by_kind },
    driving: {
      drives: 61,
      miles,
      miles_with_energy: 1278.0,
      kwh_used_known: 560.1,
      coverage: 1278.0 / miles,
      cost_per_mile: total / miles,
      cost_per_mile_basis: "spend_over_miles",
    },
    receipts,
    data_quality: {
      cost_source_inferred: true,
      superseded_excluded: 1,
      superseded: [{ charge_id: `live_${vid}_c_fx900`, superseded_by: `live_${vid}_c_fx002` }],
      drives_superseded_excluded: 0,
      open_sessions: 0,
      home_rate_on_dcfc: 3,
      home_rate_away: 3,
      currency_mismatch_excluded: 0,
    },
  };
}

export function ledgerSummaryFixture(months: number, now = new Date()): LedgerSummary {
  const cur = currentMonth(now);
  const list: MonthSummary[] = [];
  for (let i = months - 1; i >= 0; i--) {
    const m = shiftMonth(cur, -i);
    // Only the last two months have "data" in the fixture.
    if (i > 1) continue;
    const full = ledgerMonthFixture(m);
    const { receipts: _r, data_quality: { superseded: _s, ...dq }, ...rest } = full;
    list.push({ ...rest, data_quality: dq, spend: i === 1 ? { ...rest.spend, total: rest.spend.total * 0.62 } : rest.spend });
  }
  return { version: LEDGER_CONTRACT_VERSION, tz: "America/Chicago", currency: "USD", months: list };
}
