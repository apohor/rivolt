import { useQuery } from "@tanstack/react-query";
import { backend, type Drive } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import {
  durationSeconds,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";

export default function DrivesPage() {
  const q = useQuery({
    queryKey: ["drives", 100],
    queryFn: () => backend.drives(100),
  });

  return (
    <div className="space-y-4">
      <PageHeader
        title="Drives"
        subtitle={
          q.data ? `${q.data.length} most recent drive sessions` : undefined
        }
      />
      <Card>
        {q.isLoading ? (
          <Spinner />
        ) : q.isError ? (
          <ErrorBox title="Failed to load drives" detail={String(q.error)} />
        ) : !q.data || q.data.length === 0 ? (
          <p className="text-sm text-neutral-400">
            No drives yet. Import an ElectraFi CSV with <code>rivolt import electrafi</code>.
          </p>
        ) : (
          <DriveTable drives={q.data} />
        )}
      </Card>
    </div>
  );
}

function DriveTable({ drives }: { drives: Drive[] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs uppercase tracking-wide text-neutral-500">
            <th className="py-2 pr-4 font-medium">Start</th>
            <th className="py-2 pr-4 font-medium">Duration</th>
            <th className="py-2 pr-4 font-medium">Distance</th>
            <th className="py-2 pr-4 font-medium">SoC</th>
            <th className="py-2 pr-4 font-medium">Avg / Max</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-neutral-800">
          {drives.map((d) => (
            <tr key={d.ID} className="hover:bg-neutral-900/40">
              <td className="py-2 pr-4 text-neutral-300 whitespace-nowrap">
                {formatDateTime(d.StartedAt)}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {formatDuration(durationSeconds(d.StartedAt, d.EndedAt))}
              </td>
              <td className="py-2 pr-4 text-neutral-200 tabular-nums">
                {num(d.DistanceMi, 1, "mi")}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {pct(d.StartSoCPct)} → {pct(d.EndSoCPct)}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {num(d.AvgSpeedMph, 0)} / {num(d.MaxSpeedMph, 0)} mph
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
