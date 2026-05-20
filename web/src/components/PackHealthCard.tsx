import { useQuery } from "@tanstack/react-query";
import { backend, type PackHealthSample } from "../lib/api";

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
    return <div className="h-24 animate-pulse rounded bg-neutral-900/50" />;
  }
  if (q.isError || !q.data) {
    return (
      <p className="text-xs text-neutral-500">
        Couldn&apos;t load pack-health data.
      </p>
    );
  }
  const { samples, headline } = q.data;
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

  return (
    <div className="space-y-2">
      <div className="flex items-baseline gap-2">
        <span className="text-2xl font-semibold text-neutral-100">
          {headlineText}
        </span>
        {pctText ? (
          <span className="text-xs text-neutral-400">{pctText}</span>
        ) : null}
      </div>
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
