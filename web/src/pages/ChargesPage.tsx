import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { backend, type Charge } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { WindowPicker } from "../components/WindowPicker";
import { filterByWindow, type WindowKey } from "../lib/analytics";
import {
  durationSeconds,
  formatChargeState,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";

export default function ChargesPage() {
  const [win, setWin] = useState<WindowKey>("30d");
  const q = useQuery({
    queryKey: ["charges", "all"],
    queryFn: () => backend.allCharges(),
  });
  const rows = useMemo(
    () => filterByWindow(q.data ?? [], win),
    [q.data, win],
  );
  const totals = useMemo(() => summarize(rows), [rows]);

  return (
    <div className="space-y-4">
      <PageHeader
        title="Charges"
        subtitle={
          q.data
            ? `${rows.length} of ${q.data.length} charging sessions`
            : undefined
        }
        actions={<WindowPicker value={win} onChange={setWin} />}
      />
      <Card>
        {q.isLoading ? (
          <Spinner />
        ) : q.isError ? (
          <ErrorBox title="Failed to load charges" detail={String(q.error)} />
        ) : rows.length === 0 ? (
          <p className="text-sm text-neutral-400">No charges in this window.</p>
        ) : (
          <div className="space-y-3">
            <SummaryStrip totals={totals} />
            <ChargeTable charges={rows} />
          </div>
        )}
      </Card>
    </div>
  );
}

function ChargeTable({ charges }: { charges: Charge[] }) {
  const navigate = useNavigate();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs uppercase tracking-wide text-neutral-500">
            <th className="py-2 pr-4 font-medium">Start</th>
            <th className="py-2 pr-4 font-medium">Duration</th>
            <th className="py-2 pr-4 font-medium">SoC</th>
            <th className="py-2 pr-4 font-medium">Energy</th>
            <th className="py-2 pr-4 font-medium">Max kW</th>
            <th className="py-2 pr-4 font-medium">Cost</th>
            <th className="py-2 pr-4 font-medium">Final state</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-neutral-800">
          {charges.map((c) => (
            <tr
              key={c.ID}
              className="cursor-pointer hover:bg-neutral-900/60"
              onClick={() => navigate(`/charges/${c.ID}`)}
            >
              <td className="py-2 pr-4 text-neutral-300 whitespace-nowrap">
                {formatDateTime(c.StartedAt)}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {formatDuration(durationSeconds(c.StartedAt, c.EndedAt))}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {pct(c.StartSoCPct)} → {pct(c.EndSoCPct)}
              </td>
              <td className="py-2 pr-4 text-neutral-200 tabular-nums">
                {num(c.EnergyAddedKWh, 1, "kWh")}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {num(c.MaxPowerKW, 1)}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {c.Cost > 0
                  ? `${c.Cost.toFixed(2)} ${c.Currency ?? ""}`.trim()
                  : c.estimated_cost && c.estimated_cost > 0
                    ? `~${c.estimated_cost.toFixed(2)} ${c.estimated_currency ?? ""}`.trim()
                    : "—"}
              </td>
              <td className="py-2 pr-4 text-neutral-500">{formatChargeState(c.FinalState)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ChargeTotals collapses the filtered rows into a single topline.
// Cost prefers the persisted value snapshotted at charge-close time;
// for legacy/imported rows that lack one we fall back to the current
// home $/kWh rate as surfaced via estimated_cost on the same row.
// Currency is whatever the first row contributes — in practice all
// sessions for one operator share a currency.
type ChargeTotals = {
  count: number;
  durationSec: number;
  energyKWh: number;
  cost: number;
  estimated: boolean; // true if any portion of cost came from estimate
  currency: string;
};

function summarize(rows: Charge[]): ChargeTotals {
  const t: ChargeTotals = {
    count: rows.length,
    durationSec: 0,
    energyKWh: 0,
    cost: 0,
    estimated: false,
    currency: "",
  };
  for (const r of rows) {
    t.durationSec += durationSeconds(r.StartedAt, r.EndedAt);
    t.energyKWh += r.EnergyAddedKWh || 0;
    if (r.Cost > 0) {
      t.cost += r.Cost;
      if (!t.currency) t.currency = r.Currency;
    } else if (r.estimated_cost && r.estimated_cost > 0) {
      t.cost += r.estimated_cost;
      t.estimated = true;
      if (!t.currency && r.estimated_currency) t.currency = r.estimated_currency;
    }
  }
  return t;
}

function SummaryStrip({ totals }: { totals: ChargeTotals }) {
  return (
    <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      <Stat label="Sessions" value={String(totals.count)} />
      <Stat label="Duration" value={formatDuration(totals.durationSec)} />
      <Stat label="Energy" value={num(totals.energyKWh, 1, "kWh")} />
      <Stat
        label="Cost"
        value={
          totals.cost > 0
            ? `${totals.estimated ? "~" : ""}${totals.cost.toFixed(2)}${totals.currency ? ` ${totals.currency}` : ""}`
            : "—"
        }
        hint={
          totals.estimated
            ? "includes estimated cost for sessions without a persisted price"
            : undefined
        }
      />
    </dl>
  );
}

function Stat({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div className="rounded-lg border border-neutral-800 bg-neutral-900/40 px-3 py-2">
      <dt className="text-[11px] uppercase tracking-wide text-neutral-500">
        {label}
      </dt>
      <dd className="mt-0.5 text-lg font-semibold tabular-nums text-neutral-100">
        {value}
      </dd>
      {hint ? (
        <div className="mt-0.5 text-[11px] text-neutral-500">{hint}</div>
      ) : null}
    </div>
  );
}
