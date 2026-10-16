// PowerSpark: charging power as a live sparkline with a soft glow, used only by
// the ui_theme_next layer while a session is active.
export function PowerSpark({ values, width = 180, height = 36 }: { values: number[]; width?: number; height?: number }) {
  if (values.length < 2) return null;
  let hi = Math.max(...values, 1);
  let lo = Math.min(...values);
  // A steady session would draw a flat edge: give it a minimum range so the
  // line sits mid-height.
  if (hi - lo < hi * 0.2) {
    const mid = (hi + lo) / 2;
    lo = mid - hi * 0.1;
    hi = mid + hi * 0.1;
  }
  const span = Math.max(1e-6, hi - lo);
  const pad = 3;
  const xs = (i: number) => pad + (i / (values.length - 1)) * (width - pad * 2);
  const ys = (v: number) => height - pad - ((v - lo) / span) * (height - pad * 2);
  const pts = values.map((v, i) => `${xs(i).toFixed(1)},${ys(v).toFixed(1)}`);
  const line = `M ${pts.join(" L ")}`;
  const area = `${line} L ${xs(values.length - 1).toFixed(1)},${height} L ${xs(0).toFixed(1)},${height} Z`;
  const last = values[values.length - 1];
  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      className="ui-spark h-9 w-full min-w-0"
      preserveAspectRatio="none"
      role="img"
      aria-label={`Charging power over the last ${values.length} readings, now ${Math.round(last)} kilowatts`}
    >
      <defs>
        <linearGradient id="ps-fill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="oklch(78% 0.15 168)" stopOpacity={0.35} />
          <stop offset="100%" stopColor="oklch(78% 0.15 168)" stopOpacity={0} />
        </linearGradient>
      </defs>
      <path d={area} fill="url(#ps-fill)" />
      <path d={line} fill="none" stroke="oklch(80% 0.15 168)" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round" vectorEffect="non-scaling-stroke" className="ui-spark-line" />
      <circle cx={xs(values.length - 1)} cy={ys(last)} r={2.6} fill="oklch(84% 0.14 168)" className="ui-spark-dot" />
    </svg>
  );
}
