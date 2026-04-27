// Tiny dependency-free SVG charts. Good enough for sparklines and
// overview dashboards; if we ever need interactivity/zoom we can swap
// individual charts for uplot without touching call sites.

import type { CSSProperties } from "react";

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
}) {
  const width = 1000; // viewBox width, the SVG scales to container
  const padL = 52;
  const padR = 8;
  const padT = 8;
  const padB = 20;
  const innerW = width - padL - padR;
  const innerH = height - padT - padB;

  const all = series.flatMap((s) => s.points);
  if (all.length === 0) {
    return <EmptyChart height={height} />;
  }

  const xs = all.map((p) => p.x);
  const ys = all.map((p) => p.y);
  const x0 = xDomain?.[0] ?? Math.min(...xs);
  const x1 = xDomain?.[1] ?? Math.max(...xs);
  const y0 = yDomain?.[0] ?? Math.min(...ys);
  const y1 = yDomain?.[1] ?? Math.max(...ys);
  const xSpan = Math.max(1e-9, x1 - x0);
  const ySpan = Math.max(1e-9, y1 - y0);

  const sx = (x: number) => padL + ((x - x0) / xSpan) * innerW;
  const sy = (y: number) => padT + innerH - ((y - y0) / ySpan) * innerH;

  const yTickValues = tickValues(y0, y1, yTicks);
  const xTickValues = tickValues(x0, x1, xTicks);

  return (
    <svg
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
      {/* series */}
      {series.map((s, i) => {
        const proj = s.points.map((p) => ({
          x: sx(p.x),
          y: sy(p.y),
        }));
        const path =
          s.curve === "monotone"
            ? monotonePath(proj)
            : linePath(proj);
        const color = s.color ?? "#10b981";
        const sw = s.strokeWidth ?? 1.5;
        return (
          <g key={i}>
            {s.area && proj.length > 1 ? (
              <path
                d={`${path} L ${proj[proj.length - 1].x.toFixed(2)},${sy(y0).toFixed(2)} L ${proj[0].x.toFixed(2)},${sy(y0).toFixed(2)} Z`}
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
