// Tiny dependency-free SVG charts. Good enough for sparklines and
// overview dashboards; if we ever need interactivity/zoom we can swap
// individual charts for uplot without touching call sites.

import {
  useCallback,
  useRef,
  useState,
  type CSSProperties,
} from "react";

type Point = { x: number; y: number };

export type LineSeries = {
  points: Point[];
  color?: string;
  strokeWidth?: number;
  // If true, fill the area below the line with a faded gradient.
  area?: boolean;
  label?: string;
  // Path interpolation. "monotone" uses Fritsch–Carlson cubic, which
  // preserves local extrema (no overshoot) so peaks like top speed
  // stay accurate while the line still looks smooth.
  curve?: "linear" | "monotone";
  // Optional SVG `stroke-dasharray` (e.g. "3 3"). Used for secondary
  // signals like ambient temperature where we want them visibly
  // de-emphasized against the primary speed/SoC traces.
  dash?: string;
  // Per-series override for the cursor value label. Useful when a
  // series shares an axis with another series of a different unit
  // (e.g. temperature riding on the SoC% axis), where the axis
  // formatter would print the wrong unit. Falls back to the
  // axis-level `formatY` / `formatY2`.
  formatCursor?: (y: number) => string;
  // Which Y-axis this series maps to. Defaults to "left". Use "right"
  // to overlay a second signal with a different unit (e.g. SoC% on
  // the left, charger kW on the right). The right axis only renders
  // when at least one series opts in.
  axis?: "left" | "right";
};

