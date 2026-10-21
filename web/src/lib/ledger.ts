// Ledger client + types for the Money tab, per ledger contract v1
// (incl. the additive §9/§10 changes: persisted pricing, by_place,
// per-session kind/place).
//
// The server owns every money and energy total. The page renders the
// numbers it gets; it never re-sums receipts into totals or splits.
// Money is the *charging* ledger (what was paid for energy). Driving
// cost is shown only as the server's spend ÷ miles.

import { setServerUiNext } from "./uiNext";
import { hasOpenNetworkState, type NetworkState } from "./autoNetwork";
import { DEMO_TZ, inDemo } from "./demo";
import { cacheShellFlag } from "./theme";
import { useEffect, useState } from "react";
import { api, ApiError } from "./api";

export const LEDGER_CONTRACT_VERSION = 1;

// ---- types (contract §3, §4, §10) -----------------------------------

// Session cost sources (contract §10.1).
//   rivian               Rivian-billed, or a legacy non-home-rate price. Label "Billed".
//   home_rate            Home session at the user's home rate.
//   network_estimate     DCFC priced from the nearest station's network. An estimate.
//   user                 Entered by the user; cost 0 means "Free".
//   home_rate_misapplied Legacy: home rate stamped on a ≥ 50 kW session.
//                        Not in spend.total; shown as "estimate pending".
//   unknown              No cost.
export type CostSource =
  | "rivian"
  | "home_rate"
  | "network_estimate"
  | "user"
  | "home_rate_misapplied"
  | "observed_estimate"
  | "unknown";
export type ReceiptCostSource = CostSource | "mixed";

export type ChargeKind = "ac" | "dcfc";
export type Place = "home" | "away" | "unknown";

export type ReceiptFlag =
  | "fragments_folded"
  | "home_rate_on_dcfc"
  | "home_rate_away"
  | "estimate_pending"
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
  // §10 additions; optional so an older server build still parses.
  cost_source_derived?: boolean;
  kind?: ChargeKind;
  place?: Place;
  network?: string | null;
  network_state?: NetworkState | null;
  // rivian_receipts (per-user flag): what Rivian billed for the matched session.
  // Optional; absent on servers or users without the flag.
  billed_cost?: number | null;
  billed_currency?: string | null;
  billed_vendor?: string | null;
  billed_charger?: string | null;
  // observed_estimate: the price is the tracker's typical one for that month (estimate_url is the report). needs_rate: a
  // fast-charging session with no price, waiting on the user's own rate for rate_slug's network.
  estimate_month?: string | null;
  estimate_url?: string | null;
  needs_rate?: boolean;
  rate_slug?: string | null;
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
  network?: string | null;
  // Set while the auto-detected network still needs the owner's eye.
  network_state?: NetworkState | null;
  billed_cost?: number | null;
  billed_currency?: string | null;
  billed_vendor?: string | null;
  billed_charger?: string | null;
  estimate_month?: string | null;
  estimate_url?: string | null;
  needs_rate?: boolean;
  rate_slug?: string | null;
  // Sessions with and without a matched Rivian receipt; present only with the flag.
  billed_sessions?: number;
  estimated_sessions?: number;
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

export type SpendBySource = {
  rivian: number;
  home_rate: number;
  network_estimate: number;
  user: number;
};

// statementTotal is the month's headline figure. With Rivian receipts it is what Rivian
// billed plus our estimate for the rest, so it always equals the two lines under the
// headline; otherwise it is the statement total. The page and the share card both use it.
export function statementTotal(m: Pick<MonthSummary, "spend">): number {
  const { billed_total: b, estimated_total: e, total } = m.spend;
  return b != null && e != null ? b + e : total;
}

