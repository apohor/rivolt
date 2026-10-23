import { useCountUp } from "../lib/uiNext";

// ArcGauge: battery as a 240-degree arc with an animated fill. Used only by
// the app. The glow is reserved for live states.
export function ArcGauge({
  value,
  label,
  live = false,
  tone = "ok",
  size = 132,
  limit = null,
  children,
}: {
  value: number; // 0..100
  label: string;
  live?: boolean;
  tone?: "ok" | "critical" | "idle";
  size?: number;
  // Charge limit (0..100): drawn as a tick on the arc.
  limit?: number | null;
  // Rendered in the centre; receives the animated value.
  children?: (shown: number) => React.ReactNode;
}) {
  const pct = Math.max(0, Math.min(100, value));
  const shown = useCountUp(pct, true);
  const r = 52;
  const cx = 60;
  const cy = 60;
  const sweep = 240;
  const start = 150; // degrees, measured clockwise from 3 o'clock
  const pt = (deg: number): [number, number] => [cx + r * Math.cos((deg * Math.PI) / 180), cy + r * Math.sin((deg * Math.PI) / 180)];
  const [x0, y0] = pt(start);
  const [x1, y1] = pt(start + sweep);
  const arc = `M ${x0.toFixed(2)} ${y0.toFixed(2)} A ${r} ${r} 0 1 1 ${x1.toFixed(2)} ${y1.toFixed(2)}`;
  const len = (Math.PI * r * sweep) / 180;
  const stroke = tone === "critical" ? "url(#ag-crit)" : tone === "idle" ? "url(#ag-idle)" : "url(#ag-ok)";
  return (
    <div className="ui-gauge relative shrink-0" style={{ width: size, height: size * 0.9 }} data-live={live ? "true" : undefined}>
      <svg viewBox="0 0 120 108" width={size} height={size * 0.9} role="img" aria-label={label}>
        <defs>
          <linearGradient id="ag-ok" x1="0" y1="0" x2="1" y2="1">
            <stop offset="0%" stopColor="oklch(78% 0.15 168)" />
            <stop offset="100%" stopColor="oklch(70% 0.13 190)" />
          </linearGradient>
          <linearGradient id="ag-crit" x1="0" y1="0" x2="1" y2="1">
            <stop offset="0%" stopColor="oklch(72% 0.19 25)" />
            <stop offset="100%" stopColor="oklch(66% 0.2 15)" />
          </linearGradient>
          <linearGradient id="ag-idle" x1="0" y1="0" x2="1" y2="1">
            <stop offset="0%" stopColor="oklch(62% 0.02 260)" />
            <stop offset="100%" stopColor="oklch(55% 0.02 260)" />
          </linearGradient>
        </defs>
        <path d={arc} fill="none" stroke="currentColor" strokeOpacity={0.14} strokeWidth={7} strokeLinecap="round" className="text-neutral-300" />
        {limit != null && limit > 0 && limit <= 100 && (() => {
          const a = ((start + (sweep * limit) / 100) * Math.PI) / 180;
          const x1 = cx + (r - 8) * Math.cos(a);
          const y1 = cy + (r - 8) * Math.sin(a);
          const x2 = cx + (r + 7) * Math.cos(a);
          const y2 = cy + (r + 7) * Math.sin(a);
          return (
            <line
              data-testid="gauge-limit"
              x1={x1.toFixed(2)}
              y1={y1.toFixed(2)}
              x2={x2.toFixed(2)}
              y2={y2.toFixed(2)}
              stroke="currentColor"
              strokeWidth={2}
              strokeLinecap="round"
              className="text-neutral-100"
            />
          );
        })()}
        <path
          d={arc}
          fill="none"
          stroke={stroke}
          strokeWidth={7}
          strokeLinecap="round"
          strokeDasharray={`${len} ${len}`}
          strokeDashoffset={len * (1 - shown / 100)}
          className="ui-gauge-fill"
        />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center pt-1">{children?.(shown)}</div>
    </div>
  );
}
