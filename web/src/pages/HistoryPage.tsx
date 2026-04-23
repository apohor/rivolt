import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend, MachineError } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";

export default function HistoryPage() {
  const qc = useQueryClient();
  const shots = useQuery({
    queryKey: ["shots"],
    queryFn: () => backend.listShots(200),
    refetchInterval: 15_000,
  });
  const sparks = useQuery({
    queryKey: ["shots-metrics"],
    queryFn: () => backend.listShotMetrics(200, 24),
    refetchInterval: 30_000,
  });
  const status = useQuery({
    queryKey: ["shots-status"],
    queryFn: () => backend.shotsStatus(),
    refetchInterval: 15_000,
  });
  const sync = useMutation({
    mutationFn: () => backend.syncShots(),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["shots"] });
      await qc.invalidateQueries({ queryKey: ["shots-status"] });
      await qc.invalidateQueries({ queryKey: ["shots-metrics"] });
    },
  });
  const del = useMutation({
    mutationFn: (id: string) => backend.deleteShot(id),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["shots"] });
      await qc.invalidateQueries({ queryKey: ["shots-metrics"] });
      await qc.invalidateQueries({ queryKey: ["shots-status"] });
    },
  });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Shots"
        subtitle={
          status.data
            ? `${status.data.shots_cached} shots cached · last sync ${formatSync(status.data.last_sync)}`
            : "Synced from the machine to a local cache."
        }
        actions={
          <button
            type="button"
            disabled={sync.isPending}
            onClick={() => sync.mutate()}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800 disabled:opacity-50"
          >
            {sync.isPending ? "syncing…" : "Sync now"}
          </button>
        }
      />

      {status.data?.last_error && (
        <ErrorBox title="Last sync failed" detail={status.data.last_error} />
      )}
      {sync.error && (
        <ErrorBox
          title="Manual sync failed"
          detail={
            sync.error instanceof MachineError
              ? `${sync.error.status}: ${JSON.stringify(sync.error.body)}`
              : String(sync.error)
          }
        />
      )}
      {del.error && (
        <ErrorBox
          title="Could not delete shot"
          detail={
            del.error instanceof MachineError
              ? `${del.error.status}: ${JSON.stringify(del.error.body)}`
              : String(del.error)
          }
        />
      )}

      {shots.isLoading && <Spinner />}
      {shots.error && (
        <ErrorBox title="Could not load cached shots" detail={String(shots.error)} />
      )}
      {shots.data && (
        <Card>
          {shots.data.length === 0 ? (
            <div className="py-8 text-center text-sm text-neutral-500">
              No shots cached yet. Hit “Sync now”.
            </div>
          ) : (
            <ul className="divide-y divide-neutral-800">
              {shots.data.map((s) => {
                const m = sparks.data?.[s.id];
                const pts = m?.spark;
                return (
                  <li key={s.id} className="group flex items-center gap-1 py-2">
                    <Link
                      to={`/history/${encodeURIComponent(s.id)}`}
                      className="-mx-2 flex min-w-0 flex-1 items-center gap-3 rounded-md px-2 py-1.5 hover:bg-neutral-800/60"
                    >
                      <div className="h-12 w-16 shrink-0 overflow-hidden rounded-md border border-neutral-800 bg-neutral-950">
                        <Sparkline points={pts} />
                      </div>
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <div className="truncate text-sm text-neutral-100">{s.name || "(no name)"}</div>
                          {s.rating != null && s.rating > 0 && (
                            <span className="shrink-0 text-xs text-amber-400" aria-label={`${s.rating} of 5 stars`}>
                              {"★".repeat(s.rating)}
                              <span className="text-neutral-700">{"★".repeat(5 - s.rating)}</span>
                            </span>
                          )}
                        </div>
                        <div className="truncate text-xs text-neutral-500">
                          {/* Shot.name is almost always the profile name on Meticulous firmware.
                              Only show the profile breadcrumb when it actually adds information. */}
                          {s.profile_name && s.profile_name !== s.name
                            ? `${s.profile_name} · `
                            : ""}
                          {formatShotStats(s.sample_count, m)}
                          {s.note ? " · note" : ""}
                        </div>
                      </div>
                      <div className="shrink-0 text-right text-xs text-neutral-400">
                        <div>{formatWhen(s.time)}</div>
                      </div>
                    </Link>
                    <button
                      type="button"
                      aria-label="Delete shot"
                      title="Delete shot"
                      disabled={del.isPending && del.variables === s.id}
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        if (confirm(`Delete "${s.name || s.id.slice(0, 8)}" from history?`)) {
                          del.mutate(s.id);
                        }
                      }}
                      className="ml-1 shrink-0 rounded-md border border-transparent p-1.5 text-neutral-500 opacity-0 transition hover:border-red-900 hover:bg-red-950/30 hover:text-red-300 focus:opacity-100 group-hover:opacity-100 disabled:opacity-40"
                    >
                      <svg
                        viewBox="0 0 16 16"
                        className="h-4 w-4"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="1.5"
                        aria-hidden="true"
                      >
                        <path d="M3 4h10M6.5 4V2.5A.5.5 0 0 1 7 2h2a.5.5 0 0 1 .5.5V4M5 4l.5 9a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1L11 4" />
                      </svg>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </Card>
      )}
    </div>
  );
}

function formatWhen(seconds: number): string {
  if (!seconds) return "";
  return new Date(seconds * 1000).toLocaleString();
}

// Meticulous firmware streams sensor samples at ~10 Hz, so sample_count
// is effectively a duration proxy. Users recognise "24s" much faster
// than "243 samples".
function formatShotDuration(sampleCount: number): string {
  if (!sampleCount) return "0.0s";
  return `${(sampleCount / 10).toFixed(1)}s`;
}

// Compact headline for the list row: duration · yield · peak. Any field
// that's missing or zero is skipped so freshly-ingested shots that
// haven't been post-processed yet don't render "0g · 0 bar".
function formatShotStats(
  sampleCount: number,
  m: { peak_pressure?: number; final_weight?: number } | undefined,
): string {
  const parts: string[] = [formatShotDuration(sampleCount)];
  if (m?.final_weight && m.final_weight > 0) {
    parts.push(`${m.final_weight.toFixed(1)}g`);
  }
  if (m?.peak_pressure && m.peak_pressure > 0) {
    parts.push(`${m.peak_pressure.toFixed(1)} bar`);
  }
  return parts.join(" · ");
}

function formatSync(iso: string): string {
  if (!iso) return "never";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString();
}

// Sparkline renders a downsampled pressure series as a tiny polyline
// inside its container. Values are normalised to 0..1 of the container
// height. Returns an empty box when no points are available.
function Sparkline({ points }: { points?: number[] }) {
  if (!points || points.length < 2) {
    return <div className="h-full w-full" />;
  }
  const w = 64;
  const h = 48;
  const pad = 4;
  let min = points[0];
  let max = points[0];
  for (const v of points) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const range = max - min || 1;
  const dx = (w - pad * 2) / (points.length - 1);
  const d = points
    .map((v, i) => {
      const x = pad + i * dx;
      const y = h - pad - ((v - min) / range) * (h - pad * 2);
      return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  return (
    <svg
      viewBox={`0 0 ${w} ${h}`}
      preserveAspectRatio="none"
      className="h-full w-full"
      aria-hidden="true"
    >
      <path d={d} fill="none" stroke="#34d399" strokeWidth={1.25} strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}
