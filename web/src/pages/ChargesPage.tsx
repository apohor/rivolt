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
          <ChargeTable charges={rows} />
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
                {c.estimated_cost && c.estimated_cost > 0
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
