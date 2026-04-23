import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { backend, type Charge } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import {
  durationSeconds,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";

export default function ChargesPage() {
  const q = useQuery({
    queryKey: ["charges", 100],
    queryFn: () => backend.charges(100),
  });

  return (
    <div className="space-y-4">
      <PageHeader
        title="Charges"
        subtitle={
          q.data ? `${q.data.length} most recent charging sessions` : undefined
        }
      />
      <Card>
        {q.isLoading ? (
          <Spinner />
        ) : q.isError ? (
          <ErrorBox title="Failed to load charges" detail={String(q.error)} />
        ) : !q.data || q.data.length === 0 ? (
          <p className="text-sm text-neutral-400">No charges yet.</p>
        ) : (
          <ChargeTable charges={q.data} />
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
              <td className="py-2 pr-4 text-neutral-500">{c.FinalState || "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
