import { useState } from "react";
import { Link } from "react-router-dom";
import {
  formatMoney,
  grammarFor,
  sourceLabel,
  type KindTotals,
  type PlaceSplit,
  type Receipt,
  type ReceiptFlag,
} from "../lib/ledger";
import { formatDateTime } from "../lib/format";

// Presentational pieces for the Money tab. The rule they share: every
// figure says where its price came from (spec §4.2).
//   recorded → solid figure
//   suspect  → solid figure + amber "likely under-priced"
//   unknown  → "—" + "Add price"

export function MoneyValue({
  r,
  currency,
  className = "",
}: {
  r: Pick<Receipt, "cost" | "cost_source" | "flags">;
  currency: string;
  className?: string;
}) {
  const g = grammarFor(r);
  if (g === "unknown" || r.cost == null) {
    return (
      <span className={`text-neutral-500 ${className}`} title="Price unknown">
        —
      </span>
    );
  }
  if (g === "suspect") {
    return (
      <span className={className} title="Priced at your home rate; likely under-priced">
        {formatMoney(r.cost, currency)}
        <span aria-hidden className="ml-1 text-amber-400">
          !
        </span>
        <span className="sr-only"> (likely under-priced)</span>
      </span>
    );
  }
  return <span className={className}>{formatMoney(r.cost, currency)}</span>;
}

// Colours follow the app's semantics: emerald = Home, sky = Public,
// amber = DCFC.
type SplitKey = "home" | "public" | "ac" | "dcfc";
const SPLIT_META: Record<SplitKey, { label: string; bar: string; dot: string }> = {
  home: { label: "Home", bar: "bg-emerald-500", dot: "bg-emerald-400" },
  public: { label: "Public", bar: "bg-sky-500", dot: "bg-sky-400" },
  ac: { label: "AC", bar: "bg-emerald-500", dot: "bg-emerald-400" },
  dcfc: { label: "DCFC", bar: "bg-amber-500", dot: "bg-amber-400" },
};

export type SplitSegments = { key: SplitKey; totals: KindTotals }[];

// splitSegments prefers Home / Public / DCFC (when receipts reconcile
// with the server's AC total) and otherwise falls back to the
// server's AC / DCFC split.
export function splitSegments(
  place: PlaceSplit | null,
  byKind: { ac: KindTotals; dcfc: KindTotals },
): SplitSegments {
  if (place) {
    return [
      { key: "home", totals: place.home },
      { key: "public", totals: place.public },
      { key: "dcfc", totals: place.dcfc },
    ];
  }
  return [
    { key: "ac", totals: byKind.ac },
    { key: "dcfc", totals: byKind.dcfc },
  ];
}

// SplitBar: one stacked bar, switchable between $ and kWh because the
// two tell different stories (home is most of the energy but little
// of the money).
export function SplitBar({
  segments,
  currency,
  metric,
}: {
  segments: SplitSegments;
  currency: string;
  metric: "cost" | "kwh";
}) {
  const total = segments.reduce((a, s) => a + s.totals[metric], 0);
  const fmt = (t: KindTotals) =>
    metric === "cost" ? formatMoney(t.cost, currency) : `${t.kwh.toFixed(0)} kWh`;
  return (
    <div>
      <div
        className="flex h-3 w-full overflow-hidden rounded-full bg-neutral-800"
        role="img"
        aria-label={segments.map((s) => `${SPLIT_META[s.key].label} ${fmt(s.totals)}`).join(", ")}
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
      <dl className={`mt-3 grid gap-2 text-xs ${segments.length === 3 ? "grid-cols-3" : "grid-cols-2"}`}>
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
  return { label: "Public", dot: "bg-sky-400", chip: "border-sky-500/40 text-sky-300" };
}

function km(v: number): string {
  // Rounded so a receipt never reveals a precise distance from home.
  return v >= 100 ? `${Math.round(v / 10) * 10} km` : `${Math.round(v)} km`;
}

// ReceiptRow is one plug-in session. Fragments and top-ups fold into
// it (expandable). The row links to the first charge's detail page,
// which carries the existing price editor ("Add price").
export function ReceiptRow({ r, currency }: { r: Receipt; currency: string }) {
  const [open, setOpen] = useState(false);
  const chip = kindChip(r);
  const g = grammarFor(r);
  const parentID = r.sessions[0]?.charge_id ?? r.receipt_id.replace(/^rcpt_/, "");
  const notes = r.flags.map((f) => FLAG_TEXT[f]).filter(Boolean) as string[];
  if (r.spans_days > 1) notes.unshift(`${r.spans_days} days`);
  const extra = r.session_count - 1;
  return (
    <li className="py-1">
      <div className="flex items-start gap-3 rounded-lg px-2 py-2 transition-colors hover:bg-neutral-900/70">
        <span className={`mt-1.5 inline-block h-2 w-2 shrink-0 rounded-full ${chip.dot}`} aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline justify-between gap-3">
            <Link
              to={`/charges/${encodeURIComponent(parentID)}`}
              className="truncate text-sm text-neutral-100 hover:text-emerald-300"
            >
              {formatDateTime(r.started_at)}
              <span className={`ml-2 rounded-full border px-1.5 py-px text-[10px] ${chip.chip}`}>{chip.label}</span>
              {r.place === "away" && r.km_from_home != null && (
                <span className="ml-1.5 text-[11px] text-neutral-500">{km(r.km_from_home)} from home</span>
              )}
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
            {notes.map((n) => (
              <span key={n} className="text-neutral-500">
                · {n}
              </span>
            ))}
          </div>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-3 text-[11px]">
            {g === "unknown" ? (
              <Link to={`/charges/${encodeURIComponent(parentID)}`} className="font-medium text-amber-300 hover:text-amber-200">
                Add price →
              </Link>
            ) : (
              <span className={g === "suspect" ? "text-amber-300/90" : "text-neutral-500"}>{sourceLabel(r, currency)}</span>
            )}
            {g === "suspect" && (
              <Link to={`/charges/${encodeURIComponent(parentID)}`} className="font-medium text-amber-300 hover:text-amber-200">
                Fix price →
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
                  <Link to={`/charges/${encodeURIComponent(x.charge_id)}`} className="hover:text-neutral-200">
                    {formatDateTime(x.started_at)} · {x.kwh != null ? `${x.kwh.toFixed(1)} kWh` : "— kWh"}
                  </Link>
                  <span>{x.cost != null ? formatMoney(x.cost, currency) : "—"}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </li>
  );
}
