import { chargesAllQuery } from "../lib/activityLists";
import { ENERGY_ESTIMATED_TOTAL_NOTE } from "../lib/energyEstimated";
import { appPath, inDemo } from "../lib/demo";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { track } from "../lib/track";
import { backend, type Charge } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { buildMonthShareModel, shareFileName } from "../lib/shareCard";
import { ShareSheet } from "../components/ShareSheet";
import { SavingsCard } from "../components/SavingsCard";
import { OffpeakPrompt } from "../components/OffpeakPrompt";
import { fetchSavings, offpeakShareOf } from "../lib/savings";
import { kindSegments, placeKindSegments, placeSegments, ReceiptRow, SplitBar } from "../components/money";
import { VehiclePicker } from "../components/VehiclePicker";
import { useSelectedVehicle } from "../lib/selectedVehicle";
import {
  currentMonth,
  dataQualityNotes,
  unidentifiedPending,
  estimatedShare,
  fetchLedgerMonth,
  fetchLedgerSummary,
  formatMoney,
  isValidMonth,
  monthLabel,
  shiftMonth,
  statementCsvUrl,
  suspectSessionCount,
  type LedgerMonth,
  type MonthSummary,
  type Receipt,
  spendThroughDay,
  statementTotal,
} from "../lib/ledger";
import { isOpenNetworkState } from "../lib/autoNetwork";
import { formatDateTime } from "../lib/format";
import { detailPath } from "../lib/demo";

// Money: the monthly charging statement (ux-slice-spec §4, ledger
// contract v1). Every total comes from the server; the page only
// formats and explains.

type ReceiptFilter = "all" | "home" | "away" | "dcfc" | "confirm";