export function LineChart({
  series,
  height = 120,
  yDomain,
  xDomain,
  yTicks = 3,
  xTicks = 4,
  formatX,
  formatY,
  className,
  cursorX,
  onCursorChange,
  y2Domain,
  formatY2,
  bands,
  bandRows,
  backdrop,
}: {
  series: LineSeries[];
  height?: number;
  yDomain?: [number, number];
  xDomain?: [number, number];
  yTicks?: number;
  xTicks?: number;
  formatX?: (x: number) => string;
  formatY?: (y: number) => string;
  className?: string;
  // Controlled crosshair X in data units. When set, the chart renders
  // a vertical guide line and a dot on each series at the x value
  // closest to `cursorX`. Callers use this to keep multiple charts
  // (and the route map) synchronized to the same moment in time.
  cursorX?: number | null;
  // Hover/leave callback. Fires with the data-space x of the pointer
  // (snapped to the nearest sample of the first series) on
  // pointermove, and with `null` on pointerleave. Omit to disable
  // pointer interaction entirely.
  onCursorChange?: (x: number | null) => void;
  // Optional secondary Y-axis. Only used when at least one series
  // sets `axis: "right"`. Shares the X-axis with the primary axis.
  y2Domain?: [number, number];
  formatY2?: (y: number) => string;
  // Optional colored bands rendered as a thin strip along the
  // bottom of the chart area. Used to annotate intervals on the
  // x-axis without consuming a y-axis slot (e.g. precipitation
  // type segments under a temperature/wind chart). The strip is
  // ~4px tall and clips to the chart's x-domain. Each band may
  // include a `label` shown as a native SVG <title> tooltip.
  bands?: Array<{ x0: number; x1: number; color: string; label?: string }>;
  // Optional multi-row interval bands. Each row renders as its own
  // thin strip near the bottom of the chart. Useful for stacking
  // categorical timelines (for example drive mode + precipitation)
  // without needing a separate chart.
  bandRows?: Array<{
    label?: string;
    bands: Array<{ x0: number; x1: number; color: string; label?: string }>;
  }>;
  // Optional faint context layer drawn behind the main series.
  // Auto-fits to its own min/max (independent of left/right
  // axes), draws no axis labels, and renders as a low-opacity
  // area. Use for "third signal as backdrop" — e.g. elevation
  // behind a weather chart so the climb context is visible
  // without crowding the legible axes.
  backdrop?: {
    points: Point[];
    color?: string;
    label?: string;
    formatCursor?: (y: number) => string;
  };
}) {
  const width = 1000; // viewBox width, the SVG scales to container
  const svgRef = useRef<SVGSVGElement | null>(null);
  const roRef = useRef<ResizeObserver | null>(null);
  // preserveAspectRatio="none" stretches the viewBox non-uniformly, which
  // distorts text (squished on a phone, wide on desktop). Measure the
  // rendered box and counter-scale each label around its anchor (textTF,
  // below) so axis numbers, time marks and cursor values stay upright.
  // Hooks stay above the EmptyChart early returns (Rules of Hooks).
  const [aspect, setAspect] = useState({ fx: 1, fy: 1 });
  // Callback ref, NOT useEffect: async data makes this chart render
  // EmptyChart (no <svg>) on the first pass, so a mount-time effect would
  // measure a null node, bail, and never retry when the real <svg> mounts
  // later — leaving aspect at {1,1} and every label distorted (tall/thin
  // on a phone). A callback ref binds the observer exactly when the node
  // attaches, however late that is.
  const attachSvg = useCallback(
    (el: SVGSVGElement | null) => {
      svgRef.current = el;
      roRef.current?.disconnect();
      if (!el) return;
      const measure = () => {
        const r = el.getBoundingClientRect();
        if (r.width > 0 && r.height > 0) {
          setAspect({ fx: r.width / width, fy: r.height / height });
        }
      };
      measure();
      const ro = new ResizeObserver(measure);
      ro.observe(el);
      roRef.current = ro;
    },
    [height, width],
  );

  // Gutters are sized in *screen px* (÷ the stretch factor) so the
  // counter-scaled, fixed-size labels always fit — otherwise a
  // viewBox-unit gutter shrinks on a narrow phone and clips "12 kW" →
  // "12 k". Capped so a very narrow screen can't eat the whole plot.
  const hasRightAxis = series.some((s) => s.axis === "right");
  const gutterVB = (px: number) => Math.min(width * 0.28, px / aspect.fx);
  const padL = gutterVB(44);
  const padR = hasRightAxis ? gutterVB(48) : 8;
  const padT = 8;
  const padB = Math.min(height * 0.4, 22 / aspect.fy);
  const innerW = width - padL - padR;
  const innerH = height - padT - padB;

  const resolvedBandRows =
    bandRows && bandRows.length > 0
      ? bandRows
      : bands && bands.length > 0
        ? [{ bands }]
        : [];

  const all = series.flatMap((s) => s.points);
  const hasBackdrop = (backdrop?.points.length ?? 0) > 1;
  const hasBands = resolvedBandRows.some((r) => r.bands.length > 0);
  if (all.length === 0 && !hasBackdrop && !hasBands) {
    return <EmptyChart height={height} />;
  }

  // Split points by axis so each Y-domain is auto-fit only to its
  // own series. A right-axis signal with a different unit
  // (charger kW) shouldn't be squashed by the left-axis signal's
  // range.
  const leftAll = series
    .filter((s) => s.axis !== "right")
    .flatMap((s) => s.points);
  const rightAll = series
    .filter((s) => s.axis === "right")
    .flatMap((s) => s.points);

  const xs = [
    ...all.map((p) => p.x),
    ...(backdrop?.points.map((p) => p.x) ?? []),
    ...resolvedBandRows.flatMap((row) =>
      row.bands.flatMap((b) => [b.x0, b.x1]),
    ),
  ];
  if (xs.length === 0) {
    return <EmptyChart height={height} />;
  }
  const x0 = xDomain?.[0] ?? Math.min(...xs);
  const x1 = xDomain?.[1] ?? Math.max(...xs);
  const xSpan = Math.max(1e-9, x1 - x0);

  const leftYs =
    (leftAll.length > 0 ? leftAll : all).length > 0
      ? (leftAll.length > 0 ? leftAll : all).map((p) => p.y)
      : (backdrop?.points.map((p) => p.y) ?? [0, 1]);
  const y0 = yDomain?.[0] ?? Math.min(...leftYs);
  const y1 = yDomain?.[1] ?? Math.max(...leftYs);
  const ySpan = Math.max(1e-9, y1 - y0);

  const rightYs = rightAll.map((p) => p.y);
  const y20 =
    y2Domain?.[0] ?? (rightYs.length > 0 ? Math.min(...rightYs) : 0);
  const y21 =
    y2Domain?.[1] ?? (rightYs.length > 0 ? Math.max(...rightYs) : 1);
  const y2Span = Math.max(1e-9, y21 - y20);

  const sx = (x: number) => padL + ((x - x0) / xSpan) * innerW;
  const syLeft = (y: number) =>
    padT + innerH - ((y - y0) / ySpan) * innerH;
  const syRight = (y: number) =>
    padT + innerH - ((y - y20) / y2Span) * innerH;
  const syFor = (s: LineSeries) => (s.axis === "right" ? syRight : syLeft);
  // Backward-compat alias for non-axis-aware code paths below
  // (grid lines, area baseline) — those all live on the left axis.
  const sy = syLeft;

  const yTickValues = tickValues(y0, y1, yTicks);
  const y2TickValues = hasRightAxis ? tickValues(y20, y21, yTicks) : [];
  const xTickValues = tickValues(x0, x1, xTicks);

  const textTF = (tx: number, ty: number) =>
    `translate(${tx} ${ty}) scale(${1 / aspect.fx} ${1 / aspect.fy})`;

  // Convert a client pointer event to a data-space x value, clamped
  // to the visible domain. Uses the SVG's bounding rect so it works
  // regardless of CSS scaling (preserveAspectRatio="none" stretches
  // the viewBox to fit the container width).
  const eventToDataX = (clientX: number): number | null => {
    const svg = svgRef.current;
    if (!svg) return null;
    const rect = svg.getBoundingClientRect();
    if (rect.width === 0) return null;
    const vbX = ((clientX - rect.left) / rect.width) * width;
    if (vbX < padL || vbX > width - padR) return null;
    return x0 + ((vbX - padL) / innerW) * xSpan;
  };

  // Snap an arbitrary data x to the closest point in the first
  // series. Charts on the same page share the same time grid so
  // snapping to series[0] keeps the cursor anchored to a real
  // sample even when the pointer moves between samples.
  const snapToSample = (x: number): number => {
    const pts = series[0]?.points;
    if (!pts || pts.length === 0) return x;
    let best = pts[0].x;
    let bestD = Math.abs(pts[0].x - x);
    for (let i = 1; i < pts.length; i++) {
      const d = Math.abs(pts[i].x - x);
      if (d < bestD) {
        bestD = d;
        best = pts[i].x;
      }
    }
    return best;
  };

  // Resolve the cursor sample for each series at the controlled
  // cursorX (snapped per-series). Used to position dots and labels.
  const cursorSamples =
    cursorX != null && Number.isFinite(cursorX)
      ? series.map((s) => {
          const pts = s.points;
          if (!pts || pts.length === 0) return null;
          let best = pts[0];
          let bestD = Math.abs(pts[0].x - cursorX);
          for (let i = 1; i < pts.length; i++) {
            const d = Math.abs(pts[i].x - cursorX);
            if (d < bestD) {
              bestD = d;
              best = pts[i];
            }
          }
          return best;
        })
      : null;
  const cursorXClamped =
    cursorX != null && Number.isFinite(cursorX)
      ? Math.min(Math.max(cursorX, x0), x1)
      : null;

  return (
    <svg
      ref={attachSvg}
      viewBox={`0 0 ${width} ${height}`}
      className={`w-full ${className ?? ""}`}
      // Explicit pixel height: with w-full + preserveAspectRatio="none"
      // and no height, the SVG derives height from *width*, so on a
      // narrow phone the chart collapses to a short strip (~70px). Pin
      // the rendered height to the intended value instead.
      style={{ height }}
      preserveAspectRatio="none"
      role="img"
    >
      {/* grid + y axis labels */}
      {yTickValues.map((yv, i) => (
        <g key={`y${i}`}>
          <line
            x1={padL}
            x2={width - padR}
            y1={sy(yv)}
            y2={sy(yv)}
            stroke="currentColor"
            className="text-neutral-800"
            strokeWidth={1}
          />
          <g transform={textTF(padL - 6, sy(yv) + 3)}>
            <text textAnchor="end" className="fill-neutral-500" fontSize={10}>
              {formatY ? formatY(yv) : yv.toFixed(0)}
            </text>
          </g>
        </g>
      ))}
      {/* x axis labels — first/last anchor to the edge so they can't
          overflow past the chart bounds and get clipped. */}
      {xTickValues.map((xv, i) => {
        const anchor: "start" | "middle" | "end" =
          i === 0 ? "start" : i === xTickValues.length - 1 ? "end" : "middle";
        return (
          <g key={`x${i}`} transform={textTF(sx(xv), height - 6)}>
            <text textAnchor={anchor} className="fill-neutral-500" fontSize={10}>
              {formatX ? formatX(xv) : xv.toFixed(0)}
            </text>
          </g>
        );
      })}
      {/* right y axis labels (no extra grid; the left-axis grid
          already spans the chart). Drawing only labels keeps the
          background clean when two unrelated signals overlay. */}
      {hasRightAxis &&
        y2TickValues.map((yv, i) => (
          <g key={`y2-${i}`} transform={textTF(width - padR + 6, syRight(yv) + 3)}>
            <text
              textAnchor="start"
              className="fill-neutral-500"
              fontSize={10}
            >
              {formatY2 ? formatY2(yv) : yv.toFixed(0)}
            </text>
          </g>
        ))}
      {/* x-axis bands: thin colored strip at the bottom of the
          plotting area for interval annotations (e.g.
          precipitation type segments). Drawn before series so the
          data line stays visible on top. Each band clips to the
          visible x-domain so out-of-range bands disappear cleanly
          when the user pans/zooms in the future. */}
      {resolvedBandRows.map((row, rowIdx) =>
        row.bands.map((b, i) => {
          const bx0 = Math.max(sx(b.x0), padL);
          const bx1 = Math.min(sx(b.x1), width - padR);
          if (bx1 <= bx0) return null;
          const stripH = 4;
          const stripGap = 2;
          const stripY =
            padT +
            innerH -
            stripH -
            rowIdx * (stripH + stripGap);
          return (
            <rect
              key={`band-${rowIdx}-${i}`}
              x={bx0}
              y={stripY}
              width={bx1 - bx0}
              height={stripH}
              fill={b.color}
              opacity={0.85}
            >
              {b.label ? <title>{b.label}</title> : null}
            </rect>
          );
        }),
      )}
      {/* backdrop: faint context area drawn behind primary series.
          Has its own y-scale (min..max of its own points) confined
          to ~70% of the inner height so the main chart breathes,
          and contributes no axis labels. */}
      {(() => {
        if (!backdrop || backdrop.points.length < 2) return null;
        const ys = backdrop.points.map((p) => p.y);
        const lo = Math.min(...ys);
        const hi = Math.max(...ys);
        const span = Math.max(1e-9, hi - lo);
        const bandH = innerH * 0.7;
        const baseY = padT + innerH;
        const syB = (y: number) => baseY - ((y - lo) / span) * bandH;
        const proj = backdrop.points.map((p) => ({
          x: sx(p.x),
          y: syB(p.y),
        }));
        const path = monotonePath(proj);
        const last = proj[proj.length - 1];
        const first = proj[0];
        const color = backdrop.color ?? "#a78bfa";
        return (
          <g>
            <path
              d={`${path} L ${last.x.toFixed(2)},${baseY.toFixed(2)} L ${first.x.toFixed(2)},${baseY.toFixed(2)} Z`}
              fill={color}
              opacity={0.10}
            />
            <path
              d={path}
              fill="none"
              stroke={color}
              strokeWidth={1}
              opacity={0.35}
              vectorEffect="non-scaling-stroke"
            />
          </g>
        );
      })()}
      {/* series */}
      {series.map((s, i) => {
        const ys2 = syFor(s);
        const proj = s.points.map((p) => ({
          x: sx(p.x),
          y: ys2(p.y),
        }));
        const path =
          s.curve === "monotone"
            ? monotonePath(proj)
            : linePath(proj);
        const color = s.color ?? "#10b981";
        const sw = s.strokeWidth ?? 1.5;
        // Area baseline: bottom of the chart for each axis. For a
        // right-axis series we anchor to that axis's zero/min so the
        // fill doesn't stretch past the visible bounds.
        const baseY =
          s.axis === "right" ? ys2(y20) : ys2(y0);
        return (
          <g key={i}>
            {s.area && proj.length > 1 ? (
              <path
                d={`${path} L ${proj[proj.length - 1].x.toFixed(2)},${baseY.toFixed(2)} L ${proj[0].x.toFixed(2)},${baseY.toFixed(2)} Z`}
                fill={color}
                opacity={0.15}
              />
            ) : null}
            <path
              d={path}
              fill="none"
              stroke={color}
              strokeWidth={sw}
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeDasharray={s.dash}
              vectorEffect="non-scaling-stroke"
            />
          </g>
        );
      })}
      {/* crosshair: vertical guide + per-series dot + value label */}
      {cursorSamples && cursorXClamped != null ? (
        <g pointerEvents="none">
          <line
            x1={sx(cursorXClamped)}
            x2={sx(cursorXClamped)}
            y1={padT}
            y2={padT + innerH}
            stroke="#a3a3a3"
            strokeWidth={1}
            strokeDasharray="3 3"
            vectorEffect="non-scaling-stroke"
          />
          {cursorSamples.map((sample, i) => {
            if (!sample) return null;
            const s = series[i];
            const color = s.color ?? "#10b981";
            const cx = sx(sample.x);
            const cy = syFor(s)(sample.y);
            const fmt =
              s.formatCursor ??
              (s.axis === "right" ? formatY2 ?? formatY : formatY);
            const label = fmt ? fmt(sample.y) : sample.y.toFixed(0);
            // Flip the label to the left of the dot when the
            // cursor is near the right edge so it doesn't clip
            // through the right-axis tick labels (or the chart's
            // SVG bounds when there's no right axis).
            const rightEdge = width - padR;
            const flipLeft = cx > rightEdge - 36;
            const labelX = flipLeft ? cx - 8 : cx + 8;
            const labelAnchor: "start" | "end" = flipLeft ? "end" : "start";
            const labelY = Math.max(padT + 12, cy - 6);
            return (
              <g key={`cursor-${i}`}>
                <circle
                  cx={cx}
                  cy={cy}
                  r={3.5}
                  fill={color}
                  stroke="#0a0a0a"
                  strokeWidth={1.5}
                />
                <g transform={textTF(labelX, labelY)}>
                  <text
                    textAnchor={labelAnchor}
                    className="fill-neutral-100"
                    fontSize={11}
                    fontWeight={600}
                    paintOrder="stroke"
                    stroke="#0a0a0a"
                    strokeWidth={3}
                    strokeLinejoin="round"
                  >
                    {label}
                  </text>
                </g>
              </g>
            );
          })}
        </g>
      ) : null}
      {/* pointer-capture overlay; only present when interactive */}
      {onCursorChange ? (
        <rect
          x={padL}
          y={padT}
          width={innerW}
          height={innerH}
          fill="transparent"
          // pan-y (not none) so a vertical drag scrolls the page while a
          // horizontal drag / tap scrubs the chart — otherwise the chart
          // traps all touch gestures and the page can't scroll on mobile.
          style={{ cursor: "crosshair", touchAction: "pan-y" }}
          onPointerDown={(e) => {
            // Capture the pointer so subsequent moves keep updating
            // even if the finger drifts off the overlay's bounding
            // rect. Required for mobile to register a tap.
            (e.target as Element).setPointerCapture?.(e.pointerId);
            const x = eventToDataX(e.clientX);
            if (x == null) return;
            onCursorChange(snapToSample(x));
          }}
          onPointerMove={(e) => {
            const x = eventToDataX(e.clientX);
            if (x == null) return;
            onCursorChange(snapToSample(x));
          }}
          onPointerLeave={(e) => {
            // Only clear on mouse leave. Touch pointers fire
            // pointerleave immediately at touchend, which would
            // wipe the readout on every tap; leave the last value
            // on screen for touch users so the tap-to-inspect
            // gesture works.
            if (e.pointerType === "mouse") onCursorChange(null);
          }}
        />
      ) : null}
    </svg>
  );
}

