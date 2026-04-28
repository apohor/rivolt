// Tiny dependency-free SVG charts. Good enough for sparklines and
// overview dashboards; if we ever need interactivity/zoom we can swap
// individual charts for uplot without touching call sites.

import { useRef, type CSSProperties } from "react";

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
}) {
  const width = 1000; // viewBox width, the SVG scales to container
  const padL = 52;
  // Make room for right-axis tick labels when a second axis is in
  // use. Without this the labels would clip the chart edge.
  const hasRightAxis = series.some((s) => s.axis === "right");
  const padR = hasRightAxis ? 52 : 8;
  const padT = 8;
  const padB = 20;
  const innerW = width - padL - padR;
  const innerH = height - padT - padB;

  const svgRef = useRef<SVGSVGElement | null>(null);

  const all = series.flatMap((s) => s.points);
  if (all.length === 0) {
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

  const xs = all.map((p) => p.x);
  const x0 = xDomain?.[0] ?? Math.min(...xs);
  const x1 = xDomain?.[1] ?? Math.max(...xs);
  const xSpan = Math.max(1e-9, x1 - x0);

  const leftYs = (leftAll.length > 0 ? leftAll : all).map((p) => p.y);
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
      ref={svgRef}
      viewBox={`0 0 ${width} ${height}`}
      className={`w-full ${className ?? ""}`}
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
          <text
            x={padL - 6}
            y={sy(yv) + 3}
            textAnchor="end"
            className="fill-neutral-500"
            fontSize={10}
          >
            {formatY ? formatY(yv) : yv.toFixed(0)}
          </text>
        </g>
      ))}
      {/* x axis labels */}
      {xTickValues.map((xv, i) => (
        <text
          key={`x${i}`}
          x={sx(xv)}
          y={height - 6}
          textAnchor="middle"
          className="fill-neutral-500"
          fontSize={10}
        >
          {formatX ? formatX(xv) : xv.toFixed(0)}
        </text>
      ))}
      {/* right y axis labels (no extra grid; the left-axis grid
          already spans the chart). Drawing only labels keeps the
          background clean when two unrelated signals overlay. */}
      {hasRightAxis &&
        y2TickValues.map((yv, i) => (
          <text
            key={`y2-${i}`}
            x={width - padR + 6}
            y={syRight(yv) + 3}
            textAnchor="start"
            className="fill-neutral-500"
            fontSize={10}
          >
            {formatY2 ? formatY2(yv) : yv.toFixed(0)}
          </text>
        ))}
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
              s.axis === "right" ? formatY2 ?? formatY : formatY;
            const label = fmt ? fmt(sample.y) : sample.y.toFixed(0);
            const labelX = cx + 8;
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
                <text
                  x={labelX}
                  y={labelY}
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
          style={{ cursor: "crosshair" }}
          onPointerMove={(e) => {
            const x = eventToDataX(e.clientX);
            if (x == null) return;
            onCursorChange(snapToSample(x));
          }}
          onPointerLeave={() => onCursorChange(null)}
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
  const padL = 52;
  const padR = 8;
  const padT = 8;
  const padB = 20;
  const innerW = width - padL - padR;
  const innerH = height - padT - padB;

  if (data.length === 0) return <EmptyChart height={height} />;

  const max = Math.max(1, ...data.map((d) => d.value));
  const yTickValues = tickValues(0, max, 3);
  const barW = Math.max(1, innerW / data.length - barGap);

  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      className={`w-full ${className ?? ""}`}
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
          <text
            x={padL - 6}
            y={padT + innerH - (yv / max) * innerH + 3}
            textAnchor="end"
            className="fill-neutral-500"
            fontSize={10}
          >
            {formatY ? formatY(yv) : yv.toFixed(0)}
          </text>
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
      {/* sparse x labels: first, last, middle */}
      {data.length > 0 &&
        [0, Math.floor(data.length / 2), data.length - 1]
          .filter((v, i, a) => a.indexOf(v) === i)
          .map((i) => (
            <text
              key={`xl${i}`}
              x={padL + i * (innerW / data.length) + barW / 2}
              y={height - 6}
              textAnchor="middle"
              className="fill-neutral-500"
              fontSize={10}
            >
              {formatX ? formatX(data[i].label, i) : data[i].label}
            </text>
          ))}
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
  if (n <= 0 || !Number.isFinite(a) || !Number.isFinite(b) || a === b) {
    return [a];
  }
  const step = (b - a) / (n - 1);
  const out: number[] = [];
  for (let i = 0; i < n; i++) out.push(a + step * i);
  return out;
}
