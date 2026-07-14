import { useQuery } from "@tanstack/react-query";
import { backend, type PackHealthSample } from "../lib/api";

// Inline copy of HomePage's local Stat tile shape. Kept here so
// PackHealthStat can be rendered inside the existing KPI grid
// without exporting the Stat helper from HomePage.tsx.
function StatTile({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-3">
      <div className="text-xs text-neutral-500">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums">{value}</div>
      {hint ? (
        <div className="mt-1 text-[11px] text-neutral-500 tabular-nums">{hint}</div>
      ) : null}
    </div>
  );
}

// PackHealthStat is the compact KPI-tile variant rendered in
// HomePage's top stats row next to Cost / Efficiency. Shows
// just the headline number; the larger PackHealthCard (with
// sparkline) is available for a future dedicated detail view.
export function PackHealthStat({ vehicleID }: { vehicleID: string }) {
  const q = useQuery({
    queryKey: ["packHealth", vehicleID],
    queryFn: () => backend.packHealth(vehicleID),
    enabled: !!vehicleID,
    staleTime: 5 * 60_000,
  });
  if (q.isLoading || q.isError || !q.data) {
    return <StatTile label="Pack" value="—" />;
  }
  const samples = q.data.samples ?? [];
  const { headline } = q.data;
  if (samples.length === 0 || headline.effective_kwh <= 0) {
    return <StatTile label="Pack" value="—" hint="needs a 30%+ session" />;
  }
  const pct =
    headline.pct_of_nameplate > 0
      ? `${headline.pct_of_nameplate.toFixed(0)}% of nameplate · ${headline.sample_count} sessions`
      : `${headline.sample_count} sessions`;
  return (
    <StatTile
      label="Pack"
      value={`${headline.effective_kwh.toFixed(0)} kWh`}
      hint={pct}
    />
  );
}

// PackHealthCard renders the derived effective-pack-capacity for a
// vehicle: a single headline number ("127 kWh · 97% of nameplate"),
// the sample count, and a small SVG sparkline of recent samples.
// Rivian doesn't expose SoH directly so this is the closest signal
// to "is my battery aging?" the user can get without 3rd-party
// hardware.
export function PackHealthCard({ vehicleID }: { vehicleID: string }) {
  const q = useQuery({
    queryKey: ["packHealth", vehicleID],
    queryFn: () => backend.packHealth(vehicleID),
    enabled: !!vehicleID,
    staleTime: 5 * 60_000,
  });

  if (q.isLoading) {
    return <div className="h-24 animate-pulse rounded-sm bg-neutral-900/50" />;
  }
  if (q.isError || !q.data) {
    return (
      <p className="text-xs text-neutral-500">
        Couldn&apos;t load pack-health data.
      </p>
    );
  }
  // Go marshals a nil slice as JSON null, not []. Defensive coalesce
  // so the empty state renders instead of crashing the page on a
  // brand-new install with no qualifying charges yet.
  const samples = q.data.samples ?? [];
  const headline = q.data.headline;
  if (samples.length === 0) {
    return (
      <p className="text-xs text-neutral-500">
        Not enough qualifying charge sessions yet. We need at least
        one session that spans 30%+ SoC to estimate effective pack
        capacity.
      </p>
    );
  }
  const cleanSamples = samples.filter((s) => !s.derate_active);
  const headlineText =
    headline.effective_kwh > 0
      ? `${headline.effective_kwh.toFixed(0)} kWh`
      : "—";
  const pctText =
    headline.pct_of_nameplate > 0
      ? `${headline.pct_of_nameplate.toFixed(0)}% of nameplate`
      : headline.nameplate_kwh > 0
        ? ""
        : "(no nameplate set on this vehicle)";

  // Documented (nameplate spec) vs current (vehicle-reported usable
  // capacity). Shown as the headline when the vehicle has reported a
  // capacity that differs from spec — the car's own degradation signal.
  const reported = headline.reported_kwh ?? 0;
  const documented = headline.documented_kwh ?? 0;
  const reportedPct = headline.reported_pct_of_documented ?? 0;
  const hasReported = reported > 0 && documented > 0 && reportedPct < 99.5;

  return (
    <div className="space-y-2">
      {hasReported ? (
        <div>
          <div className="flex items-baseline gap-2">
            <span className="text-2xl font-semibold text-neutral-100 tabular-nums">
              {reported.toFixed(1)} kWh
            </span>
            <span className="text-xs text-neutral-400">
              current · vehicle-reported
            </span>
          </div>
          <div className="text-xs text-neutral-500 tabular-nums">
            {documented.toFixed(0)} kWh documented (nameplate) ·{" "}
            <span className="text-amber-400">
              {reportedPct.toFixed(0)}% — {(100 - reportedPct).toFixed(0)}% degradation
            </span>
          </div>
        </div>
      ) : (
        <div className="flex items-baseline gap-2">
          <span className="text-2xl font-semibold text-neutral-100">
            {headlineText}
          </span>
          {pctText ? (
            <span className="text-xs text-neutral-400">{pctText}</span>
          ) : null}
        </div>
      )}
      <Sparkline samples={cleanSamples.length > 0 ? cleanSamples : samples} />
      <p className="text-[11px] text-neutral-500">
        Median of the last {headline.window} clean session
        {headline.window === 1 ? "" : "s"} · {headline.sample_count} total
      </p>
    </div>
  );
}

// Sparkline is a dependency-free SVG plot of pack_kwh_effective over
// time. Kept here rather than in components/charts.tsx because the
// shape is small enough that LineChart's full-feature engine would
// be overkill. Axis-free: just the trace + a dim baseline.
function Sparkline({ samples }: { samples: PackHealthSample[] }) {
  if (samples.length < 2) {
    return (
      <p className="text-[11px] text-neutral-500">
        Need at least two samples to draw a trend.
      </p>
    );
  }
  const width = 240;
  const height = 56;
  const padding = 4;
  const xs = samples.map((s) => new Date(s.at).getTime());
  const ys = samples.map((s) => s.pack_kwh_effective);
  const xMin = Math.min(...xs);
  const xMax = Math.max(...xs);
  // Use a tight Y range so small drifts (a few kWh) are visible.
  // Half the average value as the floor of the visible range
  // anchors the chart even when no big swings have happened.
  const yLo = Math.min(...ys);
  const yHi = Math.max(...ys);
  const yPad = Math.max((yHi - yLo) * 0.15, 1);
  const yMin = yLo - yPad;
  const yMax = yHi + yPad;
  const sx = (x: number) =>
    padding + ((x - xMin) / Math.max(1, xMax - xMin)) * (width - padding * 2);
  const sy = (y: number) =>
    height - padding - ((y - yMin) / Math.max(0.001, yMax - yMin)) * (height - padding * 2);
  const points = samples
    .map((s) => `${sx(new Date(s.at).getTime()).toFixed(1)},${sy(s.pack_kwh_effective).toFixed(1)}`)
    .join(" ");
  const last = samples[samples.length - 1];
  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      className="h-14 w-full text-emerald-400"
      preserveAspectRatio="none"
      role="img"
      aria-label="Effective pack capacity over time"
    >
      <polyline
        points={points}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
      />
      <circle
        cx={sx(new Date(last.at).getTime())}
        cy={sy(last.pack_kwh_effective)}
        r="2.5"
        fill="currentColor"
      />
    </svg>
  );
}
