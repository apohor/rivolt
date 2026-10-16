// The charging schedule line on Now ("charging starts 11:00 PM (schedule)").
// Schedules come from /api/state (charging_schedules): daily windows, start and
// duration in minutes after local midnight, on the listed weekdays, at a place.
export type ChargingSchedule = {
  start_min: number;
  duration_min: number;
  enabled: boolean;
  weekdays: string[];
  amperage_a?: number;
  // Whether the truck is at the schedule's location; absent when unknown.
  applies_here?: boolean | null;
};

// Rivian's weekday strings pass through untouched, so "Monday", "MONDAY" and "Mon" all match:
// compare the lowercased first three letters.
const day3 = (d: string) => d.trim().slice(0, 3).toLowerCase();

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

// The weekday name and the minutes after midnight "now" is at, in the display zone.
export function localParts(now: Date, timeZone?: string): { weekday: string; minutes: number } {
  const f = new Intl.DateTimeFormat("en-US", { timeZone, weekday: "long", hour: "numeric", minute: "numeric", hourCycle: "h23" });
  const p = Object.fromEntries(f.formatToParts(now).map((x) => [x.type, x.value]));
  const weekday = DAYS.includes(p.weekday) ? p.weekday : DAYS[now.getDay()];
  return { weekday, minutes: (Number(p.hour) % 24) * 60 + Number(p.minute) };
}

// The start (minutes after midnight) of the next scheduled window today, or null. Only
// schedules that are on, apply where the truck is, and start today are considered; a
// window that has already started is not "upcoming" (the truck would be charging).
export function nextScheduledStartMin(schedules: ChargingSchedule[] | undefined | null, now: Date, timeZone?: string): number | null {
  if (!schedules?.length) return null;
  const { weekday, minutes } = localParts(now, timeZone);
  const starts = schedules
    .filter((s) => s.enabled && s.applies_here === true && s.weekdays.some((d) => day3(d) === day3(weekday)) && s.start_min > minutes)
    .map((s) => s.start_min);
  return starts.length ? Math.min(...starts) : null;
}

export function formatScheduleStart(startMin: number): string {
  const d = new Date(2000, 0, 1, Math.floor(startMin / 60), startMin % 60);
  return d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
}
