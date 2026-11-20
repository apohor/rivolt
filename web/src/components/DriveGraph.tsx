import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend, DETAIL_SAMPLE_LIMIT, type Sample } from "../lib/api";
import { clock } from "../lib/nowState";
import { smoothGaussianTime } from "../lib/smooth";
import { destinationShort, type LiveDestination } from "../lib/liveDestination";
import { forVehicle } from "../lib/vehicleSamples";
import { GraphHead, HaloDot, Legend, QuietLine, SoftDefs, SoftFill, SoftLine, softPath, thin, useBox, useSoftId, useSoftInk, type Pt } from "./NowGraphKit";

// The Now card's graph while driving (design_preview "now_drive_graph"): the speed from the start of the drive to now as one soft line, the
// battery as a quieter second line. The series are the stored samples (the endpoint the Live page reads, no Rivian call), read at the card's own
// pace. Until there are enough samples there is no frame at all and the card is as it was.
const MIN_SPAN_MS = 90_000;
const MIN_POINTS = 4;
const PAD = { t: 30, b: 26, l: 6, r: 10 };
const MPH_TO_KMH = 1.609344;

export type DriveSeries = { speed: Pt[]; soc: Pt[]; startPct: number | null; startedMs: number };

// At most SERIES_POINTS readings, evenly spaced in time (read off the smoothed line, the last one exactly), so the curve through them has no
// bunches of close readings to kink on; a short series is kept as it is.
const SERIES_POINTS = 36;
function evenly(pts: Pt[]): Pt[] {
  if (pts.length <= SERIES_POINTS) return pts;
  const x0 = pts[0].x;
  const x1 = pts[pts.length - 1].x;
  const out: Pt[] = [];
  let j = 0;
  for (let i = 0; i < SERIES_POINTS; i++) {
    const x = i === SERIES_POINTS - 1 ? x1 : x0 + ((x1 - x0) * i) / (SERIES_POINTS - 1);
    while (j < pts.length - 2 && pts[j + 1].x < x) j++;
    const a = pts[j];
    const b = pts[j + 1];
    out.push({ x, y: b.x === a.x ? b.y : a.y + ((b.y - a.y) * Math.min(Math.max((x - a.x) / (b.x - a.x), 0), 1)) });
  }
  return out;
}

// The drive's series from its samples; null while there is not enough of a drive to draw.
export function driveSeries(rows: Sample[], startedMs: number): DriveSeries | null {
  const at = (p: Sample) => new Date(p.At).getTime();
  const mine = rows.filter((p) => at(p) >= startedMs - 5_000);
  const raw = mine.map((p) => ({ x: at(p), y: Math.max(p.SpeedMph || 0, 0) }));
  // The window grows with the drive (a thirtieth of it, 12 s to a minute), so a long drive is drawn as its shape and not as every reading's jitter.
  const span = raw.length > 1 ? raw[raw.length - 1].x - raw[0].x : 0;
  const speed = evenly(smoothGaussianTime(raw, Math.min(Math.max(span / 30, 12_000), 60_000)));
  const soc = smoothGaussianTime(mine.filter((p) => p.BatteryLevelPct > 0).map((p) => ({ x: at(p), y: p.BatteryLevelPct })), 30_000);
  const first = speed[0];
  const last = speed[speed.length - 1];
  if (!first || !last || speed.length < MIN_POINTS || last.x - first.x < MIN_SPAN_MS) return null;
  return { speed, soc, startPct: mine.find((p) => p.BatteryLevelPct > 0)?.BatteryLevelPct ?? null, startedMs };
}

// The drive so far, read at the card's pace; null while the drive is not known or too short to draw.
export function useDriveSeries(vehicleId: string, enabled: boolean, startedMs: number, everyMs: number): DriveSeries | null {
  const on = enabled && Number.isFinite(startedMs);
  const q = useQuery<Sample[]>({
    queryKey: ["samples", "now-drive", vehicleId, startedMs],
    enabled: on,
    queryFn: () => backend.samples(new Date(startedMs - 30_000), DETAIL_SAMPLE_LIMIT, new Date(Date.now() + 60_000), vehicleId).then((r) => forVehicle(r, vehicleId)),
    refetchInterval: everyMs,
    refetchIntervalInBackground: false,
    staleTime: 1_000,
  });
  return useMemo(() => (on && q.data ? driveSeries(q.data, startedMs) : null), [on, q.data, startedMs]);
}