function receiptMatches(r: Receipt, f: ReceiptFilter): boolean {
  if (f === "all") return true;
  if (f === "dcfc") return r.kind === "dcfc";
  if (f === "confirm") return false; // listed from charges, all months
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
  const [filter, setFilter] = useState<ReceiptFilter>(() => (params.get("filter") === "confirm" ? "confirm" : "all"));

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

  const toConfirmN = summaryQ.data?.data.to_confirm_count ?? null;
  // The "To confirm" view spans every month, so it comes from the charge
  // list (the Money month only carries its own receipts).
  const chargesQ = useQuery({ ...chargesAllQuery(), enabled: (toConfirmN ?? 0) > 0 });
  const confirmCharges = (chargesQ.data ?? []).filter((c) => isOpenNetworkState(c.network_state));

  const m = monthQ.data?.data;
  const isSample = monthQ.data?.source === "fixture";
  const months = summaryQ.data?.data.months ?? [];

  return (
    <div className="space-y-4" data-ui-grid="money">
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
          data-ui-area="m-banner"
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
        <Statement vehicleID={vehicleID} toConfirm={summaryQ.data?.data.to_confirm_count ?? null} confirmCharges={confirmCharges} askShare={params.get("share") === "1"} sample={isSample} m={m} months={months} metric={metric} onMetric={setMetric} filter={filter} onFilter={setFilter} />
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
  confirmCharges,
  vehicleID,
}: {
  vehicleID?: string;
  askShare: boolean;
  sample: boolean;
  m: LedgerMonth;
  months: MonthSummary[];
  metric: "cost" | "kwh";
  onMetric: (v: "cost" | "kwh") => void;
  filter: ReceiptFilter;
  onFilter: (f: ReceiptFilter) => void;
  // charges awaiting confirmation of their auto-detected network (all
  // months); null while DCFC auto-network is off
  toConfirm: number | null;
  confirmCharges: Charge[];
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
  // Auto-network is "in use" only when something actually carries it: the
  // summary count is a plain int (0 for everyone, flag on or off), so
  // 0 alone must keep the legacy wording.
  const auto =
    (toConfirm ?? 0) > 0 ||
    m.receipts.some((r) => r.network_state != null || r.sessions.some((x) => x.network_state != null));
  const unid = unidentifiedPending(m);
  const pending = auto ? unid.sessions : (m.spend.estimate_pending_sessions ?? 0);
  const pendingKwh = auto ? unid.kwh : (m.spend.estimate_pending_kwh ?? 0);
  const estShare = estimatedShare(m);
  const notes = useMemo(() => dataQualityNotes(m, auto), [m, auto]);
  const suspect = useMemo(() => suspectSessionCount(m), [m]);
  const shown = m.receipts.filter((r) => receiptMatches(r, filter));

  const prev = months.find((x) => x.period === shiftMonth(m.period, -1));
  // Nothing priced this month (no charging, or every session unpriced): a
  // "vs last month" delta (-100%) or a $/mi of $0 says nothing true, so both
  // stay hidden.
  // With Rivian receipts the headline is billed + estimated, so it always equals the two
  // lines under it; otherwise it is the statement total as before.
  const headline = statementTotal(m);
  const hasCharging = m.spend.priced_sessions > 0 || headline > 0;
  // New look: a month in progress is compared with the same days of the previous month, and a
  // previous month that only has part of its days of data is not compared at all.
  const sameDay = m.is_current_month && !!prev;
  const dayN = new Date().getDate();
  const prevFullQ = useQuery({
    queryKey: ["ledger", "monthly", prev?.period ?? "", "same-day"],
    queryFn: ({ signal }) => fetchLedgerMonth(prev!.period, { signal }),
    enabled: sameDay,
    staleTime: 60_000,
    retry: 0,
  });
  const prevPartial =
    !!prev && prev.first_activity_at != null && new Date(prev.first_activity_at).getTime() - new Date(prev.range.start).getTime() > 2 * 86_400_000;
  const prevBase = sameDay
    ? prevFullQ.data?.source === "api"
      ? spendThroughDay(prevFullQ.data.data, dayN)
      : null
    : prevPartial
      ? null
      : prev
        ? prev.spend.total
        : null;
  const delta = hasCharging && prevBase != null && prevBase > 0 ? (headline - prevBase) / prevBase : null;
  const deltaLabel = prev ? (sameDay ? `${monthLabel(prev.period).split(" ")[0]} 1–${dayN}` : monthLabel(prev.period).split(" ")[0]) : "";
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
  const chargingSettings = useQuery({ queryKey: ["charging-settings"], queryFn: () => backend.getChargingSettings(), staleTime: 60_000, retry: 0 });
  const rateSet = (chargingSettings.data?.home_price_per_kwh ?? 0) > 0;

  return (
    <>
      {((m.spend.observed_estimate_sessions ?? 0) > 0 || (m.spend.unpriced_fast_sessions ?? 0) > 0) && <ObservedEstimateNotice />}
      <OffpeakPrompt homeRateSet={rateSet} />
      <section data-ui-area="m-hero" className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-4 sm:p-5" aria-label="Statement">
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
          <div className="ui-bignum text-4xl font-semibold tracking-tight tabular-nums text-neutral-50 sm:text-5xl">
            <span data-money-headline>{formatMoney(headline, cur)}</span>
          </div>
          {delta != null && (
            <div className={`pb-1.5 text-sm tabular-nums ${delta > 0 ? "text-amber-300" : "text-emerald-300"}`}>
              {delta > 0 ? "▲" : "▼"} {Math.abs(delta * 100).toFixed(0)}% vs{" "}
              {deltaLabel}
            </div>
          )}
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <ShareStatementButton m={m} partialFrom={partialFrom ?? null} disabled={sample || empty} prompt={askShare} />
          <CsvDownloadLink month={m.period} vehicleID={vehicleID} disabled={sample || empty || inDemo()} />
        </div>
        <p className="mt-1 text-sm text-neutral-400">
          spent on charging ·{" "}
          {m.energy.includes_estimated ? (
            <span className="underline decoration-dotted underline-offset-4" data-energy-estimated="true">~{m.energy.kwh_added.toFixed(0)} kWh</span>
          ) : (
            <>{m.energy.kwh_added.toFixed(0)} kWh</>
          )}
          {m.driving.miles > 0 && <> · {m.driving.miles.toFixed(0)} mi</>}
          {hasCharging && m.driving.cost_per_mile != null && <> · {formatMoney(m.driving.cost_per_mile, cur, 3)}/mi</>}
        </p>
        {m.energy.includes_estimated && (
          <p data-energy-estimated-note className="mt-1 text-xs text-neutral-500">{ENERGY_ESTIMATED_TOTAL_NOTE}</p>
        )}

        {headline === 0 && m.spend.unpriced_sessions > 0 && (
          <p data-money-zero className="mt-2 rounded-md border border-neutral-800 px-3 py-2 text-sm text-neutral-300">
            $0 doesn&apos;t mean charging was free. {m.spend.unpriced_sessions} {m.spend.unpriced_sessions === 1 ? "session" : "sessions"} ({m.spend.unpriced_kwh.toFixed(1)} kWh) can&apos;t be priced yet
            {m.spend.unpriced_home_rate_estimate == null && m.spend.unpriced_kwh > 0 && (
              <>
                {" "}
                because no home rate is set.{" "}
                <Link to={appPath("/settings?tab=charging#cost")} className="text-emerald-300 underline underline-offset-4 hover:text-emerald-200">
                  Set your home rate
                </Link>
                .
              </>
            )}
            {!(m.spend.unpriced_home_rate_estimate == null && m.spend.unpriced_kwh > 0) && "."}
          </p>
        )}

        {toConfirm != null && toConfirm > 0 && (
          <button
            type="button"
            onClick={() => onFilter("confirm")}
            className="mt-2 inline-flex items-center gap-1.5 rounded-full border border-amber-500/40 bg-amber-500/10 px-2.5 py-1 text-xs font-medium text-amber-200 hover:bg-amber-500/20"
          >
            {toConfirm} {toConfirm === 1 ? "charge" : "charges"} to confirm →
          </button>
        )}

        {m.spend.billed_total != null && m.spend.estimated_total != null && (
          <p data-billed-split className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs tabular-nums">
            <span className="text-emerald-300">
              {formatMoney(m.spend.billed_total, cur)} billed by Rivian
              {m.billed_sessions != null && <span className="text-neutral-500"> · {m.billed_sessions} {m.billed_sessions === 1 ? "session" : "sessions"}</span>}
            </span>
            <span className="text-neutral-400">
              {formatMoney(m.spend.estimated_total, cur)} estimated
              {m.estimated_sessions != null && <span className="text-neutral-500"> · {m.estimated_sessions} {m.estimated_sessions === 1 ? "session" : "sessions"}</span>}
            </span>
          </p>
        )}

        {(m.spend.observed_estimate_sessions ?? 0) > 0 && (
          <p data-observed-estimate className="mt-0.5 text-xs text-neutral-400">
            Includes {formatMoney(m.spend.observed_estimate_cost ?? 0, cur)} estimated from typical prices ({m.spend.observed_estimate_sessions}{" "}
            {m.spend.observed_estimate_sessions === 1 ? "session" : "sessions"})
          </p>
        )}
        {(m.spend.unpriced_fast_sessions ?? 0) > 0 && (
          <p data-unpriced-fast className="mt-0.5 text-xs text-amber-300/90">
            {m.spend.unpriced_fast_sessions} fast-charging {m.spend.unpriced_fast_sessions === 1 ? "session" : "sessions"} not priced yet
          </p>
        )}

        {estShare > 0 && m.spend.billed_total == null && (
          <p className="mt-0.5 text-xs text-neutral-500">
            Includes{" "}
            <span className="underline decoration-dotted underline-offset-4">{Math.round(estShare * 100)}% estimated</span>{" "}
            from charging-network rates.
          </p>
        )}

        {(pending > 0 || suspect > 0 || m.spend.unpriced_sessions > 0) && (
          <ul className="mt-3 space-y-1.5 text-xs">
            {auto && pending + suspect > 0 && (
              <li className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-amber-100/90">
                <span className="font-medium text-amber-200">Couldn't identify the network</span> for{" "}
                {pending + suspect} {pending + suspect === 1 ? "session" : "sessions"}
                {pending > 0 && (
                  <>
                    {" "}
                    ({pending} {pending === 1 ? "isn't" : "aren't"} in the total yet, {pendingKwh.toFixed(1)} kWh)
                  </>
                )}
                . Pick the network to price {pending + suspect === 1 ? "it" : "them"}.
              </li>
            )}
            {!auto && pending > 0 && (
              <li className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-amber-100/90">
                {auto ? (
                  <>
                    <span className="font-medium text-amber-200">Couldn't identify the network:</span> {pending}{" "}
                    fast-charging {pending === 1 ? "session" : "sessions"} ({pendingKwh.toFixed(1)} kWh){" "}
                    {pending === 1 ? "isn't" : "aren't"} in the total yet. Pick the network to price{" "}
                    {pending === 1 ? "it" : "them"}.
                  </>
                ) : (
                  <>
                    <span className="font-medium text-amber-200">Estimate pending:</span> {pending} fast-charging{" "}
                    {pending === 1 ? "session" : "sessions"} ({pendingKwh.toFixed(1)} kWh){" "}
                    {pending === 1 ? "isn't" : "aren't"} in the total yet. An older version priced{" "}
                    {pending === 1 ? "it" : "them"} at your home rate. Add the price.
                  </>
                )}
              </li>
            )}
            {!auto && suspect > 0 && (
              <li className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-amber-100/90">
                <span className="font-medium text-amber-200">
                  {auto ? "Couldn't identify the network:" : "Likely wrong:"}
                </span>{" "}
                {suspect} {suspect === 1 ? "session" : "sessions"} away from home{" "}
                {suspect === 1 ? "was" : "were"} priced at your home rate and {suspect === 1 ? "is" : "are"} in the
                total.{auto ? " Pick the network." : ""}
              </li>
            )}
            {m.spend.unpriced_sessions > 0 && !(headline === 0 && m.spend.unpriced_home_rate_estimate == null && m.spend.unpriced_kwh > 0) && (
              <li className="text-neutral-400">
                Not included: {m.spend.unpriced_sessions} unpriced{" "}
                {m.spend.unpriced_sessions === 1 ? "session" : "sessions"} ({m.spend.unpriced_kwh.toFixed(1)} kWh)
                {m.spend.unpriced_home_rate_estimate != null && m.spend.unpriced_home_rate_estimate > 0 && (
                  <>
                    {" "}
                    ≈{" "}
                    <span className="underline decoration-dotted underline-offset-4">
                      {formatMoney(m.spend.unpriced_home_rate_estimate, cur)}
                    </span>{" "}
                    if charged at home
                  </>
                )}
                {m.spend.unpriced_home_rate_estimate == null && m.spend.unpriced_kwh > 0 && (
                  <>
                    . No home rate is set;{" "}
                    <Link to={appPath("/settings?tab=charging")} className="text-emerald-300 underline underline-offset-4 hover:text-emerald-200">
                      set it in Settings
                    </Link>{" "}
                    to price home charging.
                  </>
                )}
              </li>
            )}
          </ul>
        )}
      </section>

      {empty ? (
        <Card>
          <h2 className="text-base font-semibold text-neutral-100">{rateSet ? "No charges recorded yet" : "Nothing priced yet"}</h2>
          <p className="mt-1 text-sm text-neutral-400">
            {rateSet ? (
              <>No charges recorded in {monthLabel(m.period)}. They appear here as you charge.</>
            ) : (
              <>
                Charges show up here priced at your home rate once Rivolt has recorded one. Set your rate in Settings → Charging so new charges are priced as they arrive.
              </>
            )}
          </p>
          {!rateSet && (
            <Link
              to={appPath("/settings?tab=charging#cost")}
              className="mt-3 inline-block rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500"
            >
              Set home rate
            </Link>
          )}
        </Card>
      ) : (
        <>
          <Card
            area="m-split"
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

          <SavingsCard area="m-save" />

          <Card title="Receipts" area="m-receipts">
            <div className="mb-3 flex flex-wrap gap-1 text-xs" role="group" aria-label="Filter receipts">
              {(["all", "home", "away", "dcfc", ...(filter === "confirm" || (toConfirm ?? 0) > 0 ? (["confirm"] as const) : [])] as ReceiptFilter[]).map((f) => (
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
                  {f === "all" ? "All" : f === "home" ? "Home" : f === "away" ? "Away" : f === "dcfc" ? "Fast" : "To confirm"}
                </button>
              ))}
            </div>
            {filter === "confirm" ? (
              <ToConfirmList charges={confirmCharges} />
            ) : shown.length === 0 ? (
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

// Opens the share sheet for the month: a 1080x1080 card built from aggregate totals only (no places, coordinates
// or receipts), masked by default: dollars appear only if the user turns "Include cost" on.
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
  const [open, setOpen] = useState(false);
  const sample = inDemo();
  // The off-peak share for this month, from the same sessions as the savings card; no share (no schedule, too few sessions) means no toggle.
  // Read with the page, not when the sheet opens: the sheet itself makes no request.
  const share = useQuery({ queryKey: ["savings", m.period], queryFn: ({ signal }) => fetchSavings(m.period, signal), staleTime: 5 * 60_000, retry: 0, enabled: !sample });
  const offpeakShare = offpeakShareOf(share.data);
  const build = useCallback(
    (includeCost: boolean, includeOffpeak: boolean) => buildMonthShareModel(m, { includeCost, sample, partialFrom, offpeakSharePct: includeOffpeak ? offpeakShare : null }),
    [m, sample, partialFrom, offpeakShare],
  );
  return (
    <div className="flex flex-wrap items-center gap-2">
      <button
        type="button"
        onClick={() => setOpen(true)}
        disabled={disabled}
        className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-300 hover:bg-neutral-800 disabled:opacity-40"
        title="An image of your month. No places or addresses; cost only if you add it"
      >
        Share statement
      </button>
      {prompt && !disabled && (
        <span role="status" className="text-xs text-emerald-300">
          Your statement is ready to share. Tap Share statement.
        </span>
      )}
      {open && <ShareSheet build={build} fileName={shareFileName("month", m.range.start)} withCostToggle withOffpeakToggle={offpeakShare != null} onClose={() => setOpen(false)} />}
    </div>
  );
}

// A link that downloads the month as a CSV: totals by cost type, home versus away, and one line per session. Own data only; nothing is
// requested until it is clicked, and no event is sent.
function CsvDownloadLink({ month, vehicleID, disabled }: { month: string; vehicleID?: string; disabled: boolean }) {
  const cls = "rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-300";
  if (disabled) {
    return (
      <span aria-disabled="true" className={`${cls} cursor-not-allowed opacity-40`} title="No sessions to download for this month">
        Download CSV
      </span>
    );
  }
  return (
    <a href={statementCsvUrl(month, { vehicleID })} download={`rivolt-statement-${month}.csv`} data-csv-download className={`${cls} hover:bg-neutral-800`} title="This month's totals and sessions as a spreadsheet file">
      Download CSV
    </a>
  );
}

// ToConfirmList: every charge whose auto-detected network is awaiting a
// look, across all months, newest first, grouped by month.
function ToConfirmList({ charges }: { charges: Charge[] }) {
  if (charges.length === 0) return <p className="text-sm text-neutral-500">Nothing to confirm.</p>;
  const groups = new Map<string, Charge[]>();
  for (const c of [...charges].sort((a, b) => b.StartedAt.localeCompare(a.StartedAt))) {
    const key = new Date(c.StartedAt).toLocaleDateString("en-CA").slice(0, 7);
    groups.set(key, [...(groups.get(key) ?? []), c]);
  }
  return (
    <div className="space-y-4" data-testid="to-confirm-list">
      {[...groups].map(([period, rows]) => (
        <section key={period} aria-label={monthLabel(period)}>
          <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-neutral-500">{monthLabel(period)}</h3>
          <ul className="-mx-2 divide-y divide-neutral-800/70">
            {rows.map((c) => {
              const strong = c.network_state === "needs_confirmation";
              const net = c.network_evidence?.candidates?.[0]?.network;
              const priced = c.PricePerKWh > 0;
              return (
                <li key={c.ID}>
                  <Link
                    to={`${detailPath("charges", c.ID)}#pricing`}
                    className="flex items-baseline justify-between gap-3 rounded-lg px-2 py-2 hover:bg-neutral-900/70"
                  >
                    <span className="min-w-0">
                      <span className="block truncate text-sm text-neutral-100">
                        {formatDateTime(c.StartedAt)}
                      </span>
                      <span className="block text-xs text-neutral-400">
                        {priced ? `${net ?? "Network"} · ${formatMoney(c.PricePerKWh, c.Currency || "USD", 3)}/kWh` : "Pick a network"}
                      </span>
                    </span>
                    <span className="shrink-0 text-right">
                      <span className="block text-sm tabular-nums text-neutral-100">
                        {priced ? formatMoney(c.Cost, c.Currency || "USD") : "Unpriced"}
                      </span>
                      <span className={`block text-[11px] ${strong ? "text-amber-300" : "text-neutral-500"}`}>
                        {strong ? "Please confirm →" : "Estimated · confirm →"}
                      </span>
                    </span>
                  </Link>
                </li>
              );
            })}
          </ul>
        </section>
      ))}
    </div>
  );
}

// One-time notice (per device): fast-charging sessions without the user's own rate are now estimated from a typical price,
// not an old table. Hidden for good once dismissed.
const OBSERVED_NOTICE_KEY = "rivolt.money.observedNotice.v1";
function ObservedEstimateNotice() {
  const [gone, setGone] = useState(() => {
    try {
      return window.localStorage.getItem(OBSERVED_NOTICE_KEY) === "1";
    } catch {
      return false;
    }
  });
  if (gone) return null;
  const hide = () => {
    setGone(true);
    try {
      window.localStorage.setItem(OBSERVED_NOTICE_KEY, "1");
    } catch {
      /* hides for this visit only */
    }
  };
  return (
    <div data-observed-notice role="note" className="mb-4 rounded-xl border border-neutral-700 bg-neutral-900/60 p-4 text-sm text-neutral-300">
      <p>Fast-charging sessions without your own rate are now estimated from a typical price (DCFC Tracker), not an old table. Add the rate you pay for exact costs.</p>
      <div className="mt-2 flex items-center gap-4 text-xs">
        <Link to={appPath("/settings?tab=charging#networks")} className="font-medium text-amber-300 hover:text-amber-200">
          Set your rate
        </Link>
        <button type="button" onClick={hide} className="text-neutral-400 hover:text-neutral-200">
          Got it
        </button>
      </div>
    </div>
  );
}
