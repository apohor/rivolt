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

// A tile (phone) says value then label or note; a row (desktop) puts rowLabel left and rowValue with the note right.
export type ParkedFigure = { id: "asleep" | "wakes" | "idle"; value: string; note: string; label: string; rowLabel: string; rowValue: string };

export type ParkedText = {
  // "−2.4%" and "since 5:20 PM"; null until the truck has two readings since parking.
  change: { value: string; since: string } | null;
  // Parked since, as the pill on the graph.
  parkedAt: string | null;
  figures: ParkedFigure[];
  // "At this rate: about 61% by Monday 8:00 AM (estimate)."; null until parked a few hours.
  projection: string | null;
};

export function parkedText(s: ParkedSeries, unit: "mi" | "km"): ParkedText {
  const since = s.parked_since ? clock(Date.parse(s.parked_since)) : null;
  const change = s.change_pct != null && since ? { value: `${s.change_pct < 0 ? MIN : s.change_pct > 0 ? "+" : ""}${Math.abs(s.change_pct).toFixed(1)}%`, since: `since ${since}` } : null;
  const f = s.figures;
  const figures: ParkedFigure[] = [];
  if (f) {
    figures.push({ id: "asleep", value: longDuration(f.asleep_today_s), note: "", label: "asleep today", rowLabel: "Asleep today", rowValue: longDuration(f.asleep_today_s) });
    if (f.wake_ups > 0) {
      figures.push({ id: "wakes", value: `${f.wake_ups} ${f.wake_ups === 1 ? "wake-up" : "wake-ups"}`, note: f.wake_cost_pct != null ? `≈ ${f.wake_cost_pct.toFixed(1)}% each` : "", label: "wake-ups", rowLabel: "Wake-ups", rowValue: String(f.wake_ups) });
    }
    if (f.idle_pct_per_day != null) {
      const mi = f.idle_mi_per_day;
      const dist = mi != null ? Math.round(unit === "km" ? mi * KM_PER_MI : mi) : null;
      figures.push({ id: "idle", value: `${Math.round(f.idle_pct_per_day)}% a day`, note: dist != null ? `≈ ${dist} ${unit} of range` : "", label: "idle loss", rowLabel: "Idle loss", rowValue: `${Math.round(f.idle_pct_per_day)}% a day` });
    }
  }
  let projection: string | null = null;
  if (s.projection) {
    const at = new Date(s.projection.at);
    const day = at.toLocaleDateString(undefined, { weekday: "long", timeZone: displayTimeZone() });
    projection = `At this rate: about ${Math.round(s.projection.soc)}% by ${day} ${clock(at.getTime())} (estimate).`;
  }
  return { change, parkedAt: since ? `Parked ${since}` : null, figures, projection };
}

// The graph draws only when there is a line to draw: a few readings over the window.
export function parkedDrawable(s: ParkedSeries | null | undefined): s is ParkedSeries {
  return !!s && s.points.length >= 4;
}