// Where the truck's own navigation is taking it (the endpoint's own answer: a 404 is off and nothing is shown, a 204 no trip). Read while the
// drive is on screen and nothing is kept after.
export function useDriveDestination(vehicleId: string, enabled: boolean): LiveDestination | null {
  const q = useQuery<LiveDestination | null | "off">({
    queryKey: ["live-destination", vehicleId],
    enabled,
    queryFn: () => backend.liveDestination(vehicleId),
    refetchInterval: (query) => (query.state.data === "off" ? false : 10_000),
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    retry: false,
    gcTime: 0,
  });
  return enabled && q.data && q.data !== "off" ? q.data : null;
}

export function DriveGraph({ series, unit, nowPct, destination = null }: { series: DriveSeries; unit: "mi" | "km"; nowPct: number | null; destination?: LiveDestination | null }) {
  const [ref, { w, h }] = useBox();
  const ink = useSoftInk();
  const id = useSoftId();
  const k = unit === "km" ? MPH_TO_KMH : 1;
  const speedUnit = unit === "km" ? "km/h" : "mph";
  const { speed, soc, startPct } = series;
  const first = speed[0];
  const last = speed[speed.length - 1];
  const peak = Math.max(...speed.map((p) => p.y * k), 10);
  const top = Math.ceil((peak * 1.15) / 10) * 10;
  const base = h - PAD.b;
  const sx = (x: number) => PAD.l + ((x - first.x) / (last.x - first.x)) * (w - PAD.l - PAD.r);
  const sv = (v: number) => base - (v / top) * (base - PAD.t);
  const socHi = Math.max(...soc.map((p) => p.y), 1);
  const socLo = Math.min(...soc.map((p) => p.y), socHi);
  const socSpan = Math.max(socHi - socLo, 3);
  const ss = (v: number) => PAD.t + (1 - (v - (socLo - socSpan * 0.1)) / (socSpan * 1.2)) * (base - PAD.t);
  const speedPts: Pt[] = speed.map((p) => ({ x: sx(p.x), y: sv(p.y * k) }));
  const socPts: Pt[] = thin(soc).map((p) => ({ x: sx(p.x), y: ss(p.y) }));
  const line = softPath(speedPts);
  const lx = sx(last.x);
  const ly = sv(last.y * k);
  const nowSpeed = Math.round(last.y * k);
  // The change is the card's own battery figure (the ring) against the first stored reading, as the charging graph does.
  const used = nowPct != null && nowPct > 0 && startPct != null ? Math.round(startPct) - Math.round(nowPct) : null;

  return (
    <div data-testid="now-drive-graph" className="flex min-w-0 flex-col min-[900px]:h-full">
      <GraphHead title="This drive" change={used != null && used > 0 ? `−${used}%` : null} since={`since ${clock(first.x)}`} testid="now-drive-change" />
      {destination && (
        <p data-testid="now-drive-dest" className="mb-1 truncate text-sm text-neutral-300">
          {destinationShort(destination, unit)}
        </p>
      )}
      <div ref={ref} className="h-[12rem] min-w-0 min-[900px]:h-auto min-[900px]:min-h-[14rem] min-[900px]:flex-1">
        {w > 0 && h > 0 && (
          <svg
            width={w}
            height={h}
            viewBox={`0 0 ${w} ${h}`}
            role="img"
            aria-label={`Speed since ${clock(first.x)}, ${nowSpeed} ${speedUnit} now, top ${Math.round(peak)} ${speedUnit}${used != null && used > 0 ? `, battery down ${used} percent` : ""}`}
            className="block overflow-visible"
          >
            <SoftDefs id={id} ink={ink} />
            <SoftFill id={id} d={line} baseY={base} x0={speedPts[0].x} x1={lx} />
            {socPts.length > 1 && <QuietLine d={softPath(socPts)} color={ink.quiet} />}
            <SoftLine id={id} d={line} color={ink.line} />
            <HaloDot id={id} x={lx} y={ly} color={ink.line} big />
            <text x={PAD.l} y={h - 6} className="fill-neutral-500 text-xs">
              {w >= 260 ? `Left ${clock(series.startedMs)}` : clock(series.startedMs)}
            </text>
            {lx - PAD.l > 170 && (
              <text x={lx} y={h - 6} textAnchor="middle" className="fill-neutral-500 text-xs">
                now
              </text>
            )}
          </svg>
        )}
      </div>
      <Legend
        items={[
          { swatch: "line", color: ink.line, label: `Speed (${speedUnit})` },
          ...(socPts.length > 1 ? [{ swatch: "line" as const, color: ink.quiet, label: "Battery" }] : []),
        ]}
      />
    </div>
  );
}
