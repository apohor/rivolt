// The one size and colour of a chart's axis labels, legends and footnotes (the app's caption size, secondary text), and the axis maths
// the day charts share with the iPhone: "nice" ticks and localised day names. The iPhone takes the size from ios/design/tokens.json
// (type.chartAxis).
export const CHART_AXIS_PX = 11;

// A round maximum and the tick values from 0 to it, for a chart whose data tops out at `max`. The step is 1, 2, 2.5 or 5 times a power
// of ten (whole numbers only: a chart of miles never shows 2.5), and the one with the lowest top that gives two or three intervals
// wins; four intervals only when nothing else fits. 509 gives 0 · 200 · 400 · 600, 7 gives 0 · 5 · 10.
export function niceAxis(max: number): { top: number; ticks: number[] } {
  const m = Number.isFinite(max) && max > 0 ? max : 1;
  const steps: number[] = [];
  for (let p = 1; p <= 1e9; p *= 10) for (const k of [1, 2, 2.5, 5]) if (Number.isInteger(k * p)) steps.push(k * p);
  const fits = (maxIntervals: number) =>
    steps
      .map((step) => ({ step, n: Math.max(1, Math.ceil(m / step - 1e-9)) }))
      .filter((c) => c.n >= 2 && c.n <= maxIntervals)
      .sort((a, b) => a.step * a.n - b.step * b.n || a.n - b.n)[0];
  const pick = fits(3) ?? fits(4) ?? { step: Math.ceil(m), n: 1 };
  const top = pick.step * pick.n;
  return { top, ticks: Array.from({ length: pick.n + 1 }, (_, i) => pick.step * i) };
}

// The same for a chart of durations in minutes: the step is a round number of minutes or hours (15 and 30 minutes, 1, 2, 3, 4, 6, 8, 12 or
// 24 hours and on), so the ticks read 0 · 8h · 16h · 24h, not 8h 20m · 16h 40m · 25h. Two or three intervals (three when two would reach the same top: 8h · 16h · 24h, not 12h · 24h); four only when nothing else fits.
export function niceMinutesAxis(max: number): { top: number; ticks: number[] } {
  const m = Number.isFinite(max) && max > 0 ? max : 60;
  const steps = [15, 30, 60, 120, 180, 240, 360, 480, 720, 1440, 2880, 5760, 10080, 20160, 40320];
  const fits = (maxIntervals: number) =>
    steps
      .map((step) => ({ step, n: Math.max(1, Math.ceil(m / step - 1e-9)) }))
      .filter((c) => c.n >= 2 && c.n <= maxIntervals)
      .sort((a, b) => a.step * a.n - b.step * b.n || b.n - a.n)[0];
  const pick = fits(3) ?? fits(4) ?? { step: Math.ceil(m / 60) * 60, n: 1 };
  return { top: pick.step * pick.n, ticks: Array.from({ length: pick.n + 1 }, (_, i) => pick.step * i) };
}

// "Oct 1": a day's name on a chart axis, in the reader's language. Accepts a Date or a "2026-10-01" label (a local day).
export function dayName(d: Date | string): string {
  const day = typeof d === "string" ? new Date(Number(d.slice(0, 4)), Number(d.slice(5, 7)) - 1, Number(d.slice(8, 10))) : d;
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(day);
}

// Which of an axis's labels fit side by side: each is `x` (its anchor, in screen px), `w` (its text's width in px) and `anchor`. The first and
// the last are kept when they can be; a label that would touch the one before it (with `gap` px between) is dropped, and the last wins over
// the one before it, since the end of the period is what the axis is for. Returns the kept indices, in order.
export function fitAxisLabels(items: { x: number; w: number; anchor: "start" | "middle" | "end" }[], gap = 6): number[] {
  const span = (it: { x: number; w: number; anchor: "start" | "middle" | "end" }) => {
    const left = it.anchor === "start" ? it.x : it.anchor === "end" ? it.x - it.w : it.x - it.w / 2;
    return [left, left + it.w] as const;
  };
  const kept: number[] = [];
  items.forEach((it, i) => {
    const last = i === items.length - 1;
    while (kept.length && span(items[kept[kept.length - 1]])[1] + gap > span(it)[0]) {
      // Overlap: the last label displaces the one before it (never the first), any other label gives way.
      if (last && kept.length > 1) kept.pop();
      else return;
    }
    kept.push(i);
  });
  return kept;
}

/** An estimate of a label's drawn width at the chart axis size (digits and letters of a UI font are about 0.6 em wide). */
export const axisLabelWidth = (text: string): number => Math.ceil(text.length * CHART_AXIS_PX * 0.6);
