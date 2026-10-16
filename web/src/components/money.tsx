import { detailPath } from "../lib/demo";
import { hasOpenNetworkState } from "../lib/autoNetwork";
import { useState } from "react";
import { Link } from "react-router-dom";
import {
  formatMoney,
  grammarFor,
  needsPrice,
  sourceLabel,
  type ChargeKind,
  type KindTotals,
  type Place,
  type Receipt,
  type ReceiptFlag,
} from "../lib/ledger";
import { formatDateTime } from "../lib/format";
import { formatDistance, usePreferences } from "../lib/preferences";

// Presentational pieces for the Money tab. The rule they share: every
// figure says where its price came from (spec §4.2, contract §10):
//   recorded  → solid figure (billed, home rate, entered)
//   free      → "Free"
//   estimated → dotted underline + "est."
//   suspect   → solid + amber "!" (home rate stamped away from home)
//   pending   → "Pending" (old home-rate DCFC stamp; not in the total)
//   unknown   → "—"

export function MoneyValue({
  r,
  currency,
  className = "",
}: {
  r: Pick<Receipt, "cost" | "cost_source" | "flags"> & {
    sessions?: Receipt["sessions"];
    billed_cost?: number | null;
    billed_currency?: string | null;
  };
  currency: string;
  className?: string;
}) {
  // A matched Rivian receipt wins: its amount, solid, never dotted-estimated.
  if (r.billed_cost != null) {
    return (
      <span className={className} title="Billed by Rivian">
        {formatMoney(r.billed_cost, r.billed_currency || currency)}
      </span>
    );
  }
  const g = grammarFor(r);
  if (g === "pending") {
    return (
      <span className={`text-neutral-500 ${className}`} title="Estimate pending; not in the total">
        Pending
      </span>
    );
  }
  if (g === "unknown" || r.cost == null) {
    return (
      <span className={`text-neutral-500 ${className}`} title="Price unknown">
        —
      </span>
    );
  }
  if (g === "free") return <span className={className}>Free</span>;
  if (g === "estimated") {
    return (
      <span
        className={`underline decoration-dotted decoration-neutral-500 underline-offset-4 ${className}`}
        title="Estimated from the charging network's rate"
      >
        {formatMoney(r.cost, currency)}
        <span className="ml-1 text-[10px] font-normal uppercase tracking-wide text-neutral-500">est.</span>
      </span>
    );
  }
  if (g === "suspect") {
    return (
      <span className={className} title="Priced at your home rate away from home; likely wrong">
        {formatMoney(r.cost, currency)}
        <span aria-hidden className="ml-1 text-amber-400">
          !
        </span>
        <span className="sr-only"> (likely wrong)</span>
      </span>
    );
  }
  return <span className={className}>{formatMoney(r.cost, currency)}</span>;
}

// Colours follow the app's semantics: emerald = Home / AC, sky = Away,
// amber = DCFC, neutral = unknown.
type SplitKey = Place | ChargeKind | "public";
const SPLIT_META: Record<SplitKey, { label: string; bar: string; dot: string }> = {
  home: { label: "Home", bar: "bg-emerald-500", dot: "bg-emerald-400" },
  away: { label: "Away", bar: "bg-sky-500", dot: "bg-sky-400" },
  public: { label: "Away", bar: "bg-sky-500", dot: "bg-sky-400" },
  unknown: { label: "Unknown place", bar: "bg-neutral-500", dot: "bg-neutral-400" },
  ac: { label: "AC", bar: "bg-emerald-500", dot: "bg-emerald-400" },
  dcfc: { label: "DCFC", bar: "bg-amber-500", dot: "bg-amber-400" },
};

export type SplitSegments = { key: SplitKey; totals: KindTotals }[];

// Server-owned splits only (contract §10.2): by_place answers "where",
// by_kind answers "how fast". The two don't cross-tabulate, so they're
// shown as two bars rather than a client-computed Home/Public/DCFC.
export function placeSegments(byPlace: Record<Place, KindTotals>): SplitSegments {
  const segs: SplitSegments = [
    { key: "home", totals: byPlace.home },
    { key: "away", totals: byPlace.away },
  ];
  if (byPlace.unknown.sessions > 0) segs.push({ key: "unknown", totals: byPlace.unknown });
  return segs;
}

