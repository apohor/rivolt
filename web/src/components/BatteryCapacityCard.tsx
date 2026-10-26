import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { capacityView, estimateLine } from "../lib/batteryCapacity";
import { dotOpacity, dotRadius, dots, specInRange, trendRuns, yDomain, type CapacitySample } from "../lib/capacityChart";
import { usePreferences } from "../lib/preferences";
import { Card } from "./ui";

const dayFmt = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
const note = "text-sm text-neutral-400";

// "Battery capacity" (Truck tab): one card, one headline. What the truck reports, else our estimate from charges;
// the other figure on one labelled line under it, Rivian's spec when it is a usable figure, and our estimate's
// history as dots. Plain figures: no score, no comparison with the spec, no "degradation".
export function BatteryCapacityCard({ vehicleID }: { vehicleID: string }) {
  const { distanceUnit } = usePreferences();
  const units = distanceUnit === "km" ? "metric" : "imperial";
  const pack = useQuery({
    queryKey: ["packHealth", vehicleID],
    queryFn: () => backend.packHealth(vehicleID),
    enabled: !!vehicleID,
    staleTime: 5 * 60_000,
  });
  // The same query as the Trends card: one request for both.
  const trends = useQuery({
    queryKey: ["trends", vehicleID, units],
    queryFn: () => backend.trends(vehicleID, units),
    enabled: !!vehicleID,
    staleTime: 5 * 60_000,
    retry: 1,
  });
  const loading = pack.isPending || trends.isPending;
  const v = capacityView(pack.data, trends.data);
  const other = estimateLine(v);
  return (
    <Card title="Battery capacity" id="truck-pack">
      <div data-battery-capacity className="space-y-1">
        {loading && <div className="h-24 animate-pulse rounded-sm bg-neutral-900/50" />}
        {!loading && pack.isError && trends.isError && (
          <p className="text-xs text-neutral-500">Couldn&apos;t load battery capacity.</p>
        )}
        {!loading && v.head && (
          <p data-cap-head className="flex flex-wrap items-baseline gap-x-2 text-2xl font-semibold tabular-nums text-neutral-100">
            {v.head.kind === "reported" ? `${v.head.kwh.toFixed(1)} kWh` : `About ${v.head.kwh.toFixed(0)} kWh`}
            <span className="text-sm font-normal text-neutral-400">
              {v.head.kind === "reported" ? "usable, as your truck reports it" : "usable (estimate)"}
            </span>
          </p>
        )}
        {!loading && other && (
          <p data-cap-estimate className="text-sm text-neutral-300">
            {other}
          </p>
        )}
        {!loading && v.head?.kind === "estimate" && v.estimate && (
          <p data-cap-count className="text-xs text-neutral-500">
            from {v.estimate.charges} {v.estimate.charges === 1 ? "charge" : "charges"}
          </p>
        )}
        {!loading && v.spec > 0 && (
          <p data-cap-spec-line className="text-xs text-neutral-500 tabular-nums">
            Rivian&apos;s spec for this pack: {v.spec.toFixed(0)} kWh
          </p>
        )}
        {!loading && v.estimate && trends.data?.capacity && (
          <>
            <CapacityDots samples={trends.data.capacity.samples as CapacitySample[]} median={v.estimate.kwh} spec={v.spec} />
            <p className="mt-1 text-xs text-neutral-500">
              Our estimate comes from charges that add at least {v.needPct}% of battery. It moves a little with temperature and is not a Rivian figure. Charging losses and how the truck reports charge level can shift it by a few percent.
            </p>
          </>
        )}
        {!loading && v.progress && <p data-cap-progress className={`mt-1 ${note}`}>{v.progress}</p>}
        {!loading && v.untracked && <p data-cap-untracked className={`mt-1 ${note}`}>Our estimate can&apos;t be tracked on this truck yet.</p>}
      </div>
    </Card>
  );
}

