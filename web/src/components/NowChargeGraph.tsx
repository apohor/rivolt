import { createContext, useContext, type ReactNode } from "react";
import { chargeFinishMs, clock } from "../lib/nowState";
import { formatTemperature, type TemperatureUnit } from "../lib/preferences";
import { powerMax, useOpenChargeSeries } from "./LiveChargeGraph";
import { GraphHead, HaloDot, Legend, Pill, QuietLine, SoftDefs, SoftFill, SoftLine, labelWidth, softPath, thin, useBox, useSoftId, useSoftInk, type Pt } from "./NowGraphKit";

// The Now card's charging graph (design_preview "now_charge_graph"), in the soft look of the parked graph: the open charge's battery from plug-in
// to now as one soft line, the charger's power as a quieter second line, and, when the card has a finish time, the way on to the target as a
// soft dotted continuation with the finish time in a small pill. The series are the Live page's (stored samples, no Rivian call), read at
// its 30 s pace. Under two minutes of samples it says so instead of drawing a stub.
const MIN_SPAN_MS = 120_000;
// The session so far takes its true share of the whole charge (so far plus what is left), but never less than this share of the width, so a
// charge that has just started still shows its start readably.
const MIN_HISTORY_SHARE = 0.25;
const PAD = { t: 30, b: 26, l: 6, r: 10 };

// The open charge's series, read once for the card: the graph draws it and the temperatures row takes the pack's reading from it.
const SeriesContext = createContext<ReturnType<typeof useOpenChargeSeries> | null>(null);

export function ChargeSeriesProvider({ vehicleId, enabled, children }: { vehicleId: string; enabled: boolean; children: ReactNode }) {
  return <SeriesContext.Provider value={useOpenChargeSeries(vehicleId, enabled)}>{children}</SeriesContext.Provider>;
}

// Whether the charge's series is still on its way (the card keeps its height until it has answered).
export function useChargePending(): boolean {
  return useContext(SeriesContext)?.pending ?? false;
}

// The pack's line needs two stored readings and the newest not older than this (a stale reading draws no line and no legend entry).
const PACK_FRESH_MS = 15 * 60_000;
// Its own y scale (the range is a few degrees, the battery's tens of points), never tighter than this many degrees Celsius so a flat pack stays flat.
const PACK_MIN_SPAN_C = 3;

