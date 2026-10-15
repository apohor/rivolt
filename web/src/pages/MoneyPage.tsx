import { inDemo } from "../lib/demo";
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { track } from "../lib/track";
import { backend } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { buildStatementCardModel, statementCardBlob } from "../lib/statementCard";
import { kindSegments, placeKindSegments, placeSegments, ReceiptRow, SplitBar } from "../components/money";
import { VehiclePicker } from "../components/VehiclePicker";
import { useSelectedVehicle } from "../lib/selectedVehicle";
import {
  currentMonth,
  dataQualityNotes,
  estimatedShare,
  fetchLedgerMonth,
  fetchLedgerSummary,
  formatMoney,
  isValidMonth,
  monthLabel,
  shiftMonth,
  suspectSessionCount,
  type LedgerMonth,
  type MonthSummary,
  type Receipt,
} from "../lib/ledger";
import { hasOpenNetworkState } from "../lib/autoNetwork";

// Money: the monthly charging statement (ux-slice-spec §4, ledger
// contract v1). Every total comes from the server; the page only
// formats and explains. Gated by money_tab_enabled (see App.tsx).

type ReceiptFilter = "all" | "home" | "away" | "dcfc" | "confirm";

function receiptMatches(r: Receipt, f: ReceiptFilter): boolean {
  if (f === "all") return true;
  if (f === "dcfc") return r.kind === "dcfc";
  if (f === "confirm") return hasOpenNetworkState(r);
  if (f === "home") return r.kind === "ac" && r.place === "home";
  return r.kind === "ac" && r.place === "away";
}

export default function MoneyPage() {
  const [params, setParams] = useSearchParams();
  const thisMonth = currentMonth();
  const monthParam = params.get("month");
  const month = isValidMonth(monthParam) && monthParam <= thisMonth ? monthParam : thisMonth;
  const fixture = params.get("fixture") === "1";
  // money_view: once per page load (src=statement when it came from the
  // monthly email). Counts and enums only.
  const viewedRef = useRef(false);
  useEffect(() => {
    if (viewedRef.current) return;
    viewedRef.current = true;
    const [ty, tm] = thisMonth.split("-").map(Number);
    const [my, mm] = month.split("-").map(Number);
    const props: Record<string, string | number> = { month_offset: Math.max(0, (ty - my) * 12 + (tm - mm)) };
    if (params.get("src") === "statement") props.src = "statement";
    track("money_view", props);
  }, [month, thisMonth, params]);
  const [metric, setMetric] = useState<"cost" | "kwh">("cost");
  const [filter, setFilter] = useState<ReceiptFilter>("all");

  const setMonth = (m: string) => {
    const next = new URLSearchParams(params);
    if (m === thisMonth) next.delete("month");
    else next.set("month", m);
    setParams(next, { replace: true });
  };

  // Multi-vehicle: scope to the picked car; single-vehicle accounts
  // omit vehicle_id (the server then covers all of the user's cars).
  const vehicles = useQuery({
    queryKey: ["rivian", "vehicles"],
    queryFn: () => backend.vehicles(),
    staleTime: 5 * 60_000,
    retry: 1,
  });
  const { selectedID, select } = useSelectedVehicle(vehicles.data);
  const vehicleID = (vehicles.data?.length ?? 0) > 1 ? selectedID : undefined;

  const monthQ = useQuery({
    queryKey: ["ledger", "monthly", month, vehicleID ?? "", fixture],
    queryFn: ({ signal }) => fetchLedgerMonth(month, { vehicleID, fixture, signal }),
    staleTime: 60_000,
  });
  const summaryQ = useQuery({
    queryKey: ["ledger", "summary", vehicleID ?? "", fixture],
    queryFn: ({ signal }) => fetchLedgerSummary({ months: 24, vehicleID, fixture, signal }),
    staleTime: 5 * 60_000,
    retry: 1,
  });

  const m = monthQ.data?.data;
  const isSample = monthQ.data?.source === "fixture";
  const months = summaryQ.data?.data.months ?? [];

  return (
    <div className="space-y-4">
      <PageHeader
        title="Money"
        subtitle="What charging actually cost"
        actions={
          <div className="flex items-center gap-2">
            <VehiclePicker vehicles={vehicles.data ?? []} selectedID={selectedID} onChange={select} />
            <MonthPicker month={month} max={thisMonth} onChange={setMonth} />
          </div>
        }
      />

      {isSample && (
        <div
          role="note"
          className="rounded-lg border border-sky-800/60 bg-sky-950/40 px-3 py-2 text-xs text-sky-200"
        >
          <span className="font-medium">Sample data.</span>{" "}
          {fixture
            ? "Showing the built-in example statement (?fixture=1)."
            : "The ledger service isn't available yet, so this is an example statement, not your truck."}
        </div>
      )}

      {monthQ.isLoading ? (
        <Spinner />
      ) : monthQ.isError ? (
        <ErrorBox title="Couldn't load this month" detail={String(monthQ.error)} />
      ) : m ? (
        <Statement toConfirm={summaryQ.data?.data.to_confirm_count ?? 0} askShare={params.get("share") === "1"} sample={isSample} m={m} months={months} metric={metric} onMetric={setMetric} filter={filter} onFilter={setFilter} />
      ) : null}
    </div>
  );
}

