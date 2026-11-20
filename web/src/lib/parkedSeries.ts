import type { ParkedSeries } from "./api";
import { displayTimeZone } from "./format";
import { clock } from "./nowState";

// The words and numbers of the Now card's "Battery while parked" graph. The server computes every figure; this only formats them, and the
// iPhone's ParkedSeries view says the same strings (same order, same rounding).

const KM_PER_MI = 1.609344;
const MIN = "−";

// The zone the server counts "today" and 08:00 in: the display zone, else the browser's.
export function parkedZone(): string {
  return displayTimeZone() ?? Intl.DateTimeFormat().resolvedOptions().timeZone ?? "UTC";
}

// "18 h 40 min", "45 min": the figure under the graph.
export function longDuration(sec: number): string {
  const m = Math.max(0, Math.round(sec / 60));
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")} min`;
}

// "6 h 10", "45 min": the label inside a sleep band.
export function bandDuration(sec: number): string {
  const m = Math.max(0, Math.round(sec / 60));
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")}`;
}

// A tile (phone) says value then label or its short note (the third tile has no room for "of range"); a row (desktop) puts rowLabel left and rowValue with the note right.
export type ParkedFigure = { id: "asleep" | "wakes" | "idle"; value: string; note: string; tileNote: string; label: string; rowLabel: string; rowValue: string };

export type ParkedText = {
  figures: ParkedFigure[];
  // "At this rate: about 61% by Monday 8:00 AM (estimate)."; null until parked a few hours.
  projection: string | null;
};

export function parkedText(s: ParkedSeries, unit: "mi" | "km"): ParkedText {
  const f = s.figures;
  const figures: ParkedFigure[] = [];
  if (f) {
    figures.push({ id: "asleep", value: longDuration(f.asleep_today_s), note: "", tileNote: "", label: "asleep today", rowLabel: "Asleep today", rowValue: longDuration(f.asleep_today_s) });
    if (f.wake_ups > 0) {
      // A cost that rounds to 0.0% says nothing: the count stands alone.
      const cost = f.wake_cost_pct != null && f.wake_cost_pct >= 0.05 ? `≈ ${f.wake_cost_pct.toFixed(1)}% each` : "";
      figures.push({ id: "wakes", value: `${f.wake_ups} ${f.wake_ups === 1 ? "wake-up" : "wake-ups"}`, note: cost, tileNote: cost, label: "wake-ups", rowLabel: "Wake-ups", rowValue: String(f.wake_ups) });
    }
    if (f.idle_pct_per_day != null) {
      const mi = f.idle_mi_per_day;
      const dist = mi != null ? Math.round(unit === "km" ? mi * KM_PER_MI : mi) : null;
      figures.push({ id: "idle", value: `${Math.round(f.idle_pct_per_day)}% a day`, note: dist != null ? `≈ ${dist} ${unit} of range` : "", tileNote: dist != null ? `≈ ${dist} ${unit}` : "", label: "idle loss", rowLabel: "Idle loss", rowValue: `${Math.round(f.idle_pct_per_day)}% a day` });
    }
  }
  let projection: string | null = null;
  if (s.projection) {
    const at = new Date(s.projection.at);
    const day = at.toLocaleDateString(undefined, { weekday: "long", timeZone: displayTimeZone() });
    projection = `At this rate: about ${Math.round(s.projection.soc)}% by ${day} ${clock(at.getTime())} (estimate).`;
  }
  return { figures, projection };
}

// The header change: the loss since the truck was parked. The base is the reading at the parked marker, so a drive or a charge before it
// never counts; when the parking began before the window the base is the window's first reading and the header says "in 24 h".
export function parkedChange(basePct: number, nowPct: number, sinceMs: number | null): { value: string; since: string } {
  const d = Math.round((nowPct - basePct) * 10) / 10;
  return { value: d === 0 ? "No loss" : `${d < 0 ? MIN : "+"}${Math.abs(d).toFixed(1)}%`, since: sinceMs != null ? `since ${clock(sinceMs)}` : "in 24 h" };
}

// "Parked 5:20 PM": the chip's own time, so the card says one parked time.
export function parkedAtLabel(ms: number): string {
  return `Parked ${clock(ms)}`;
}

// The graph draws only when there is a line to draw: a few readings over the window.
export function parkedDrawable(s: ParkedSeries | null | undefined): s is ParkedSeries {
  return !!s && s.points.length >= 4;
}

// The start of the sleep the truck is in now, from the series: a time; null when that sleep began before the 24 h window (its start is not
// known); undefined when the series has no open sleep.
export function openSleepSince(s: ParkedSeries): number | null | undefined {
  const sl = s.sleeps[s.sleeps.length - 1];
  if (!sl || !sl.open) return undefined;
  const from = Date.parse(sl.from);
  return from <= Date.parse(s.from) ? null : from;
}