function linePath(pts: { x: number; y: number }[]): string {
  if (pts.length === 0) return "";
  let d = `M ${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}`;
  for (let i = 1; i < pts.length; i++) {
    d += ` L ${pts[i].x.toFixed(2)},${pts[i].y.toFixed(2)}`;
  }
  return d;
}

// Fritsch–Carlson monotone cubic interpolation. Produces a smooth path
// that never overshoots between samples, so genuine peaks (max speed,
// hard braking) survive intact.
function monotonePath(pts: { x: number; y: number }[]): string {
  const n = pts.length;
  if (n < 2) return linePath(pts);
  const dx = new Array<number>(n - 1);
  const dy = new Array<number>(n - 1);
  const m = new Array<number>(n - 1); // secant slopes
  for (let i = 0; i < n - 1; i++) {
    dx[i] = pts[i + 1].x - pts[i].x;
    dy[i] = pts[i + 1].y - pts[i].y;
    m[i] = dx[i] === 0 ? 0 : dy[i] / dx[i];
  }
  const t = new Array<number>(n); // tangents
  t[0] = m[0];
  t[n - 1] = m[n - 2];
  for (let i = 1; i < n - 1; i++) {
    if (m[i - 1] * m[i] <= 0) {
      t[i] = 0;
    } else {
      t[i] = (m[i - 1] + m[i]) / 2;
      // Fritsch–Carlson constraint to enforce monotonicity.
      const a = t[i] / m[i - 1];
      const b = t[i] / m[i];
      const h = a * a + b * b;
      if (h > 9) {
        const tau = 3 / Math.sqrt(h);
        t[i] = tau * m[i - 1] * a;
      }
    }
  }
  let d = `M ${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}`;
  for (let i = 0; i < n - 1; i++) {
    const h = dx[i];
    const c1x = pts[i].x + h / 3;
    const c1y = pts[i].y + (t[i] * h) / 3;
    const c2x = pts[i + 1].x - h / 3;
    const c2y = pts[i + 1].y - (t[i + 1] * h) / 3;
    d += ` C ${c1x.toFixed(2)},${c1y.toFixed(2)} ${c2x.toFixed(2)},${c2y.toFixed(2)} ${pts[i + 1].x.toFixed(2)},${pts[i + 1].y.toFixed(2)}`;
  }
  return d;
}