// One bar, three honest buckets (contract §11): Home = home AC,
// Public = away AC, DCFC = every fast session. Cells come from the
// server; nothing is cross-tabulated client-side. Unknown-place AC only
// appears when it has sessions.
export function placeKindSegments(
  byPlaceKind: Record<Place, Record<ChargeKind, KindTotals>>,
  byKind: Record<ChargeKind, KindTotals>,
): SplitSegments {
  const segs: SplitSegments = [
    { key: "home", totals: byPlaceKind.home.ac },
    { key: "public", totals: byPlaceKind.away.ac },
    { key: "dcfc", totals: byKind.dcfc },
  ];
  if (byPlaceKind.unknown.ac.sessions > 0) segs.push({ key: "unknown", totals: byPlaceKind.unknown.ac });
  return segs;
}

export function kindSegments(byKind: Record<ChargeKind, KindTotals>): SplitSegments {
  return [
    { key: "ac", totals: byKind.ac },
    { key: "dcfc", totals: byKind.dcfc },
  ];
}

// SplitBar: one stacked bar, $ or kWh, because the two tell different
// stories (home is most of the energy but little of the money).
export function SplitBar({
  label,
  segments,
  currency,
  metric,
}: {
  label: string;
  segments: SplitSegments;
  currency: string;
  metric: "cost" | "kwh";
}) {
  const total = segments.reduce((a, s) => a + s.totals[metric], 0);
  const fmt = (t: KindTotals) => (metric === "cost" ? formatMoney(t.cost, currency) : `${t.kwh.toFixed(0)} kWh`);
  const cols = segments.length === 4 ? "grid-cols-2 sm:grid-cols-4" : segments.length === 3 ? "grid-cols-3" : "grid-cols-2";
  return (
    <div>
      <div className="mb-1.5 text-[11px] uppercase tracking-wide text-neutral-500">{label}</div>
      <div
        className="flex h-3 w-full overflow-hidden rounded-full bg-neutral-800"
        role="img"
        aria-label={`${label}: ${segments.map((s) => `${SPLIT_META[s.key].label} ${fmt(s.totals)}`).join(", ")}`}
      >
        {total > 0 &&
          segments.map((s) =>
            s.totals[metric] > 0 ? (
              <div
                key={s.key}
                className={SPLIT_META[s.key].bar}
                style={{ width: `${(s.totals[metric] / total) * 100}%` }}
              />
            ) : null,
          )}
      </div>
      <dl className={`mt-2 grid gap-2 text-xs ${cols}`}>
        {segments.map((s) => (
          <div key={s.key}>
            <dt className="flex items-center gap-1.5 text-neutral-400">
              <span className={`inline-block h-2 w-2 rounded-full ${SPLIT_META[s.key].dot}`} />
              {SPLIT_META[s.key].label}
            </dt>
            <dd className="mt-0.5 tabular-nums text-neutral-100">{fmt(s.totals)}</dd>
            <dd className="text-neutral-500">
              {s.totals.sessions} {s.totals.sessions === 1 ? "session" : "sessions"}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

const FLAG_TEXT: Partial<Record<ReceiptFlag, string>> = {
  null_energy: "no energy recorded",
  open_session: "charging now",
  abandoned: "ended abruptly",
  continues_next_month: "continues next month",
  continued_from_previous_month: "started last month",
  currency_mismatch: "other currency, not in total",
};

function kindChip(r: Receipt): { label: string; dot: string; chip: string } {
  if (r.kind === "dcfc") return { label: "DCFC", dot: "bg-amber-400", chip: "border-amber-500/40 text-amber-300" };
  if (r.place === "home") return { label: "Home", dot: "bg-emerald-400", chip: "border-emerald-500/40 text-emerald-300" };
  if (r.place === "away") return { label: "Away", dot: "bg-sky-400", chip: "border-sky-500/40 text-sky-300" };
  return { label: "AC", dot: "bg-neutral-400", chip: "border-neutral-600 text-neutral-300" };
}

// ReceiptRow is one plug-in session. Fragments and top-ups fold into
// it (expandable). Price actions link to the existing charge detail
// page, which carries the pricing editor (PATCH /api/charges/{id}/pricing).
export function ReceiptRow({ r, currency }: { r: Receipt; currency: string }) {
  const { distanceUnit } = usePreferences();
  const [open, setOpen] = useState(false);
  const chip = kindChip(r);
  const g = grammarFor(r);
  // Rivian receipts are on for this user when the server sends the matched counts.
  const billedMode = r.billed_sessions != null;
  const parentID = r.sessions[0]?.charge_id ?? r.receipt_id.replace(/^rcpt_/, "");
  const notes = r.flags.map((f) => FLAG_TEXT[f]).filter(Boolean) as string[];
  if (r.spans_days > 1) notes.unshift(`${r.spans_days} days`);
  const extra = r.session_count - 1;
  // One action per row. A missing or pending price outranks a likely
  // wrong one; the link opens the first session with that problem.
  const addPrice = g === "unknown" || g === "pending" || needsPrice(r);
  const confirmPending = hasOpenNetworkState(r);
  const action = addPrice ? "Add price →" : g === "suspect" ? "Fix price →" : confirmPending ? "Confirm →" : null;
  const target = addPrice
    ? r.sessions.find((x) => x.cost == null || x.cost_source === "unknown" || x.cost_source === "home_rate_misapplied")
    : r.sessions.find((x) => x.cost_source === "home_rate" && x.flags.includes("home_rate_away"));
  const fixID = target?.charge_id ?? (confirmPending ? r.sessions.find((x) => hasOpenNetworkState(x))?.charge_id : undefined) ?? parentID;
  return (
    <li className="py-1">
      <div className="flex items-start gap-3 rounded-lg px-2 py-2 transition-colors hover:bg-neutral-900/70">
        <span className={`mt-1.5 inline-block h-2 w-2 shrink-0 rounded-full ${chip.dot}`} aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline justify-between gap-3">
            <Link
              to={detailPath("charges", parentID)}
              className="truncate text-sm text-neutral-100 hover:text-emerald-300"
            >
              {formatDateTime(r.started_at)}
              <span className={`ml-2 rounded-full border px-1.5 py-px text-[10px] ${chip.chip}`}>{chip.label}</span>
            </Link>
            <MoneyValue r={r} currency={currency} className="shrink-0 text-sm font-medium tabular-nums text-neutral-100" />
          </div>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-neutral-400">
            <span className="tabular-nums">{r.kwh.toFixed(1)} kWh</span>
            {r.soc_start != null && r.soc_end != null && (
              <span className="tabular-nums">
                {Math.round(r.soc_start)}% → {Math.round(r.soc_end)}%
              </span>
            )}
            {r.peak_kw != null && (
              <span className="tabular-nums">{r.peak_kw.toFixed(r.peak_kw < 10 ? 1 : 0)} kW peak</span>
            )}
            {r.place !== "home" && (r.billed_vendor || r.billed_charger) && (
              <span data-billed-where className="text-neutral-300">
                {[r.billed_vendor, r.billed_charger].filter(Boolean).join(" · ")}
              </span>
            )}
            {r.place === "away" && r.km_from_home != null && (
              <span className="whitespace-nowrap tabular-nums text-neutral-500">
                {formatDistance(r.km_from_home, distanceUnit)} from home
              </span>
            )}
            {notes.map((n) => (
              <span key={n} className="text-neutral-500">
                · {n}
              </span>
            ))}
          </div>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-3 text-[11px]">
            {billedMode && r.billed_cost != null ? (
              <span data-price-label="billed" className="font-medium text-emerald-300">Billed by Rivian</span>
            ) : (
              <span className={g === "suspect" || g === "pending" ? "text-amber-300/90" : "text-neutral-500"}>
                {billedMode && r.cost_source !== "rivian" && g !== "unknown" && g !== "pending" && g !== "free" && (
                  <span data-price-label="estimated" className="font-medium text-neutral-300">Estimated · </span>
                )}
                {sourceLabel(r, currency)}
              </span>
            )}
            {action && (
              <Link to={`${detailPath("charges", fixID)}${!addPrice && g !== "suspect" ? "#pricing" : ""}`} className="font-medium text-amber-300 hover:text-amber-200">
                {action}
              </Link>
            )}
            {extra > 0 && (
              <button
                type="button"
                onClick={() => setOpen((v) => !v)}
                aria-expanded={open}
                className="text-neutral-500 underline-offset-2 hover:text-neutral-200 hover:underline"
              >
                {open ? "Hide" : "+"} {extra} {extra === 1 ? "top-up/fragment" : "top-ups/fragments"}
              </button>
            )}
          </div>
          {open && extra > 0 && (
            <ul className="mt-2 space-y-1 border-l border-neutral-800 pl-3 text-[11px] text-neutral-400">
              {r.sessions.map((x) => (
                <li key={x.charge_id} className="flex justify-between gap-3 tabular-nums">
                  <Link to={detailPath("charges", x.charge_id)} className="hover:text-neutral-200">
                    {formatDateTime(x.started_at)} · {x.kwh != null ? `${x.kwh.toFixed(1)} kWh` : "— kWh"}
                    {x.kind === "dcfc" ? " · DCFC" : ""}
                  </Link>
                  <MoneyValue r={x} currency={currency} />
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </li>
  );
}
