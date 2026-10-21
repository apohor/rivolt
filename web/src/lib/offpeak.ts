// The off-peak schedule: windows of the week with their own rate, in the user's time zone. Hours outside every window are priced at
// the home rate. The checks here mirror the server's (it refuses the same things with a 400) so a mistake shows next to its row.

export type OffpeakDays = "all" | "weekdays" | "weekends";
export type OffpeakWindow = { days: OffpeakDays; start: string; end: string; rate: number; label?: string };
export type OffpeakSchedule = {
  tz: string;
  windows: OffpeakWindow[];
  // GET only: whether a schedule is saved, the flat home rate outside the windows (0 = not set) and the currency both are in.
  configured?: boolean;
  base_rate?: number;
  currency?: string;
};

// A window as edited: the rate is text until it's valid.
export type DraftWindow = { days: OffpeakDays; start: string; end: string; rate: string; label: string };

export const MAX_WINDOWS = 8;
export const RATE_MAX = 5;
export const LABEL_MAX = 40;

export const DAYS_LABEL: Record<OffpeakDays, string> = { all: "Every day", weekdays: "Weekdays", weekends: "Weekends" };

const dayIndexes = (d: OffpeakDays): number[] => (d === "weekdays" ? [0, 1, 2, 3, 4] : d === "weekends" ? [5, 6] : [0, 1, 2, 3, 4, 5, 6]);

export function minutes(t: string): number | null {
  const m = /^(\d{2}):(\d{2})$/.exec(t);
  if (!m) return null;
  const h = Number(m[1]);
  const mi = Number(m[2]);
  return h < 24 && mi < 60 ? h * 60 + mi : null;
}

// The week as [start, end) minute ranges, Monday 00:00 = 0. A window that ends after midnight continues into the next day, and the
// last day wraps into Monday.
export function weekIntervals(w: Pick<DraftWindow, "days" | "start" | "end">): [number, number][] {
  const s = minutes(w.start);
  const e = minutes(w.end);
  if (s === null || e === null || s === e) return [];
  const len = (e - s + 1440) % 1440;
  const out: [number, number][] = [];
  for (const d of dayIndexes(w.days)) {
    const from = d * 1440 + s;
    const to = from + len;
    if (to <= 10080) out.push([from, to]);
    else {
      out.push([from, 10080]);
      out.push([0, to - 10080]);
    }
  }
  return out;
}

export const fmtClock = (t: string): string => {
  const m = minutes(t);
  if (m === null) return t;
  const h = Math.floor(m / 60);
  const mi = m % 60;
  return `${h % 12 === 0 ? 12 : h % 12}${mi ? `:${String(mi).padStart(2, "0")}` : ""} ${h < 12 ? "AM" : "PM"}`;
};

export const describeWindow = (w: DraftWindow): string => `${DAYS_LABEL[w.days]}, ${fmtClock(w.start)} to ${fmtClock(w.end)}`;

export type WindowErrors = { rate?: string; time?: string; label?: string; overlap?: string };

export function validateWindows(ws: DraftWindow[]): WindowErrors[] {
  const errs: WindowErrors[] = ws.map((w) => {
    const e: WindowErrors = {};
    const r = w.rate.trim();
    if (r === "") e.rate = "Enter a rate.";
    else if (!Number.isFinite(Number(r))) e.rate = "Enter a number.";
    else if (Number(r) < 0 || Number(r) > RATE_MAX) e.rate = `The rate must be between 0 and ${RATE_MAX} per kWh.`;
    else if (/\.\d{5,}$/.test(r)) e.rate = "Use at most 4 decimal places.";
    if (minutes(w.start) === null || minutes(w.end) === null) e.time = "Enter a start and an end time.";
    else if (w.start === w.end) e.time = "The start and the end can't be the same time.";
    if (w.label.length > LABEL_MAX) e.label = `The name can be at most ${LABEL_MAX} characters.`;
    return e;
  });
  const iv = ws.map(weekIntervals);
  for (let j = 1; j < ws.length; j++) {
    for (let i = 0; i < j; i++) {
      const clash = iv[i].some(([a, b]) => iv[j].some(([c, d]) => a < d && c < b));
      if (clash && !errs[j].overlap) errs[j].overlap = `Overlaps window ${i + 1} (${describeWindow(ws[i])}). Change one of them.`;
    }
  }
  return errs;
}

export const hasErrors = (errs: WindowErrors[]): boolean => errs.some((e) => e.rate || e.time || e.label || e.overlap);

// Which hours of an ordinary weekday / weekend day a schedule covers, for the strip under the editor: [0..23] true when any window
// covers that whole hour on that kind of day.
export function coverage(ws: DraftWindow[], kind: "weekdays" | "weekends"): boolean[] {
  const day = kind === "weekdays" ? 1 : 5; // Tuesday / Saturday
  const out = Array<boolean>(24).fill(false);
  for (const w of ws) {
    for (const [a, b] of weekIntervals(w)) {
      for (let h = 0; h < 24; h++) {
        const from = day * 1440 + h * 60;
        if (a <= from && from + 60 <= b) out[h] = true;
      }
    }
  }
  return out;
}

export const toDraft = (s: OffpeakSchedule): DraftWindow[] => s.windows.map((w) => ({ days: w.days, start: w.start, end: w.end, rate: String(w.rate), label: w.label ?? "" }));

export const toWindows = (ws: DraftWindow[]): OffpeakWindow[] =>
  ws.map((w) => ({ days: w.days, start: w.start, end: w.end, rate: Math.round(Number(w.rate) * 10000) / 10000, ...(w.label.trim() ? { label: w.label.trim() } : {}) }));

// A server refusal (400 {"error", "field": "windows[1].start"}) as the row and field it belongs to; row is null for the tz or the count.
export function serverFieldError(body: unknown): { row: number | null; field: string; message: string } | null {
  if (!body || typeof body !== "object") return null;
  const b = body as { error?: unknown; field?: unknown };
  if (typeof b.error !== "string" || typeof b.field !== "string") return null;
  const m = /^windows\[(\d+)\]\.?(\w*)$/.exec(b.field);
  return { row: m ? Number(m[1]) : null, field: m ? m[2] : b.field, message: b.error };
}