export function BarChart({
  data,
  height = 100,
  color = "#10b981",
  formatY,
  formatX,
  className,
  barGap = 2,
}: {
  data: { label: string; value: number; x?: number }[];
  height?: number;
  color?: string;
  formatY?: (v: number) => string;
  formatX?: (label: string, i: number) => string;
  className?: string;
  barGap?: number;
}) {
  const width = 1000;
  // Hooks must run before any early return (Rules of Hooks).
  const svgRef = useRef<SVGSVGElement | null>(null);
  const roRef = useRef<ResizeObserver | null>(null);
  const [aspect, setAspect] = useState({ fx: 1, fy: 1 });
  // Callback ref so the observer binds when the <svg> actually mounts,
  // even if an earlier render returned EmptyChart (see LineChart note).
  const attachSvg = useCallback(
    (el: SVGSVGElement | null) => {
      svgRef.current = el;
      roRef.current?.disconnect();
      if (!el) return;
      const measure = () => {
        const r = el.getBoundingClientRect();
        if (r.width > 0 && r.height > 0) {
          setAspect({ fx: r.width / width, fy: r.height / height });
        }
      };
      measure();
      const ro = new ResizeObserver(measure);
      ro.observe(el);
      roRef.current = ro;
    },
    [height, width],
  );
  const textTF = (tx: number, ty: number) =>
    `translate(${tx} ${ty}) scale(${1 / aspect.fx} ${1 / aspect.fy})`;

  // Gutters sized in screen px (÷ stretch factor) so counter-scaled
  // labels don't clip on a narrow phone (see LineChart note).
  const padL = Math.min(width * 0.28, 44 / aspect.fx);
  const padR = 8;
  const padT = 8;
  const padB = Math.min(height * 0.4, 22 / aspect.fy);
  const innerW = width - padL - padR;
  const innerH = height - padT - padB;

  if (data.length === 0) return <EmptyChart height={height} />;

  const max = Math.max(1, ...data.map((d) => d.value));
  const yTickValues = tickValues(0, max, 3);
  const barW = Math.max(1, innerW / data.length - barGap);

  return (
    <svg
      ref={attachSvg}
      viewBox={`0 0 ${width} ${height}`}
      className={`w-full ${className ?? ""}`}
      // Pin pixel height so the bars don't collapse on narrow screens
      // (see LineChart note).
      style={{ height }}
      preserveAspectRatio="none"
      role="img"
    >
      {yTickValues.map((yv, i) => (
        <g key={`y${i}`}>
          <line
            x1={padL}
            x2={width - padR}
            y1={padT + innerH - (yv / max) * innerH}
            y2={padT + innerH - (yv / max) * innerH}
            stroke="currentColor"
            className="text-neutral-800"
            strokeWidth={1}
          />
          <g transform={textTF(padL - 6, padT + innerH - (yv / max) * innerH + 3)}>
            <text textAnchor="end" className="fill-neutral-500" fontSize={10}>
              {formatY ? formatY(yv) : yv.toFixed(0)}
            </text>
          </g>
        </g>
      ))}
      {data.map((d, i) => {
        const h = (d.value / max) * innerH;
        const x = padL + i * (innerW / data.length) + barGap / 2;
        const y = padT + innerH - h;
        return (
          <g key={i}>
            <rect
              x={x}
              y={y}
              width={barW}
              height={h}
              fill={color}
              opacity={0.85}
              rx={1.5}
            >
              <title>
                {d.label}: {d.value.toFixed(1)}
              </title>
            </rect>
          </g>
        );
      })}
      {/* sparse x labels: first, last, middle — edges anchored so they
          can't overflow the chart bounds. */}
      {data.length > 0 &&
        [0, Math.floor(data.length / 2), data.length - 1]
          .filter((v, i, a) => a.indexOf(v) === i)
          .map((i) => {
            const anchor: "start" | "middle" | "end" =
              i === 0 ? "start" : i === data.length - 1 ? "end" : "middle";
            return (
              <g
                key={`xl${i}`}
                transform={textTF(padL + i * (innerW / data.length) + barW / 2, height - 6)}
              >
                <text textAnchor={anchor} className="fill-neutral-500" fontSize={10}>
                  {formatX ? formatX(data[i].label, i) : data[i].label}
                </text>
              </g>
            );
          })}
    </svg>
  );
}

function EmptyChart({ height }: { height: number }) {
  const style: CSSProperties = { height };
  return (
    <div
      style={style}
      className="flex items-center justify-center text-xs text-neutral-600"
    >
      no data
    </div>
  );
}

// "Nice" tick values across [a, b].
function tickValues(a: number, b: number, n: number): number[] {
  if (n <= 0) return [];
  if (!Number.isFinite(a) || !Number.isFinite(b) || a === b) {
    return [a];
  }
  const step = (b - a) / (n - 1);
  const out: number[] = [];
  for (let i = 0; i < n; i++) out.push(a + step * i);
  return out;
}