// Each charge's estimate as a dot (bigger and stronger the more the charge added), the rolling median where estimates
// are dense, the median dashed and the rated pack dotted when it falls in range, on the median ±15 %. No line joins
// the dots: an empty month stays empty. A tap or hover shows that charge's date and estimate.
export function CapacityDots({ samples, median, spec }: { samples: CapacitySample[]; median: number; spec: number }) {
  const ref = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(600);
  const [pick, setPick] = useState<number | null>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const set = () => setW(Math.max(260, Math.round(el.clientWidth)));
    set();
    const ro = new ResizeObserver(set);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const H = 150;
  // Room on the right for the "Rivian spec" label when the spec line shows, so it never sits on the dots.
  const PAD = { l: 34, r: specInRange(spec, median) ? 66 : 12, t: 8, b: 22 };
  const ds = dots(samples, median);
  if (ds.length === 0) return null;
  const [lo, hi] = yDomain(median);
  const x0 = ds[0].x;
  const x1 = Math.max(ds[ds.length - 1].x, x0 + 86_400_000);
  const iw = w - PAD.l - PAD.r;
  const ih = H - PAD.t - PAD.b;
  const X = (x: number) => PAD.l + ((x - x0) / (x1 - x0)) * iw;
  const Y = (y: number) => PAD.t + (1 - (y - lo) / (hi - lo)) * ih;
  const runs = trendRuns(samples);
  const showSpec = specInRange(spec, median);
  const picked = pick != null ? ds[pick] : null;
  const choose = (e: React.PointerEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const px = ((e.clientX - r.left) / r.width) * w;
    const py = ((e.clientY - r.top) / r.height) * H;
    let best = -1;
    let bestD = 24 * 24;
    ds.forEach((d, i) => {
      const dd = (X(d.x) - px) ** 2 + (Y(d.y) - py) ** 2;
      if (dd < bestD) {
        bestD = dd;
        best = i;
      }
    });
    setPick(best >= 0 ? best : null);
  };
  const ticks = [lo, Math.round(median), hi];
  const fmt = (x: number) => dayFmt.format(new Date(x));
  return (
    <div ref={ref} className="relative mt-2" data-cap-chart>
      <svg
        viewBox={`0 0 ${w} ${H}`}
        width={w}
        height={H}
        role="img"
        aria-label={`Estimated usable capacity over time, kWh: ${ds.length} charge estimates around ${median.toFixed(0)} kWh`}
        onPointerDown={choose}
        onPointerMove={(e) => e.pointerType === "mouse" && choose(e)}
        onPointerLeave={() => setPick(null)}
        className="touch-pan-y"
      >
        {ticks.map((v) => (
          <g key={v}>
            <line x1={PAD.l} x2={PAD.l + iw} y1={Y(v)} y2={Y(v)} className="text-neutral-800" stroke="currentColor" strokeOpacity={0.5} />
            <text x={PAD.l - 6} y={Y(v) + 3.5} textAnchor="end" className="fill-neutral-500 text-[11px]">{v}</text>
          </g>
        ))}
        {showSpec && (
          <>
            <line data-cap-spec x1={PAD.l} x2={PAD.l + iw} y1={Y(spec)} y2={Y(spec)} stroke="#737373" strokeDasharray="1 3" strokeWidth={1} />
            {/* Labelled past its end (in the right margin), so the dots read as our estimate, not as a gap to it. */}
            <text data-cap-spec-label x={PAD.l + iw + 4} y={Y(spec) + 3.5} textAnchor="start" className="fill-neutral-500 text-[11px]">
              Rivian spec
            </text>
          </>
        )}
        <line data-cap-median x1={PAD.l} x2={PAD.l + iw} y1={Y(median)} y2={Y(median)} stroke="#a3a3a3" strokeDasharray="4 3" strokeWidth={1} />
        {runs.map((run, i) => (
          <polyline
            key={i}
            data-cap-trend
            points={run.map((p) => `${X(p.x).toFixed(1)},${Y(Math.min(hi, Math.max(lo, p.y))).toFixed(1)}`).join(" ")}
            fill="none"
            stroke="currentColor"
            className="text-neutral-300"
            strokeOpacity={0.6}
            strokeWidth={2.5}
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        ))}
        {ds.map((d, i) => (
          <circle
            key={i}
            data-cap-dot
            cx={X(d.x)}
            cy={Y(d.y)}
            r={dotRadius(d.strength)}
            fill={d.outside ? "none" : "#34d399"}
            fillOpacity={dotOpacity(d.strength)}
            stroke="#34d399"
            strokeOpacity={d.outside ? 0.9 : 0}
            strokeWidth={1.5}
          />
        ))}
        {picked && <circle cx={X(picked.x)} cy={Y(picked.y)} r={dotRadius(picked.strength) + 3} fill="none" stroke="currentColor" className="text-neutral-100" strokeWidth={1.5} />}
        {[x0, x1].map((x, i) => (
          <text key={i} x={X(x)} y={H - 6} textAnchor={i === 0 ? "start" : "end"} className="fill-neutral-500 text-[11px]">{fmt(x)}</text>
        ))}
      </svg>
      {picked && (
        <div
          role="status"
          data-cap-tip
          className="pointer-events-none absolute top-0 z-10 whitespace-nowrap rounded-md border border-neutral-700 bg-neutral-900/95 px-2 py-1 text-xs text-neutral-200 shadow-lg"
          style={{ left: Math.min(Math.max(X(picked.x) - 60, 0), w - 200) }}
        >
          {fmt(picked.x)} · about {picked.y.toFixed(0)} kWh (estimate)
          {picked.energyKwh != null && ` · ${picked.energyKwh.toFixed(0)} kWh added`}
        </div>
      )}
      <p data-cap-legend className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-neutral-500">
        <span>
          <span aria-hidden className="mr-1 inline-block h-2 w-2 rounded-full bg-emerald-400 align-middle" />
          One charge&apos;s estimate (bigger: more added)
        </span>
        {runs.length > 0 && (
          <span>
            <span aria-hidden className="mr-1 inline-block h-0 w-4 border-t-[2.5px] border-neutral-300/60 align-middle" />
            Typical (rolling median)
          </span>
        )}
        <span>
          <span aria-hidden className="mr-1 inline-block h-0 w-4 border-t border-dashed border-neutral-400 align-middle" />
          Median {median.toFixed(0)} kWh
        </span>
        {showSpec && (
          <span>
            <span aria-hidden className="mr-1 inline-block h-0 w-4 border-t border-dotted border-neutral-500 align-middle" />
            Rivian spec {spec.toFixed(0)} kWh
          </span>
        )}
      </p>
    </div>
  );
}

