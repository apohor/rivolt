import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { backend, type Drive } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { WindowPicker } from "../components/WindowPicker";
import { filterByWindow, type WindowKey } from "../lib/analytics";
import {
  durationSeconds,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";

export default function DrivesPage() {
  const [win, setWin] = useState<WindowKey>("30d");
  const q = useQuery({
    queryKey: ["drives", "all"],
    queryFn: () => backend.allDrives(),
  });
  const rows = useMemo(
    () => filterByWindow(q.data ?? [], win),
    [q.data, win],
  );

  return (
    <div className="space-y-4">
      <PageHeader
        title="Drives"
        subtitle={
          q.data
            ? `${rows.length} of ${q.data.length} drive sessions`
            : undefined
        }
        actions={<WindowPicker value={win} onChange={setWin} />}
      />
      <Card>
        {q.isLoading ? (
          <Spinner />
        ) : q.isError ? (
          <ErrorBox title="Failed to load drives" detail={String(q.error)} />
        ) : rows.length === 0 ? (
          <p className="text-sm text-neutral-400">
            No drives in this window.
          </p>
        ) : (
          <DriveTable drives={rows} />
        )}
      </Card>
    </div>
  );
}

function DriveTable({ drives }: { drives: Drive[] }) {
  const navigate = useNavigate();
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
            <tr
              key={d.ID}
              className="cursor-pointer hover:bg-neutral-900/60"
              onClick={() => navigate(`/drives/${d.ID}`)}
            >
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
