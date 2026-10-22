import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend, type TruckTrends } from "../lib/api";
import { dotOpacity, dotRadius, dots, specInRange, trendRuns, yDomain, type CapacitySample } from "../lib/capacityChart";
import { usePreferences } from "../lib/preferences";
import { BarChart } from "./charts";
import { specKwh } from "./PackHealthCard";
import { Card } from "./ui";

const monthFmt = new Intl.DateTimeFormat(undefined, { month: "short", year: "2-digit", timeZone: "UTC" });
const monthLabel = (ym: string): string => monthFmt.format(new Date(`${ym}-15T12:00:00Z`));
const dayFmt = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });

const note = "text-sm text-neutral-400";

// "Trends for your truck" (Truck tab): efficiency by month and the estimated usable capacity, for this truck only.
// A chart is drawn only when the server's own rule is met; otherwise the card says how far along it is, so there is
// never an empty chart or a trend from two data points.
export function TrendsCard({ vehicleID }: { vehicleID: string }) {
  const { distanceUnit } = usePreferences();
  const units = distanceUnit === "km" ? "metric" : "imperial";
  const q = useQuery({
    queryKey: ["trends", vehicleID, units],
    queryFn: () => backend.trends(vehicleID, units),
    enabled: !!vehicleID,
    staleTime: 5 * 60_000,
    retry: 1,
  });
  if (q.data === null) return null; // a server without the endpoint
  return (
    <Card title="Trends for your truck" span="full">
      <div data-trends className="space-y-6">
        {q.isPending && <div className="h-40 animate-pulse rounded-sm bg-neutral-900/50" />}
        {q.isError && (
          <p role="status" className={note}>
            Couldn&apos;t load trends.{" "}
            <button type="button" onClick={() => void q.refetch()} className="text-emerald-300 underline underline-offset-2">
              Try again
            </button>
          </p>
        )}
        {q.data && (
          <>
            <EfficiencyTrend t={q.data} />
            <CapacityTrend t={q.data} />
          </>
        )}
      </div>
    </Card>
  );
}

function EfficiencyTrend({ t }: { t: TruckTrends }) {
  const rows = t.efficiency;
  const enough = t.qualifying_months >= t.min_months;
  return (
    <section data-trend="efficiency" aria-labelledby="trend-eff">
      <h3 id="trend-eff" className="text-sm font-medium text-neutral-100">
        Efficiency by month
      </h3>
      {enough ? (
        <>
          <BarChart
            ariaLabel={`Efficiency by month, ${t.efficiency_unit}`}
            data={rows.map((r) => ({ label: monthLabel(r.month), value: r.distance >= t.min_month_distance && r.efficiency ? r.efficiency : 0 }))}
            height={140}
            formatY={(v) => v.toFixed(1)}
            formatValue={(v) => (v > 0 ? `${v.toFixed(2)} ${t.efficiency_unit}` : "not enough driving")}
          />
          <p className="mt-1 text-xs text-neutral-500">
            {t.efficiency_unit}, pack-side. Months under {t.min_month_distance} {t.distance_unit} of driving are left blank. It includes weather and driving style, so cold months usually run lower.
          </p>
        </>
      ) : (
        <p data-trend-state="sparse" className={`mt-1 ${note}`}>
          Not enough driving yet for a trend: {t.qualifying_months} of {t.min_months} months with at least {t.min_month_distance} {t.distance_unit}. This fills in as you drive.
        </p>
      )}
    </section>
  );
}

function CapacityTrend({ t }: { t: TruckTrends }) {
  const c = t.capacity;
  const head = (
    <h3 id="trend-cap" className="text-sm font-medium text-neutral-100">
      Estimated usable capacity (from your charges)
    </h3>
  );
  if (!c) {
    return (
      <section data-trend="capacity" data-trend-state="untracked" aria-labelledby="trend-cap">
        {head}
        <p className={`mt-1 ${note}`}>Battery capacity can&apos;t be tracked on this truck yet.</p>
      </section>
    );
  }
  const need = c.min_soc_delta_pct ?? 30;
  if (c.median_kwh == null || c.samples.length < 2) {
    return (
      <section data-trend="capacity" data-trend-state="sparse" aria-labelledby="trend-cap">
        {head}
        <p className={`mt-1 ${note}`}>
          Needs about {c.min_samples} charges that add at least {need}% of battery, spread over at least {c.min_span_days} days. So far: {c.qualifying_count} of {c.min_samples} charges{c.span_days < c.min_span_days ? `, over ${Math.round(c.span_days)} of ${c.min_span_days} days` : ""}.
        </p>
      </section>
    );
  }
  const spec = specKwh(c.spec_kwh ?? 0);
  return (
    <section data-trend="capacity" data-trend-state="ok" aria-labelledby="trend-cap">
      {head}
      <p className="mt-1 text-2xl font-semibold tabular-nums text-neutral-100">
        About {c.median_kwh.toFixed(0)} kWh <span className="text-sm font-normal text-neutral-400">usable (estimate)</span>
      </p>
      <p data-cap-count className="text-xs text-neutral-500">
        from {c.samples.length} {c.samples.length === 1 ? "charge" : "charges"}
      </p>
      <CapacityDots samples={c.samples as CapacitySample[]} median={c.median_kwh} spec={spec} />
      <p className="mt-1 text-xs text-neutral-500">
        Estimated from charges that add at least {need}% of battery. It moves a little with temperature and is not a Rivian figure. Charging losses and how the truck reports charge level can shift it by a few percent.
        {spec > 0 && ` Rivian's spec for this pack: ${spec.toFixed(0)} kWh.`}
      </p>
    </section>
  );
}

// Each charge's estimate as a dot (bigger and stronger the more the charge added), the rolling median where estimates
// are dense, the median dashed and the rated pack dotted when it falls in range, on the median ±15 %. No line joins
// the dots: an empty month stays empty. A tap or hover shows that charge's date and estimate.
function CapacityDots({ samples, median, spec }: { samples: CapacitySample[]; median: number; spec: number }) {
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
  const PAD = { l: 34, r: 12, t: 8, b: 22 };
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
            <text x={PAD.l - 6} y={Y(v) + 3.5} textAnchor="end" className="fill-neutral-500 text-[10px]">{v}</text>
          </g>
        ))}
        {showSpec && (
          <line data-cap-spec x1={PAD.l} x2={PAD.l + iw} y1={Y(spec)} y2={Y(spec)} stroke="#737373" strokeDasharray="1 3" strokeWidth={1} />
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
          <text key={i} x={X(x)} y={H - 6} textAnchor={i === 0 ? "start" : "end"} className="fill-neutral-500 text-[10px]">{fmt(x)}</text>
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
            Rated pack {spec.toFixed(0)} kWh
          </span>
        )}
      </p>
    </div>
  );
}
