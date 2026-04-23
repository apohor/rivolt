// Thin React wrapper around uPlot for shot time-series.
//
// uPlot is ~45kB minified, fully Canvas 2D, and works on Safari 15.
import { useEffect, useRef } from "react";
import uPlot, { type AlignedData, type Options } from "uplot";
import "uplot/dist/uPlot.min.css";

type Props = {
  /** First series is the X axis (seconds). Remaining arrays are Y series. */
  data: AlignedData;
  series: {
    label: string;
    stroke: string;
    scale?: string;
    /** Unit suffix to render in the cursor read-out (e.g. "bar", "g/s"). */
    unit?: string;
    /** Decimal places for the cursor read-out. Default 1. */
    precision?: number;
    /** Dash pattern in px, e.g. [6,4] for a dashed line. Undefined = solid. */
    dash?: number[];
  }[];
  /** Optional uPlot scale overrides (y-axis, etc). */
  scales?: Options["scales"];
  /** Container height. Width fills the parent. */
  height?: number;
  /** Enable click-and-drag X-axis zoom. Off by default — only sensible
   *  on a finished shot, not while data is still streaming in. */
  zoomable?: boolean;
};

export default function ShotChart({ data, series, scales, height = 280, zoomable = false }: Props) {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const plotRef = useRef<uPlot | null>(null);

  useEffect(() => {
    if (!rootRef.current) return;
    const el = rootRef.current;

    // Build series definitions: first entry is the X-axis placeholder.
    const seriesDefs: Options["series"] = [
      // X-axis read-out: the axis label already says "shot time (s)",
      // so the tick/cursor values are unit-less to keep the graph clean.
      { label: "Time", value: (_u, v) => (v == null ? "—" : v.toFixed(1)) },
      ...series.map((s) => {
        const prec = s.precision ?? 1;
        const unit = s.unit ? ` ${s.unit}` : "";
        return {
          label: s.label,
          stroke: s.stroke,
          width: 1.5,
          scale: s.scale,
          dash: s.dash,
          // Suppress per-sample dot markers along the line. uPlot otherwise
          // draws a dot at every datum when density is low, which looks
          // noisy. We still want a prominent dot at the cursor position —
          // that's configured via the top-level cursor.points below.
          points: { show: false },
          // Cursor read-out: e.g. "8.5 bar".
          value: (_u: uPlot, v: number | null) =>
            v == null ? "—" : `${v.toFixed(prec)}${unit}`,
        };
      }),
    ];

    const opts: Options = {
      width: el.clientWidth || 600,
      height,
      series: seriesDefs,
      scales: {
        x: { time: false },
        ...(scales ?? {}),
      },
      axes: [
        {
          // X axis: seconds since shot start (profile_time/1000).
          label: "shot time (s)",
          labelSize: 22,
          labelFont: "11px -apple-system, BlinkMacSystemFont, system-ui, sans-serif",
          stroke: "#737373",
          grid: { stroke: "#262626" },
          ticks: { stroke: "#262626" },
          values: (_u, splits) => splits.map((v) => v.toFixed(0)),
        },
        { stroke: "#737373", grid: { stroke: "#262626" }, ticks: { stroke: "#262626" } },
      ],
      // Live legend shows the value at the cursor position. uPlot only
      // updates this on cursor move (not on each setData), so it's cheap
      // even on the streaming chart.
      legend: { show: true, live: true },
      // pxAlign:false gives sub-pixel-positioned strokes — smoother lines
      // when data updates frequently.
      pxAlign: false,
      // Cursor:
      //   - drag-to-zoom only when `zoomable` (static shot detail view)
      //   - a coloured ring-dot on every series at the hover X. This
      //     already pinpoints the X position on each line, so the
      //     full-height crosshair (cursor.x/y) would be redundant noise
      //     and is left at its default (off via CSS: see u-cursor-x/y
      //     having no visible border).
      //   - NB: uPlot's `points.show` must return an HTMLElement, not a
      //     boolean — so we let the built-in factory create the div and
      //     only customize size/stroke/fill.
      cursor: {
        drag: { x: zoomable, y: false, uni: 30 },
        points: {
          size: 8,
          stroke: (u: uPlot, sIdx: number) =>
            (u.series[sIdx].stroke as string) || "#fafafa",
          fill: () => "#0a0a0a",
          width: 2,
        },
      },
    };

    const plot = new uPlot(opts, data, el);
    plotRef.current = plot;

    const onResize = () => {
      if (!plotRef.current || !el) return;
      plotRef.current.setSize({ width: el.clientWidth, height });
    };
    window.addEventListener("resize", onResize);

    return () => {
      window.removeEventListener("resize", onResize);
      plot.destroy();
      plotRef.current = null;
    };
    // Rebuild the plot when shape changes. `data` reference change handled below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [series.length, height]);

  // Hot-swap data without tearing the chart down.
  useEffect(() => {
    if (plotRef.current) plotRef.current.setData(data);
  }, [data]);

  return <div ref={rootRef} className="caffeine-uplot w-full" style={{ minHeight: height }} />;
}
