import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { Card, PageHeader, Spinner, ErrorBox } from "../components/ui";
import {
  durationSeconds,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";

export default function HomePage() {
  const health = useQuery({
    queryKey: ["health"],
    queryFn: () => backend.health(),
    refetchInterval: 30_000,
  });
  const drives = useQuery({
    queryKey: ["drives", 10],
    queryFn: () => backend.drives(10),
  });
  const charges = useQuery({
    queryKey: ["charges", 10],
    queryFn: () => backend.charges(10),
  });

  // Headline stats from the most recent 10 drives/charges. A future
  // "last N days" window is better, but this is a useful starting signal.
  const recentDrives = drives.data ?? [];
  const recentCharges = charges.data ?? [];
  const totalMiles = recentDrives.reduce((a, d) => a + (d.DistanceMi || 0), 0);
  const totalKWh = recentCharges.reduce((a, c) => a + (c.EnergyAddedKWh || 0), 0);
  const latestSoC = recentDrives[0]?.EndSoCPct ?? recentCharges[0]?.EndSoCPct ?? 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Overview"
        subtitle={
          health.data
            ? `Rivolt ${health.data.version} · connected`
            : health.isError
              ? "Rivolt backend unreachable"
              : "connecting…"
        }
      />

      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <Stat label="Battery (latest)" value={pct(latestSoC, 0)} />
        <Stat label="Miles (last 10 drives)" value={num(totalMiles, 1, "mi")} />
        <Stat label="Energy (last 10 charges)" value={num(totalKWh, 1, "kWh")} />
        <Stat label="Sessions stored" value={`${recentDrives.length}d · ${recentCharges.length}c`} />
      </div>

      <Card
        title="Recent drives"
        actions={
          <Link to="/drives" className="text-xs text-emerald-400 hover:underline">
            all drives →
          </Link>
        }
      >
        {drives.isLoading ? (
          <Spinner />
        ) : drives.isError ? (
          <ErrorBox title="Failed to load drives" detail={String(drives.error)} />
        ) : recentDrives.length === 0 ? (
          <EmptyState
            kind="drives"
            note="Import an ElectraFi CSV or connect a Rivian account to start populating data."
          />
        ) : (
          <ul className="divide-y divide-neutral-800">
            {recentDrives.slice(0, 5).map((d) => (
              <li key={d.ID} className="py-2 flex items-center justify-between text-sm">
                <span className="text-neutral-300">{formatDateTime(d.StartedAt)}</span>
                <span className="text-neutral-400 tabular-nums">
                  {num(d.DistanceMi, 1, "mi")} · {pct(d.StartSoCPct)}→{pct(d.EndSoCPct)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card
        title="Recent charges"
        actions={
          <Link to="/charges" className="text-xs text-emerald-400 hover:underline">
            all charges →
          </Link>
        }
      >
        {charges.isLoading ? (
          <Spinner />
        ) : charges.isError ? (
          <ErrorBox title="Failed to load charges" detail={String(charges.error)} />
        ) : recentCharges.length === 0 ? (
          <EmptyState kind="charges" />
        ) : (
          <ul className="divide-y divide-neutral-800">
            {recentCharges.slice(0, 5).map((c) => (
              <li key={c.ID} className="py-2 flex items-center justify-between text-sm">
                <span className="text-neutral-300">{formatDateTime(c.StartedAt)}</span>
                <span className="text-neutral-400 tabular-nums">
                  {formatDuration(durationSeconds(c.StartedAt, c.EndedAt))} ·{" "}
                  {pct(c.StartSoCPct)}→{pct(c.EndSoCPct)} · {num(c.EnergyAddedKWh, 1, "kWh")}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-3">
      <div className="text-xs text-neutral-500">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums">{value}</div>
    </div>
  );
}

function EmptyState({ kind, note }: { kind: string; note?: string }) {
  return (
    <div className="text-sm text-neutral-400">
      <p>No {kind} yet.</p>
      {note ? <p className="mt-1 text-xs text-neutral-500">{note}</p> : null}
    </div>
  );
}