function MonthPicker({ month, max, onChange }: { month: string; max: string; onChange: (m: string) => void }) {
  const atMax = month >= max;
  const btn =
    "rounded-md px-2 py-1.5 text-neutral-300 hover:bg-neutral-800 hover:text-neutral-100 disabled:opacity-30 disabled:hover:bg-transparent";
  return (
    <div className="inline-flex items-center rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-sm">
      <button type="button" className={btn} onClick={() => onChange(shiftMonth(month, -1))} aria-label="Previous month">
        ◀
      </button>
      <span className="min-w-32 px-2 text-center tabular-nums text-neutral-100" aria-live="polite">
        {monthLabel(month)}
      </span>
      <button
        type="button"
        className={btn}
        onClick={() => onChange(shiftMonth(month, 1))}
        disabled={atMax}
        aria-label="Next month"
      >
        ▶
      </button>
    </div>
  );
}

function Statement({
  askShare,
  sample,
  m,
  months,
  metric,
  onMetric,
  filter,
  onFilter,
  toConfirm,
}: {
  askShare: boolean;
  sample: boolean;
  m: LedgerMonth;
  months: MonthSummary[];
  metric: "cost" | "kwh";
  onMetric: (v: "cost" | "kwh") => void;
  filter: ReceiptFilter;
  onFilter: (f: ReceiptFilter) => void;
  // charges awaiting confirmation of their auto-detected network (all months)
  toConfirm: number;
}) {
  const cur = m.currency;
  // Server-owned splits only; by_place is absent on pre-§10 builds.
  // One Home/Public/DCFC bar when the server sends by_place_kind (§11);
  // otherwise today's two bars.
  const combined = m.energy.by_place_kind
    ? placeKindSegments(m.energy.by_place_kind, m.energy.by_kind)
    : null;
  const where = !combined && m.energy.by_place ? placeSegments(m.energy.by_place) : null;
  const how = combined ? null : kindSegments(m.energy.by_kind);
  const pending = m.spend.estimate_pending_sessions ?? 0;
  const estShare = estimatedShare(m);
  const notes = useMemo(() => dataQualityNotes(m), [m]);
  const suspect = useMemo(() => suspectSessionCount(m), [m]);
  const shown = m.receipts.filter((r) => receiptMatches(r, filter));

  const prev = months.find((x) => x.period === shiftMonth(m.period, -1));
  const delta = prev && prev.spend.total > 0 ? (m.spend.total - prev.spend.total) / prev.spend.total : null;
  // Partial month (MQ8): the first month with any data, where activity
  // starts well after the 1st.
  const earliest = months.find((x) => x.first_activity_at != null);
  const partialFrom =
    m.first_activity_at &&
    (!earliest || earliest.period === m.period) &&
    new Date(m.first_activity_at).getTime() - new Date(m.range.start).getTime() > 2 * 86_400_000
      ? m.first_activity_at
      : null;
  const empty = m.receipts.length === 0 && m.spend.total === 0;

  return (
    <>
      <section className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-4 sm:p-5" aria-label="Statement">
        <div className="flex flex-wrap items-center gap-2 text-[11px]">
          {m.is_current_month && (
            <span className="rounded-full border border-neutral-700 px-2 py-0.5 text-neutral-300">Month to date</span>
          )}
          {partialFrom && (
            <span className="rounded-full border border-neutral-700 px-2 py-0.5 text-neutral-400">
              Partial month · data from{" "}
              {new Date(partialFrom).toLocaleDateString(undefined, { month: "short", day: "numeric" })}
            </span>
          )}
        </div>
        <div className="mt-2 flex flex-wrap items-end gap-x-4 gap-y-1">
          <div className="text-4xl font-semibold tracking-tight tabular-nums text-neutral-50 sm:text-5xl">
            {formatMoney(m.spend.total, cur)}
          </div>
          {delta != null && (
            <div className={`pb-1.5 text-sm tabular-nums ${delta > 0 ? "text-amber-300" : "text-emerald-300"}`}>
              {delta > 0 ? "▲" : "▼"} {Math.abs(delta * 100).toFixed(0)}% vs{" "}
              {monthLabel(prev!.period).split(" ")[0]}
            </div>
          )}
        </div>
        <div className="mt-1">
          <ShareStatementButton m={m} partialFrom={partialFrom ?? null} disabled={sample || empty} prompt={askShare} />
        </div>
        <p className="mt-1 text-sm text-neutral-400">
          spent on charging · {m.energy.kwh_added.toFixed(0)} kWh
          {m.driving.miles > 0 && <> · {m.driving.miles.toFixed(0)} mi</>}
          {m.driving.cost_per_mile != null && <> · {formatMoney(m.driving.cost_per_mile, cur, 3)}/mi</>}
        </p>

        {toConfirm > 0 && (
          <button
            type="button"
            onClick={() => onFilter("confirm")}
            className="mt-2 inline-flex items-center gap-1.5 rounded-full border border-amber-500/40 bg-amber-500/10 px-2.5 py-1 text-xs font-medium text-amber-200 hover:bg-amber-500/20"
          >
            {toConfirm} {toConfirm === 1 ? "charge" : "charges"} to confirm →
          </button>
        )}

        {estShare > 0 && (
          <p className="mt-0.5 text-xs text-neutral-500">
            Includes{" "}
            <span className="underline decoration-dotted underline-offset-4">{Math.round(estShare * 100)}% estimated</span>{" "}
            from charging-network rates.
          </p>
        )}

        {(pending > 0 || suspect > 0 || m.spend.unpriced_sessions > 0) && (
          <ul className="mt-3 space-y-1.5 text-xs">
            {pending > 0 && (
              <li className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-amber-100/90">
                <span className="font-medium text-amber-200">Estimate pending:</span> {pending} fast-charging{" "}
                {pending === 1 ? "session" : "sessions"} ({(m.spend.estimate_pending_kwh ?? 0).toFixed(1)} kWh){" "}
                {pending === 1 ? "isn't" : "aren't"} in the total yet. An older version priced{" "}
                {pending === 1 ? "it" : "them"} at your home rate. Add the price, or wait for the re-price.
              </li>
            )}
            {suspect > 0 && (
              <li className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-amber-100/90">
                <span className="font-medium text-amber-200">Likely wrong:</span> {suspect}{" "}
                {suspect === 1 ? "session" : "sessions"} away from home{" "}
                {suspect === 1 ? "was" : "were"} priced at your home rate and {suspect === 1 ? "is" : "are"} in the
                total.
              </li>
            )}
            {m.spend.unpriced_sessions > 0 && (
              <li className="text-neutral-400">
                Not included: {m.spend.unpriced_sessions} unpriced{" "}
                {m.spend.unpriced_sessions === 1 ? "session" : "sessions"} ({m.spend.unpriced_kwh.toFixed(1)} kWh)
                {m.spend.unpriced_home_rate_estimate != null && (
                  <>
                    {" "}
                    ≈{" "}
                    <span className="underline decoration-dotted underline-offset-4">
                      {formatMoney(m.spend.unpriced_home_rate_estimate, cur)}
                    </span>{" "}
                    if charged at home
                  </>
                )}
              </li>
            )}
          </ul>
        )}
      </section>

      {empty ? (
        <Card>
          <p className="text-sm text-neutral-400">
            No charging recorded in {monthLabel(m.period)}. Sessions show up here once your truck charges.
          </p>
        </Card>
      ) : (
        <>
          <Card
            title="Where the energy came from"
            actions={
              <div className="inline-flex rounded-md border border-neutral-800 p-0.5 text-xs" role="group" aria-label="Split by">
                {(["cost", "kwh"] as const).map((k) => (
                  <button
                    key={k}
                    type="button"
                    aria-pressed={metric === k}
                    onClick={() => onMetric(k)}
                    className={`rounded px-2 py-0.5 ${metric === k ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
                  >
                    {k === "cost" ? "$" : "kWh"}
                  </button>
                ))}
              </div>
            }
          >
            <div className="space-y-4">
              {combined && <SplitBar label="Charging" segments={combined} currency={cur} metric={metric} />}
              {where && <SplitBar label="Where" segments={where} currency={cur} metric={metric} />}
              {how && <SplitBar label="How" segments={how} currency={cur} metric={metric} />}
            </div>
          </Card>

          <Card
            title="Receipts"
            actions={
              <div className="flex flex-wrap gap-1 text-xs" role="group" aria-label="Filter receipts">
                {(["all", "home", "away", "dcfc", ...(filter === "confirm" || m.receipts.some((r) => hasOpenNetworkState(r)) ? (["confirm"] as const) : [])] as ReceiptFilter[]).map((f) => (
                  <button
                    key={f}
                    type="button"
                    aria-pressed={filter === f}
                    onClick={() => onFilter(f)}
                    className={`rounded-full border px-2 py-0.5 ${
                      filter === f
                        ? "border-emerald-600/60 bg-emerald-900/30 text-emerald-200"
                        : "border-neutral-800 text-neutral-400 hover:text-neutral-100"
                    }`}
                  >
                    {f === "all" ? "All" : f === "home" ? "Home" : f === "away" ? "Away" : f === "dcfc" ? "DCFC" : "To confirm"}
                  </button>
                ))}
              </div>
            }
          >
            {shown.length === 0 ? (
              <p className="text-sm text-neutral-500">No receipts match this filter.</p>
            ) : (
              <ul className="-mx-2 divide-y divide-neutral-800/70">
                {shown.map((r) => (
                  <ReceiptRow key={r.receipt_id} r={r} currency={cur} />
                ))}
              </ul>
            )}
            <p className="mt-3 text-[11px] text-neutral-500">
              One receipt per plug-in; top-ups and fragments are folded in.{" "}
              <span className="underline decoration-dotted underline-offset-4">Dotted</span> = estimate ·{" "}
              <span className="text-amber-300/90">!</span> = home rate used away from home · Pending = not in the
              total yet.
            </p>
          </Card>
        </>
      )}

      {notes.length > 0 && (
        <Card title="About these numbers">
          <ul className="list-disc space-y-1 pl-4 text-xs leading-relaxed text-neutral-400">
            {notes.map((n) => (
              <li key={n}>{n}</li>
            ))}
          </ul>
        </Card>
      )}
    </>
  );
}

// Exports the month as a 1080x1080 PNG built from aggregate totals only
// (no places, coordinates or receipts). Uses the share sheet when the
// device can share files, otherwise downloads.
function ShareStatementButton({
  m,
  partialFrom,
  disabled,
  prompt = false,
}: {
  m: LedgerMonth;
  partialFrom: string | null;
  disabled: boolean;
  // Arrived via the statement email's "Share it" link (share=1).
  prompt?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const onClick = async () => {
    setBusy(true);
    setErr(null);
    setDone(null);
    try {
      const blob = await statementCardBlob(buildStatementCardModel(m, { partialFrom, sample: inDemo() }));
      if (!blob) throw new Error("Image export isn't available in this browser.");
      const file = new File([blob], `rivolt-${m.period}.png`, { type: "image/png" });
      if (navigator.canShare?.({ files: [file] })) {
        await navigator.share({ files: [file], title: `Rivolt ${m.period}` });
        setDone("Shared.");
      } else {
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = file.name;
        a.click();
        // Revoking immediately can cancel the download in Safari/Firefox.
        window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
        setDone(`Saved ${file.name} to your downloads.`);
      }
    } catch (e) {
      if (!(e instanceof DOMException && e.name === "AbortError")) {
        setErr(e instanceof Error ? e.message : "Couldn't create the image.");
      }
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-wrap items-center gap-2">
      <button
        type="button"
        onClick={onClick}
        disabled={disabled || busy}
        className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-300 hover:bg-neutral-800 disabled:opacity-40"
        title="Image with totals only: no places or addresses"
      >
        {busy ? "Preparing…" : "Share statement"}
      </button>
      {prompt && !disabled && (
        <span role="status" className="text-xs text-emerald-300">
          Your statement is ready to share. Tap Share statement.
        </span>
      )}
      {err && <span role="alert" className="text-xs text-rose-300">{err}</span>}
      {done && <span role="status" className="text-xs text-emerald-300">{done}</span>}
    </div>
  );
}