export function NowChargeGraph({ nowMs, nowPct, targetPct, etaHours, powerKw = null, tempUnit = "c" }: { nowMs: number; nowPct: number; targetPct: number | null; etaHours: number | null; powerKw?: number | null; tempUnit?: TemperatureUnit }) {
  const series = useContext(SeriesContext);
  const { open, soc, power, startPct, packLine } = series ?? { open: undefined, soc: [], power: [], startPct: null, packLine: [] };
  const [ref, { w, h }] = useBox();
  const ink = useSoftInk();
  const id = useSoftId();
  if (!open) return null;
  const first = soc[0];
  const last = soc[soc.length - 1];
  if (!first || !last || soc.length < 3 || last.x - first.x < MIN_SPAN_MS) {
    return (
      <p data-testid="now-charge-graph-collecting" className="text-xs text-neutral-400">
        Collecting this charge's first minutes. The graph appears shortly.
      </p>
    );
  }
  const target = targetPct != null && targetPct > nowPct ? targetPct : null;
  // The pill's clock is the chip's and the slot's (chargeFinishMs: the shown whole minutes from now, never from the last stored sample).
  const endX = target != null && etaHours != null && etaHours > 0 ? chargeFinishMs(etaHours, nowMs) : null;
  const peak = power.reduce((m, p) => Math.max(m, p.y), 0);
  // The power named is the card's live figure (the slot's big number), so one kW is said once; the line itself is the stored samples.
  const lastPower = power[power.length - 1];
  const nowKw = powerKw ?? lastPower?.y ?? null;
  // The pack's line: only the measured part (from the first battery reading), and only with two readings of which the newest is recent. No flat fake line.
  const packPts = packLine.filter((p) => p.x >= first.x);
  const packLast = packPts[packPts.length - 1];
  const packOn = packPts.length >= 2 && nowMs - packLast.x <= PACK_FRESH_MS;
  const packNow = packOn ? formatTemperature(packLast.y, tempUnit) : null;
  const packDelta = packOn ? Math.round((packLast.y - packPts[0].y) * (tempUnit === "f" ? 1.8 : 1)) : 0;
  // The two numbers the graph shows are the first stored reading and the card's own battery figure (the ring), so the change is exactly their difference.
  const startShown = Math.round(startPct ?? first.y);
  const nowShown = Math.round(nowPct);
  const gained = nowShown - startShown;

  // The time axis is to scale (the session so far against the time left) down to the floor; past it the dotted way on is shortened and the
  // pill carries the finish time.
  const elapsed = last.x - first.x;
  const share = endX != null ? Math.max(elapsed / (elapsed + Math.max(endX - last.x, 1)), MIN_HISTORY_SHARE) : 1;
  const x1 = endX != null ? last.x + (elapsed * (1 - share)) / share : last.x;
  const hi = Math.max(last.y, target ?? 0, ...soc.map((p) => p.y));
  const lo = Math.min(...soc.map((p) => p.y));
  // The y range is the data's own: a little under the start and a little over the target, so the lines use the height.
  const span = Math.max(hi - lo, 4);
  const yLo = lo - span * 0.1;
  const yHi = hi + span * 0.06;
  const base = h - PAD.b;
  const sx = (x: number) => PAD.l + ((x - first.x) / (x1 - first.x)) * (w - PAD.l - PAD.r);
  const sy = (v: number) => PAD.t + (1 - (v - yLo) / (yHi - yLo)) * (base - PAD.t);
  const sp = (v: number) => base - (v / powerMax(peak)) * (base - PAD.t);
  const socPts: Pt[] = thin(soc).map((p) => ({ x: sx(p.x), y: sy(p.y) }));
  const powPts: Pt[] = thin(power).map((p) => ({ x: sx(p.x), y: sp(p.y) }));
  const line = softPath(socPts);
  // Its own y scale: the pack's range with room above and below (never tighter than a few degrees), so a 5 degree rise is plainly visible.
  let sk: (v: number) => number = () => base;
  let packPath = "";
  if (packOn) {
    const pMin = Math.min(...packPts.map((p) => p.y));
    const pMax = Math.max(...packPts.map((p) => p.y));
    const pSpan = Math.max(pMax - pMin, PACK_MIN_SPAN_C);
    const pMid = (pMin + pMax) / 2;
    const pLo = pMid - pSpan * 0.65;
    const pHi = pMid + pSpan * 0.65;
    sk = (v) => PAD.t + 6 + (1 - (v - pLo) / (pHi - pLo)) * (base - PAD.t - 12);
    packPath = softPath(thin(packPts).map((p) => ({ x: sx(p.x), y: sk(p.y) })));
  }
  const lx = sx(last.x);
  const ly = sy(last.y);
  const ex = endX != null ? sx(x1) : null;
  const ey = target != null ? sy(target) : null;
  // The estimate leaves "now" as a curve: it starts with the slope the battery line had over its last stretch (the last ~24 px or so; never steeper than the way to the
  // target) and eases in level at the target, so there is no angle at the "now" dot. It still ends at the real finish time.
  const estimate = (() => {
    if (ex == null || ey == null) return "";
    const dx = ex - lx;
    const dy = ey - ly;
    const from = socPts.find((p) => p.x >= lx - Math.max(24, 0.08 * (lx - socPts[0].x))) ?? socPts[0];
    const m = lx - from.x > 1 ? (ly - from.y) / (lx - from.x) : 0;
    const rise = Math.min(0, Math.max(dy, m * dx * 0.4));
    const f = (v: number) => v.toFixed(1);
    return `M${f(lx)} ${f(ly)} C${f(lx + dx * 0.4)} ${f(ly + rise)} ${f(ex - dx * 0.35)} ${f(ey)} ${f(ex)} ${f(ey)}`;
  })();
  const pill = target != null ? `${Math.round(target)}% · ${clock(endX ?? last.x)}` : null;
  const startText = w >= 260 ? `Plugged in ${clock(first.x)}` : clock(first.x);
  const labelNow = lx - PAD.l > 170;
  const labelStartValue = lx - PAD.l > 90;
  const valueLeft = lx - PAD.l > 60;
  // The finish pill sits at the top right; when "now" is close to it (a charge about to finish) the current value goes under the dot, beside the estimate, so the two never overlap.
  const pillY = Math.max((ey ?? 0) - 18, 12);
  const pillLeft = w - PAD.r - labelWidth(pill ?? "");
  const nowW = Math.ceil(`${nowShown}%`.length * 8.4);
  const nowBelow = pill != null && ey != null && Math.abs(ly - 12 - pillY) < 20 && (valueLeft ? lx - 10 : lx + 40) > pillLeft - 6;
  // The now value under the dot is kept inside the card's right edge.
  const nowX = nowBelow ? Math.min(lx + 8, w - PAD.r - nowW) : valueLeft ? lx - 10 : lx + 10;
  const nowY = nowBelow ? ly + 24 : Math.max(ly - 12, 12);
  const nowAnchor = valueLeft && !nowBelow ? "end" : "start";
  // The pack's value at the end of its line goes where it touches nothing else: the boxes of the other labels are collected and the first free
  // spot (above the end to its left, below it, then to its right) is taken. With no free spot the legend alone carries the value.
  type Box = { x0: number; y0: number; x1: number; y1: number };
  const boxOf = (x: number, y: number, tw: number, anchor: "start" | "end", fs: number): Box => ({ x0: anchor === "end" ? x - tw : x, x1: anchor === "end" ? x : x + tw, y0: y - fs, y1: y + 4 });
  const taken: Box[] = [
    boxOf(nowX, nowY, nowW, nowAnchor, 14),
    { x0: PAD.l, x1: PAD.l + 60, y0: h - 20, y1: h },
    { x0: lx - 10, x1: lx + 10, y0: ly - 10, y1: ly + 10 },
  ];
  // The power line runs level across the top; a label on it is hard to read.
  if (powPts.length > 1) taken.push({ x0: PAD.l, x1: lx, y0: Math.min(...powPts.map((p) => p.y)) - 1, y1: Math.max(...powPts.map((p) => p.y)) + 1 });
  if (labelStartValue) taken.push(boxOf(socPts[0].x + 2, Math.max(socPts[0].y - 14, 12), `${startShown}%`.length * 7, "start", 12));
  if (pill) taken.push({ x0: pillLeft, x1: w - PAD.r, y0: pillY - 10, y1: pillY + 10 });
  if (ex != null && ey != null) taken.push({ x0: ex - 12, x1: ex + 12, y0: ey - 12, y1: ey + 12 });
  const packX = packOn ? sx(packLast.x) : 0;
  const packY = packOn ? sk(packLast.y) : 0;
  const packW = packNow ? Math.ceil(packNow.length * 8.4) : 0;
  // First choice: above its own line, left of the dot, ending where the line has risen to that spot's height, so it reads as the pack's and never as the battery's value beside it.
  const packYAt = (x: number) => {
    const pts = thin(packPts).map((p) => ({ x: sx(p.x), y: sk(p.y) }));
    for (let i = 1; i < pts.length; i++) if (pts[i].x >= x) return pts[i - 1].y + ((pts[i].y - pts[i - 1].y) * (x - pts[i - 1].x)) / Math.max(1, pts[i].x - pts[i - 1].x);
    return packY;
  };
  const packSpots = packOn
    ? ([
        ...[28, 48, 72, 100].map((dx) => [packX - dx, packYAt(packX - dx) - 9, "end"] as const),
        [packX - 10, packY - 10, "end"],
        [packX - 10, packY + 18, "end"],
        [packX + 10, packY + 4, "start"],
        [packX + 10, packY - 10, "start"],
        [packX + 10, packY + 18, "start"],
        [packX - 56, packY - 10, "end"],
        [packX - 56, packY + 18, "end"],
        [packX - 110, packY - 10, "end"],
        [packX - 110, packY + 18, "end"],
        [packX - 10, (powPts.length > 1 ? Math.min(...powPts.map((p) => p.y)) : packY) - 8, "end"],
        [packX - 70, (powPts.length > 1 ? Math.min(...powPts.map((p) => p.y)) : packY) - 8, "end"],
      ] as const)
    : [];
  // A spot clear of the other labels and of the lines is needed; with none, the legend alone carries the value.
  const onLine = (b: Box, pts: Pt[]) =>
    pts.some((p, i) => {
      const q = pts[i + 1] ?? p;
      for (let t = 0; t <= 1; t += 0.1) {
        const x = p.x + (q.x - p.x) * t;
        const y = p.y + (q.y - p.y) * t;
        if (x > b.x0 - 3 && x < b.x1 + 3 && y > b.y0 - 2 && y < b.y1 + 2) return true;
      }
      return false;
    });
  const packFree = (x: number, y: number, a: "start" | "end") => {
    const b = boxOf(x, y, packW, a, 14);
    const nb = boxOf(nowX, nowY, nowW, nowAnchor, 14);
    const apart = b.x0 > nb.x1 + 24 || b.x1 < nb.x0 - 24 || b.y0 > nb.y1 + 14 || b.y1 < nb.y0 - 14;
    return apart && b.x0 >= PAD.l && b.x1 <= w - PAD.r && b.y0 >= 0 && b.y1 <= base + 2 && !taken.some((t) => b.x0 < t.x1 + 3 && b.x1 > t.x0 - 3 && b.y0 < t.y1 + 2 && b.y1 > t.y0 - 2);
  };
  // The dotted estimate, sampled, so a label never lands on it either.
  const estimatePts: Pt[] = [];
  if (ex != null && ey != null) for (let t = 0; t <= 1; t += 0.125) estimatePts.push({ x: lx + (ex - lx) * t, y: ly + (ey - ly) * t });
  const packLinePts = packOn ? thin(packPts).map((p) => ({ x: sx(p.x), y: sk(p.y) })) : [];
  const packSpot = packSpots.find(([x, y, a]) => packFree(x, y, a) && ![socPts, packLinePts, estimatePts].some((line) => onLine(boxOf(x, y, packW, a, 14), line)));

  return (
    <div data-testid="now-charge-graph" className="flex min-w-0 flex-col">
      <GraphHead title="Battery while charging" change={gained > 0 ? `+${gained}%` : null} since={`since ${clock(first.x)}`} />
      <div ref={ref} className="h-[150px] min-w-0 min-[900px]:h-[200px]">
        {w > 0 && h > 0 && (
          <svg
            width={w}
            height={h}
            viewBox={`0 0 ${w} ${h}`}
            role="img"
            aria-label={`Battery from ${startShown}% to ${nowShown}% since ${clock(first.x)}${nowKw != null ? `, charging at ${Math.round(nowKw)} kilowatts` : ""}${pill ? `, reaching ${pill.replace("·", "at")}` : ""}${packNow ? `, pack temperature ${packNow} now, ${packDelta === 0 ? "unchanged" : `${packDelta > 0 ? "up" : "down"} ${Math.abs(packDelta)} degrees`} over the charge` : ""}`}
            className="block overflow-visible"
          >
            <SoftDefs id={id} ink={ink} />
            {ex != null && <rect x={lx + 12} y={PAD.t + 4} width={Math.max(ex - lx - 20, 4)} height={base - PAD.t - 4} rx={28} fill={`url(#${id}-band)`} filter={`url(#${id}-soften)`} />}
            <SoftFill id={id} d={line} baseY={base} x0={socPts[0].x} x1={lx} />
            {powPts.length > 1 && <QuietLine d={softPath(powPts)} color={ink.quiet} />}
            {packOn && <path d={packPath} data-testid="now-charge-graph-pack-line" fill="none" stroke={ink.pack} strokeWidth={1.4} strokeLinecap="round" strokeLinejoin="round" />}
            <SoftLine id={id} d={line} color={ink.line} />
            {ex != null && ey != null && (
              <>
                <path d={estimate} data-testid="now-charge-graph-estimate-line" fill="none" stroke={ink.line} strokeWidth={2.2} strokeLinecap="round" strokeDasharray="0.1 7" strokeOpacity=".8" />
                <HaloDot id={id} x={ex} y={ey} color={ink.line} />
                <Pill x={w - PAD.r} y={Math.max(ey - 18, 12)} text={pill ?? ""} w={labelWidth(pill ?? "")} anchor="end" />
              </>
            )}
            <HaloDot id={id} x={socPts[0].x} y={socPts[0].y} color={ink.line} />
            <HaloDot id={id} x={lx} y={ly} color={ink.line} big />
            {labelStartValue && (
              <text x={socPts[0].x + 2} y={Math.max(socPts[0].y - 14, 12)} className="fill-neutral-300 text-xs">
                {startShown}%
              </text>
            )}
            <text x={nowX} y={nowY} textAnchor={nowAnchor} className="fill-neutral-100 text-sm font-semibold">
              {nowShown}%
            </text>
            {packOn && <circle cx={packX} cy={packY} r={3} fill={ink.pack} />}
            {packOn && packSpot && (
              <text data-testid="now-charge-graph-pack-now" x={packSpot[0]} y={packSpot[1]} textAnchor={packSpot[2]} fill={ink.pack} className="text-sm font-semibold">
                {packNow}
              </text>
            )}
            <text x={PAD.l} y={h - 6} className="fill-neutral-500 text-xs">
              {startText}
            </text>
            {labelNow && (
              <text x={lx} y={h - 6} textAnchor="middle" className="fill-neutral-500 text-xs">
                now
              </text>
            )}
          </svg>
        )}
      </div>
      <Legend
        items={[
          { swatch: "line", color: ink.line, label: "Battery" },
          ...(nowKw != null ? [{ swatch: "line" as const, color: ink.quiet, label: `Power ${nowKw.toFixed(nowKw < 10 ? 1 : 0)} kW` }] : []),
          ...(packNow ? [{ swatch: "line" as const, color: ink.pack, label: `Pack ${packNow}`, testid: "now-charge-graph-pack" }] : []),
          ...(endX != null ? [{ swatch: "dash" as const, color: ink.line, label: "Estimate", testid: "now-charge-graph-estimate" }] : []),
        ]}
      />
    </div>
  );
}
