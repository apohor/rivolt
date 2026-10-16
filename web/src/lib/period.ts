import { useEffect, useState } from "react";

// Calendar-aligned scope for the ui_theme_next layer: Week (Mon to Sun),
// Month, Year, All or a Custom date range, with stepping to earlier periods.
// The choice is remembered per device. Ranges are computed in the browser's
// local calendar and are half-open: [start, end). `null` bounds are open.

export type PeriodKind = "week" | "month" | "year" | "all" | "custom";

export type Period = {
  kind: PeriodKind;
  // 0 = the current period, -1 = the previous one, ... (week/month/year only)
  offset: number;
  // Custom range, inclusive dates as yyyy-mm-dd (local).
  from?: string;
  to?: string;
};

export type PeriodRange = { start: Date | null; end: Date | null };

export const DEFAULT_PERIOD: Period = { kind: "month", offset: 0 };

const KEY = "rivolt.period";

function startOfDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}

function addDays(d: Date, n: number): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
}

function parseYmd(s: string | undefined): Date | null {
  if (!s || !/^\d{4}-\d{2}-\d{2}$/.test(s)) return null;
  const [y, m, d] = s.split("-").map(Number);
  const dt = new Date(y, m - 1, d);
  return Number.isNaN(dt.getTime()) ? null : dt;
}

export function toYmd(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export function periodRange(p: Period, now: Date = new Date()): PeriodRange {
  switch (p.kind) {
    case "all":
      return { start: null, end: null };
    case "week": {
      const today = startOfDay(now);
      const monday = addDays(today, -((today.getDay() + 6) % 7));
      const start = addDays(monday, p.offset * 7);
      return { start, end: addDays(start, 7) };
    }
    case "month": {
      const start = new Date(now.getFullYear(), now.getMonth() + p.offset, 1);
      return { start, end: new Date(start.getFullYear(), start.getMonth() + 1, 1) };
    }
    case "year": {
      const y = now.getFullYear() + p.offset;
      return { start: new Date(y, 0, 1), end: new Date(y + 1, 0, 1) };
    }
    case "custom": {
      const a = parseYmd(p.from);
      const b = parseYmd(p.to);
      if (!a || !b) return { start: null, end: null };
      const [lo, hi] = a <= b ? [a, b] : [b, a];
      return { start: lo, end: addDays(hi, 1) };
    }
  }
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

// A short label for the picker: "Oct 6–12", "Sep 29–Oct 5", "Oct 2026",
// "2026", "All time", "Oct 3 – Oct 17, 2026".
export function periodLabel(p: Period, now: Date = new Date()): string {
  const r = periodRange(p, now);
  if (p.kind === "all" || !r.start || !r.end) return p.kind === "custom" ? "Pick dates" : "All time";
  const last = addDays(r.end, -1);
  if (p.kind === "year") return String(r.start.getFullYear());
  if (p.kind === "month") return `${MONTHS[r.start.getMonth()]} ${r.start.getFullYear()}`;
  if (p.kind === "week") {
    const sameMonth = r.start.getMonth() === last.getMonth();
    return sameMonth
      ? `${MONTHS[r.start.getMonth()]} ${r.start.getDate()}–${last.getDate()}`
      : `${MONTHS[r.start.getMonth()]} ${r.start.getDate()}–${MONTHS[last.getMonth()]} ${last.getDate()}`;
  }
  const sameYear = r.start.getFullYear() === last.getFullYear();
  const a = `${MONTHS[r.start.getMonth()]} ${r.start.getDate()}${sameYear ? "" : `, ${r.start.getFullYear()}`}`;
  return `${a} – ${MONTHS[last.getMonth()]} ${last.getDate()}, ${last.getFullYear()}`;
}

export function canStep(p: Period): boolean {
  return p.kind === "week" || p.kind === "month" || p.kind === "year";
}

// stepPeriod moves to an earlier or later period; it never goes past the
// current one (no future periods).
export function stepPeriod(p: Period, delta: -1 | 1): Period {
  if (!canStep(p)) return p;
  return { ...p, offset: Math.min(0, p.offset + delta) };
}

// Days a range spans (for per-day bars); null when open-ended.
export function rangeDays(r: PeriodRange): number | null {
  if (!r.start || !r.end) return null;
  return Math.round((r.end.getTime() - r.start.getTime()) / 86_400_000);
}

// The last day shown for daily bars: the period's last day, but never later
// than today (the current period is still in progress).
export function barsEndDay(r: PeriodRange, now: Date = new Date()): Date {
  if (!r.end) return now;
  const last = addDays(r.end, -1);
  return last > now ? now : last;
}

export function filterByRange<T extends { StartedAt: string }>(items: T[], r: PeriodRange): T[] {
  if (!r.start && !r.end) return items;
  const lo = r.start ? r.start.getTime() : -Infinity;
  const hi = r.end ? r.end.getTime() : Infinity;
  return items.filter((it) => {
    const t = new Date(it.StartedAt).getTime();
    return t >= lo && t < hi;
  });
}

function sanitize(raw: unknown): Period {
  const o = (raw ?? {}) as Partial<Period>;
  const kind: PeriodKind = o.kind === "week" || o.kind === "month" || o.kind === "year" || o.kind === "all" || o.kind === "custom" ? o.kind : "month";
  const offset = typeof o.offset === "number" && Number.isFinite(o.offset) ? Math.min(0, Math.round(o.offset)) : 0;
  const out: Period = { kind, offset: kind === "all" || kind === "custom" ? 0 : offset };
  if (kind === "custom") {
    if (parseYmd(o.from)) out.from = o.from;
    if (parseYmd(o.to)) out.to = o.to;
  }
  return out;
}

export function loadPeriod(): Period {
  try {
    const raw = window.localStorage.getItem(KEY);
    return raw ? sanitize(JSON.parse(raw)) : DEFAULT_PERIOD;
  } catch {
    return DEFAULT_PERIOD;
  }
}

const EVENT = "rivolt:period";

export function savePeriod(p: Period) {
  try {
    window.localStorage.setItem(KEY, JSON.stringify(p));
  } catch {
    /* blocked storage: the choice still applies for this session via the event */
  }
  window.dispatchEvent(new CustomEvent(EVENT, { detail: p }));
}

// usePeriod: the shared per-device scope (Now, Activity and Truck follow it).
export function usePeriod(): [Period, (p: Period) => void] {
  const [p, setP] = useState<Period>(() => loadPeriod());
  useEffect(() => {
    const on = (e: Event) => setP(sanitize((e as CustomEvent<Period>).detail));
    const onStorage = (e: StorageEvent) => {
      if (e.key === KEY) setP(loadPeriod());
    };
    window.addEventListener(EVENT, on);
    window.addEventListener("storage", onStorage);
    return () => {
      window.removeEventListener(EVENT, on);
      window.removeEventListener("storage", onStorage);
    };
  }, []);
  return [p, savePeriod];
}