// spendThroughDay: a month's spend up to and including a day of the month, from its receipts
// (each receipt counts on the day it started). Used to compare a month in progress with the same
// stretch of the previous month. Unpriced receipts count as 0.
export function spendThroughDay(m: Pick<LedgerMonth, "receipts">, day: number): number {
  let sum = 0;
  for (const r of m.receipts) {
    const d = Number(r.local_date.slice(8, 10));
    if (Number.isFinite(d) && d <= day && r.cost != null) sum += r.cost;
  }
  return sum;
}

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
    // Older builds send only rivian/home_rate; read missing keys as 0.
    by_source: Partial<SpendBySource>;
    estimated_share?: number | null;
    priced_sessions: number;
    unpriced_sessions: number;
    unpriced_kwh: number;
    unpriced_home_rate_estimate: number | null;
    estimate_pending_sessions?: number;
    estimate_pending_kwh?: number;
    // Priced from the tracker's typical price: inside the totals but reported on their own, never blended.
    observed_estimate_cost?: number;
    observed_estimate_sessions?: number;
    unpriced_fast_sessions?: number;
    // rivian_receipts: the total split into what Rivian billed and what we estimate.
    billed_total?: number | null;
    estimated_total?: number | null;
  };
  // rivian_receipts: sessions with and without a matched Rivian receipt this month.
  billed_sessions?: number;
  estimated_sessions?: number;
  energy: {
    sessions: number;
    kwh_added: number;
    null_energy_sessions: number;
    by_kind: Record<ChargeKind, KindTotals>;
    // §10: per-session place split; reconciles with the totals.
    by_place?: Record<Place, KindTotals>;
    // §11: per-session place x kind cells, always all six when present.
    by_place_kind?: Record<Place, Record<ChargeKind, KindTotals>>;
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
    cost_source_derived_sessions?: number;
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
    // Omitted when empty (contract §9); treat missing as [].
    superseded?: { charge_id: string; superseded_by: string }[];
  };
};

