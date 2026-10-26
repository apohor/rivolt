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

// "Oct 1": a day's name on a chart axis, in the reader's language. Accepts a Date or a "2026-10-01" label (a local day).
export function dayName(d: Date | string): string {
  const day = typeof d === "string" ? new Date(Number(d.slice(0, 4)), Number(d.slice(5, 7)) - 1, Number(d.slice(8, 10))) : d;
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(day);
}