export type LedgerSummary = {
  version: number;
  tz: string;
  currency: string;
  months: MonthSummary[];
  // DCFC auto-network: charges awaiting confirm / needing attention.
  // Null or absent until the dcfc_auto_network flag is on.
  to_confirm_count?: number | null;
  needs_attention_count?: number | null;
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
      .get<{ enabled?: boolean; ui_theme_next?: boolean }>("/api/ledger/enabled")
      .then((r) => {
        setServerUiNext(!!r?.ui_theme_next);
        return !!r?.enabled;
      })
      .catch(() => false)
      .then((v) => {
        flagCache = v;
        cacheShellFlag(v);
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
  // The public demo is fixed to the sample truck's zone (stable totals).
  if (inDemo()) return DEMO_TZ;
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

// Visual treatments (ux-slice-spec §4.2 mapped onto contract §10):
//   recorded  - solid figure (billed, home rate, entered)
//   free      - "Free" (user-entered $0)
//   estimated - dotted underline + "est." (network_estimate)
//   suspect   - solid + amber "likely wrong" (home rate stamped away from home)
//   pending   - "Pending" + Add price (home_rate_misapplied; not in total)
//   unknown   - "—" + Add price
export type CostGrammar = "recorded" | "free" | "estimated" | "suspect" | "pending" | "unknown";

type Priceable = {
  cost: number | null;
  cost_source: ReceiptCostSource;
  flags: ReceiptFlag[];
  network_state?: NetworkState | null;
  sessions?: Pick<ReceiptSession, "cost_source" | "flags" | "network_state">[];
};

// isSuspect: a session counted in the total at the home rate while
// away from home. On a receipt, look at its sessions: a misapplied
// DCFC session also carries home_rate_away (contract §10.3), but it's
// out of the total and must not make a billed receipt look wrong.
function isSuspect(r: Priceable): boolean {
  if (r.sessions) {
    return r.sessions.some((x) => x.cost_source === "home_rate" && x.flags.includes("home_rate_away"));
  }
  return r.cost_source === "home_rate" && r.flags.includes("home_rate_away");
}

// needsPrice: some session on the receipt has no usable price yet
// (unpriced, or a pending legacy DCFC stamp), even if other sessions
// are priced and the receipt shows a figure.
export function needsPrice(r: Pick<Receipt, "flags" | "sessions">): boolean {
  return (
    r.flags.includes("unpriced") ||
    r.flags.includes("estimate_pending") ||
    r.sessions.some((x) => x.cost == null || x.cost_source === "unknown" || x.cost_source === "home_rate_misapplied")
  );
}

// isConfirmedNetwork: the owner confirmed (or picked) the auto-detected
// network, so the price is no longer an estimate they should check.
export function isConfirmedNetwork(r: {
  cost_source: ReceiptCostSource;
  network_state?: NetworkState | null;
  sessions?: { cost_source?: string; network_state?: NetworkState | null }[];
}): boolean {
  if (r.network_state === "confirmed") return true;
  const est = (r.sessions ?? []).filter((x) => x.cost_source === "network_estimate");
  return r.cost_source === "network_estimate" && est.length > 0 && est.every((x) => x.network_state === "confirmed");
}

export function grammarFor(r: Priceable): CostGrammar {
  if (r.cost_source === "home_rate_misapplied") return "pending";
  if (r.cost == null || r.cost_source === "unknown") {
    return r.flags.includes("estimate_pending") ? "pending" : "unknown";
  }
  if (r.cost_source === "user" && r.cost === 0) return "free";
  if (r.cost_source === "observed_estimate") return "estimated";
  if (r.cost_source === "network_estimate") return isConfirmedNetwork(r) ? "recorded" : "estimated";
  if (r.cost_source === "mixed" && r.sessions?.some((x) => x.cost_source === "network_estimate" || x.cost_source === "observed_estimate")) {
    return "estimated";
  }
  if (isSuspect(r)) return "suspect";
  return "recorded";
}

export function sourceLabel(
  r: {
    cost: number | null;
    cost_source: ReceiptCostSource;
    rate: number | null;
    network?: string | null;
    flags: ReceiptFlag[];
    network_state?: NetworkState | null;
    sessions?: { network_state?: NetworkState | null }[];
    estimate_month?: string | null;
    needs_rate?: boolean;
  },
  currency: string,
): string {
  const rate = r.rate != null && r.rate > 0 ? ` · ${formatMoney(r.rate, currency, 2)}/kWh` : "";
  switch (r.cost_source) {
    case "rivian":
      return `Billed${rate}`;
    case "home_rate":
      return r.flags.includes("home_rate_away")
        ? `Home rate, but away from home${rate} · likely wrong`
        : `Home rate${rate}`;
    case "network_estimate":
      return `${hasOpenNetworkState(r) ? "Estimated, confirm" : isConfirmedNetwork(r) ? "Confirmed" : "Estimate"}${r.network ? ` · ${r.network}` : ""}${rate}`;
    case "observed_estimate":
      // Never an undated tracker figure: without a month it says so.
      return `${monthLabelShort(r.estimate_month) ? `Estimate: typical price seen by DCFC Tracker, ${monthLabelShort(r.estimate_month)}` : "Estimate (typical observed price, date unknown)"}${r.network ? ` · ${r.network}` : ""}${rate}`;
    case "user":
      return r.cost === 0 ? "Free · entered by you" : `Entered by you${rate}`;
    case "home_rate_misapplied":
      return "Estimate pending · fast charge was priced at your home rate";
    case "mixed": {
      const parts = [
        r.flags.includes("estimate_pending") ? "part pending" : null,
        r.flags.includes("unpriced") ? "part unpriced" : null,
      ].filter(Boolean);
      return parts.length ? `Mixed prices · ${parts.join(", ")}` : "Mixed prices";
    }
    case "unknown":
      return r.needs_rate ? "Needs a price" : "Price unknown";
  }
}

// "2026-10" -> "Oct 2026" (empty when the month isn't there).
export function monthLabelShort(ym: string | null | undefined): string {
  const m = ym ? /^(\d{4})-(\d{2})$/.exec(ym) : null;
  // A real month only: "2026-13" or "2026-00" is a malformed value, not a date ("Invalid Date" must never reach the page).
  if (!m || Number(m[2]) < 1 || Number(m[2]) > 12) return "";
  const d = new Date(`${ym}-15T12:00:00Z`);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleDateString("en-US", { month: "short", year: "numeric", timeZone: "UTC" });
}

export function bySource(m: MonthSummary): SpendBySource {
  const s = m.spend.by_source;
  return {
    rivian: s.rivian ?? 0,
    home_rate: s.home_rate ?? 0,
    network_estimate: s.network_estimate ?? 0,
    user: s.user ?? 0,
  };
}

// estimatedShare: server value when present, else derived from the
// server's own by_source (no receipt summing).
export function estimatedShare(m: MonthSummary): number {
  if (m.spend.estimated_share != null) return m.spend.estimated_share;
  return m.spend.total > 0 ? bySource(m).network_estimate / m.spend.total : 0;
}

// suspectSessionCount: sessions counted in the total at the home rate
// while away from home (contract §10.3 #2: session flags carry it).
// Misapplied DCFC sessions aren't counted here; they're "pending" and
// already out of the total.
export function suspectSessionCount(m: LedgerMonth): number {
  let n = 0;
  for (const r of m.receipts) {
    for (const x of r.sessions) {
      if (x.cost_source === "home_rate" && x.cost != null && x.flags.includes("home_rate_away")) n += 1;
    }
  }
  return n;
}

// unidentifiedPending: with DCFC auto-network on, home-rate DCFC
// sessions that now carry an estimate (network_state set) are folded into
// "N charges to confirm"; only the ones the matcher couldn't price stay
// "pending". Falls back to the server's counters when a month has no
// per-session rows.
export function unidentifiedPending(m: LedgerMonth): { sessions: number; kwh: number } {
  let sessions = 0;
  let kwh = 0;
  let seen = false;
  for (const r of m.receipts) {
    for (const x of r.sessions) {
      seen = true;
      if (x.cost_source === "home_rate_misapplied" && !x.network_state) {
        sessions += 1;
        kwh += x.kwh ?? 0;
      }
    }
  }
  if (!seen) return { sessions: m.spend.estimate_pending_sessions ?? 0, kwh: m.spend.estimate_pending_kwh ?? 0 };
  return { sessions, kwh };
}

// Plain-English quality notes from the server's counters (spec MQ*).
// `auto`: DCFC auto-network fields are present (to_confirm_count != null).
export function dataQualityNotes(m: LedgerMonth, auto = false): string[] {
  const n: string[] = [];
  const dq = m.data_quality;
  const s = (k: number, one: string, many: string) => (k === 1 ? one : many);
  const pending = auto ? unidentifiedPending(m).sessions : (m.spend.estimate_pending_sessions ?? 0);
  if (pending > 0) {
    n.push(
      auto
        ? `Couldn't identify the network for ${pending} fast-charging ${s(pending, "session", "sessions")}; ${s(pending, "it's", "they're")} left out of the total until you pick the network.`
        : `${pending} fast-charging ${s(pending, "session was", "sessions were")} priced at your home rate by an older version of Rivolt. ${s(pending, "It's", "They're")} left out of the total until you add the price.`,
    );
  }
  const suspect = suspectSessionCount(m);
  if (suspect > 0) {
    n.push(
      `${suspect} ${s(suspect, "session", "sessions")} away from home ${s(suspect, "was", "were")} priced at your home rate and ${s(suspect, "is", "are")} in the total; the real price was probably different.`,
    );
  }
  if (m.spend.unpriced_sessions > 0) {
    n.push(
      `${m.spend.unpriced_sessions} ${s(m.spend.unpriced_sessions, "session has", "sessions have")} no price and ${s(m.spend.unpriced_sessions, "isn't", "aren't")} in the total.`,
    );
  }
  const share = estimatedShare(m);
  if (share > 0) {
    n.push(`${Math.round(share * 100)}% of the total is estimated from charging-network rates.`);
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
    const k = dq.cost_source_derived_sessions;
    n.push(
      k != null && k > 0
        ? `${k} older ${s(k, "session's", "sessions'")} price source was worked out from your home rate rather than recorded.`
        : "Some price sources were worked out from your home rate rather than recorded.",
    );
  }
  return n;
}

// ---- fixtures -------------------------------------------------------

export const DCFC_MIN_KW = 50;

// A typed month shaped like the replay corpus, covering every price
// state: home rate, folded top-ups, a legacy DCFC stamped at the home
// rate ("pending") with a sub-50 kW taper fragment, a network
// estimate, a Rivian-billed stop, a user "Free" session, an unpriced
// one, null energy and a dedupe. NO coordinates, addresses or place
// names: places are home/away and distances are rounded.
// now decides which month is "current" (e2e pins it with STUB_NOW).
export function ledgerMonthFixture(month: string, now = new Date()): LedgerMonth {
  const [y, mo] = month.split("-").map(Number);
  // Local CDT wall clock → UTC (fixture only; fixed -5h offset).
  const at = (day: number, h: number, min = 0) =>
    new Date(Date.UTC(y, mo - 1, day, h + 5, min)).toISOString();
  const homeRate = 0.11;
  const vid = "01-000000000";
  let seq = 0;

  type SessIn = Partial<ReceiptSession> & Pick<ReceiptSession, "kwh" | "peak_kw" | "soc_start" | "soc_end">;
  const sess = (place: Place, start: string, end: string, o: SessIn): ReceiptSession => {
    seq += 1;
    const source: CostSource = o.cost_source ?? "home_rate";
    const cost =
      o.cost !== undefined ? o.cost : source === "unknown" || o.kwh == null ? null : +(o.kwh * homeRate).toFixed(4);
    const price = o.price_per_kwh !== undefined ? o.price_per_kwh : cost != null && o.kwh ? cost / o.kwh : null;
    return {
      charge_id: `live_${vid}_c_fx${String(seq).padStart(3, "0")}`,
      started_at: start,
      ended_at: end,
      final_state: "charging_complete",
      source: "live",
      flags: [],
      cost_source_derived: true,
      network: null,
      ...o,
      cost,
      price_per_kwh: price,
      cost_source: cost == null ? "unknown" : source,
      kind: o.peak_kw != null && o.peak_kw >= DCFC_MIN_KW ? "dcfc" : "ac",
      place,
    };
  };
  // Counts in spend.total (contract §10.1).
  const inTotal = (x: ReceiptSession) =>
    x.cost != null && x.cost_source !== "unknown" && x.cost_source !== "home_rate_misapplied";

  const receipt = (km: number | null, sessions: ReceiptSession[], extra: ReceiptFlag[] = []): Receipt => {
    const first = sessions[0];
    const last = sessions[sessions.length - 1];
    const counted = sessions.filter(inTotal);
    const cost = counted.length ? counted.reduce((a, x) => a + (x.cost ?? 0), 0) : null;
    const countedKwh = counted.reduce((a, x) => a + (x.kwh ?? 0), 0);
    const sources = new Set(sessions.map((x) => x.cost_source));
    const peak = Math.max(...sessions.map((x) => x.peak_kw ?? 0));
    const flags = new Set<ReceiptFlag>(extra);
    if (sessions.length > 1) flags.add("fragments_folded");
    for (const x of sessions) x.flags.forEach((f) => flags.add(f));
    if (sessions.some((x) => x.cost == null)) flags.add("unpriced");
    const d0 = new Date(first.started_at);
    const d1 = new Date(last.ended_at);
    const days =
      Math.round(
        (Date.UTC(d1.getUTCFullYear(), d1.getUTCMonth(), d1.getUTCDate()) -
          Date.UTC(d0.getUTCFullYear(), d0.getUTCMonth(), d0.getUTCDate())) /
          86_400_000,
      ) + 1;
    return {
      receipt_id: `rcpt_${first.charge_id}`,
      vehicle_id: vid,
      kind: peak >= DCFC_MIN_KW ? "dcfc" : "ac",
      place: first.place ?? "unknown",
      km_from_home: km,
      started_at: first.started_at,
      ended_at: last.ended_at,
      local_date: new Date(d0.getTime() - 5 * 3_600_000).toISOString().slice(0, 10),
      spans_days: Math.max(1, days),
      soc_start: first.soc_start,
      soc_end: last.soc_end,
      kwh: sessions.reduce((a, x) => a + (x.kwh ?? 0), 0),
      peak_kw: peak || null,
      cost,
      cost_source: sources.size === 1 ? [...sources][0] : "mixed",
      rate: cost != null && countedKwh > 0 ? cost / countedKwh : null,
      network: sessions.find((x) => x.network)?.network ?? null,
      session_count: sessions.length,
      unpriced_sessions: sessions.filter((x) => x.cost == null).length,
      flags: [...flags],
      sessions,
    };
  };

  const misapplied = (): SessIn["flags"] => ["home_rate_on_dcfc", "home_rate_away", "estimate_pending"];
  const receipts: Receipt[] = [
    receipt(0, [
      sess("home", at(2, 22), at(3, 4, 34), { kwh: 44.3, peak_kw: 7.6, soc_start: 27.4, soc_end: 62.8 }),
      sess("home", at(3, 4, 45), at(3, 6, 5), { kwh: 8.6, peak_kw: 7.6, soc_start: 62.8, soc_end: 70 }),
    ]),
    receipt(0, [sess("home", at(8, 19, 13), at(9, 3, 15), { kwh: 51.8, peak_kw: 7.6, soc_start: 28.1, soc_end: 70 })]),
    receipt(0, [
      sess("home", at(12, 7, 10), at(12, 7, 40), {
        kwh: null, peak_kw: 7.5, soc_start: 64, soc_end: 66, cost_source: "unknown", flags: ["null_energy"],
      }),
    ]),
    receipt(0, [
      sess("home", at(17, 17, 34), at(17, 21, 42), { kwh: 26.0, peak_kw: 7.6, soc_start: 20.6, soc_end: 42.2 }),
      sess("home", at(17, 21, 54), at(18, 3, 11), { kwh: 34.1, peak_kw: 7.6, soc_start: 42.2, soc_end: 69.9 }),
      ...[18, 19, 19, 19, 20, 20, 20, 20].map((d, i) => {
        const h = [23, 5, 13, 19, 0, 4, 10, 15][i];
        return sess("home", at(d, h, 10), at(d, h, 20), { kwh: 0.6, peak_kw: 7.5, soc_start: 69.4, soc_end: 70 });
      }),
    ]),
    receipt(0, [sess("home", at(26, 20, 5), at(27, 7, 0), { kwh: 72.8, peak_kw: 7.6, soc_start: 38.4, soc_end: 96.8 })]),
    // Legacy DCFC stamped at the home rate → pending, plus a sub-50 kW
    // taper fragment that stays home_rate (away) and counts as AC.
    receipt(300, [
      sess("away", at(27, 10, 28), at(27, 11, 4), {
        kwh: 81.5, peak_kw: 211.4, soc_start: 17.2, soc_end: 83, cost_source: "home_rate_misapplied", flags: misapplied(),
      }),
      sess("away", at(27, 11, 5), at(27, 11, 11), {
        kwh: 2.4, peak_kw: 38, soc_start: 83, soc_end: 85, flags: ["home_rate_away"],
      }),
    ]),
    receipt(530, [
      sess("away", at(27, 13, 35), at(27, 14, 15), {
        kwh: 64.2, peak_kw: 128.5, soc_start: 19.6, soc_end: 71.9, cost: 23.11, price_per_kwh: 0.36,
        cost_source: "network_estimate", network: "Electrify America", cost_source_derived: false,
      }),
    ]),
    receipt(640, [
      sess("away", at(27, 21, 41), at(28, 11, 0), {
        kwh: 15.5, peak_kw: 1.4, soc_start: 37.8, soc_end: 50.4, cost: 0, price_per_kwh: 0,
        cost_source: "user", cost_source_derived: false,
      }),
    ]),
    receipt(640, [
      sess("away", at(28, 18, 31), at(30, 8, 2), {
        kwh: 38.2, peak_kw: 1.4, soc_start: 27.2, soc_end: 58.3, cost_source: "unknown",
      }),
    ]),
    receipt(530, [
      sess("away", at(30, 11, 46), at(30, 12, 16), {
        kwh: 62.9, peak_kw: 179.6, soc_start: 22.3, soc_end: 73, cost: 37.74, price_per_kwh: 0.6,
        cost_source: "rivian", network: "Rivian Adventure Network", cost_source_derived: false,
      }),
    ]),
    receipt(300, [
      sess("away", at(30, 14, 50), at(30, 15, 29), {
        kwh: 86.4, peak_kw: 215.2, soc_start: 2.6, soc_end: 72.8, cost_source: "home_rate_misapplied", flags: misapplied(),
      }),
    ]),
    receipt(0, [sess("home", at(30, 19, 50), at(30, 23, 59), { kwh: 45.6, peak_kw: 7.6, soc_start: 11.7, soc_end: 49.4 })], [
      "continues_next_month",
    ]),
  ];
  receipts.sort((a, b) => b.started_at.localeCompare(a.started_at));
  return ledgerMonthFromReceipts(month, receipts, { homeRate, vid, now });
}

// ledgerMonthFromReceipts computes a month's totals from its receipts the way
// the server does (per session, by kind/place/source). The fixture and the
// edge-state fixtures (no charges, home-only, R2, ...) share it.
export function ledgerMonthFromReceipts(
  month: string,
  receipts: Receipt[],
  // now decides whether month is the current one (default: the real clock).
  opts: { homeRate?: number; vid?: string; miles?: number; drives?: number; now?: Date } = {},
): LedgerMonth {
  const [y, mo] = month.split("-").map(Number);
  const tz = "America/Chicago";
  const homeRate = opts.homeRate ?? 0.11;
  const vid = opts.vid ?? "01-000000000";
  const inTotal = (x: ReceiptSession) =>
    x.cost != null && x.cost_source !== "unknown" && x.cost_source !== "home_rate_misapplied";
  // Totals computed once here, per session, the way the server does.
  const empty = (): KindTotals => ({ sessions: 0, kwh: 0, cost: 0, peak_kw: null });
  const by_kind: Record<ChargeKind, KindTotals> = { ac: empty(), dcfc: empty() };
  const by_place: Record<Place, KindTotals> = { home: empty(), away: empty(), unknown: empty() };
  const by_place_kind: Record<Place, Record<ChargeKind, KindTotals>> = {
    home: { ac: empty(), dcfc: empty() },
    away: { ac: empty(), dcfc: empty() },
    unknown: { ac: empty(), dcfc: empty() },
  };
  const by_source: SpendBySource = { rivian: 0, home_rate: 0, network_estimate: 0, user: 0 };
  let priced = 0;
  let unpriced = 0;
  let unpricedKwh = 0;
  let pending = 0;
  let pendingKwh = 0;
  let nullEnergy = 0;
  let derived = 0;
  let onDcfc = 0;
  let away = 0;
  const sessions = receipts.flatMap((r) => r.sessions);
  for (const x of sessions) {
    const kwh = x.kwh ?? 0;
    for (const line of [by_kind[x.kind!], by_place[x.place!], by_place_kind[x.place!][x.kind!]]) {
      line.sessions += 1;
      line.kwh += kwh;
      if (x.peak_kw != null) line.peak_kw = Math.max(line.peak_kw ?? 0, x.peak_kw);
      if (inTotal(x)) line.cost += x.cost ?? 0;
    }
    if (x.kwh == null) nullEnergy += 1;
    if (x.cost_source_derived) derived += 1;
    if (x.flags.includes("home_rate_on_dcfc")) onDcfc += 1;
    if (x.flags.includes("home_rate_away")) away += 1;
    if (x.cost_source === "home_rate_misapplied") {
      pending += 1;
      pendingKwh += kwh;
    } else if (inTotal(x)) {
      priced += 1;
      by_source[x.cost_source as keyof SpendBySource] += x.cost ?? 0;
    } else {
      unpriced += 1;
      unpricedKwh += kwh;
    }
  }
  const total = by_source.rivian + by_source.home_rate + by_source.network_estimate + by_source.user;
  const miles = opts.miles ?? 1284.3;
  return {
    version: LEDGER_CONTRACT_VERSION,
    period: month,
    tz,
    tz_source: "query",
    range: { start: new Date(Date.UTC(y, mo - 1, 1, 5)).toISOString(), end: new Date(Date.UTC(y, mo, 1, 5)).toISOString() },
    currency: "USD",
    vehicle_id: null,
    is_current_month: month === currentMonth(opts.now),
    first_activity_at: receipts.length ? receipts[receipts.length - 1].started_at : null,
    last_activity_at: receipts.length ? receipts[0].started_at : null,
    spend: {
      total,
      by_source,
      estimated_share: total > 0 ? by_source.network_estimate / total : null,
      priced_sessions: priced,
      unpriced_sessions: unpriced,
      unpriced_kwh: unpricedKwh,
      unpriced_home_rate_estimate: homeRate > 0 ? +(unpricedKwh * homeRate).toFixed(4) : null,
      estimate_pending_sessions: pending,
      estimate_pending_kwh: pendingKwh,
    },
    energy: {
      sessions: sessions.length,
      kwh_added: by_kind.ac.kwh + by_kind.dcfc.kwh,
      null_energy_sessions: nullEnergy,
      by_kind,
      by_place,
      by_place_kind,
    },
    driving: {
      drives: opts.drives ?? 61,
      miles,
      miles_with_energy: miles ? miles * 0.995 : 0,
      kwh_used_known: miles ? 560.1 : 0,
      coverage: miles ? 0.995 : null,
      cost_per_mile: miles ? total / miles : null,
      cost_per_mile_basis: "spend_over_miles",
    },
    receipts,
    data_quality: {
      cost_source_inferred: derived > 0,
      cost_source_derived_sessions: derived,
      superseded_excluded: receipts.length ? 1 : 0,
      ...(receipts.length ? { superseded: [{ charge_id: `live_${vid}_c_fx900`, superseded_by: `live_${vid}_c_fx002` }] } : {}),
      drives_superseded_excluded: 0,
      open_sessions: 0,
      home_rate_on_dcfc: onDcfc,
      home_rate_away: away,
      currency_mismatch_excluded: 0,
    },
  };
}

export function ledgerSummaryFixture(months: number, now = new Date()): LedgerSummary {
  const cur = currentMonth(now);
  const list: MonthSummary[] = [];
  for (let i = months - 1; i >= 0; i--) {
    // Only the last two months have "data" in the fixture.
    if (i > 1) continue;
    const full = ledgerMonthFixture(shiftMonth(cur, -i), now);
    const { receipts: _r, data_quality: { superseded: _s, ...dq }, ...rest } = full;
    list.push({
      ...rest,
      data_quality: dq,
      spend: i === 1 ? { ...rest.spend, total: rest.spend.total * 0.62 } : rest.spend,
    });
  }
  return { version: LEDGER_CONTRACT_VERSION, tz: "America/Chicago", currency: "USD", months: list };
}
