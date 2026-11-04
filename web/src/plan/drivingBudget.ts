import { useSyncExternalStore } from "react";

// "Driving per day": how many hours the person will drive in a day, for the trip planner. 4 to 14 h in whole-hour steps, 8 h until changed, kept
// on this device. A drive longer than it is offered over more than one day, and the search for somewhere to sleep (`budget_hours`) looks where it
// runs out, so every use of the limit reads this one number.
export const DRIVING_DEFAULT_HOURS = 8;
export const DRIVING_MIN_HOURS = 4;
export const DRIVING_MAX_HOURS = 14;
export const DRIVING_STEP_HOURS = 1;
const KEY = "rivolt.drivingHoursPerDay";

export function clampDrivingHours(v: number): number {
  if (!Number.isFinite(v)) return DRIVING_DEFAULT_HOURS;
  return Math.min(DRIVING_MAX_HOURS, Math.max(DRIVING_MIN_HOURS, Math.round(v / DRIVING_STEP_HOURS) * DRIVING_STEP_HOURS));
}

// "8 h".
export function drivingHoursLabel(h: number): string {
  return `${Number.isInteger(h) ? h : h.toFixed(1)} h`;
}

export function drivingHoursOptions(): number[] {
  const out: number[] = [];
  for (let h = DRIVING_MIN_HOURS; h <= DRIVING_MAX_HOURS + 1e-9; h += DRIVING_STEP_HOURS) out.push(h);
  return out;
}

export function readDrivingHours(): number {
  try {
    const raw = window.localStorage.getItem(KEY);
    return raw === null ? DRIVING_DEFAULT_HOURS : clampDrivingHours(Number(raw));
  } catch {
    return DRIVING_DEFAULT_HOURS;
  }
}

const listeners = new Set<() => void>();
let memory: number | null = null;

export function writeDrivingHours(h: number): void {
  const v = clampDrivingHours(h);
  memory = v;
  try {
    window.localStorage.setItem(KEY, String(v));
  } catch {
    /* kept for this visit only */
  }
  listeners.forEach((l) => l());
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb);
  window.addEventListener("storage", cb);
  return () => {
    listeners.delete(cb);
    window.removeEventListener("storage", cb);
  };
}

const snapshot = (): number => {
  const stored = readDrivingHours();
  // Storage unavailable: the value set this visit.
  return memory !== null && stored === DRIVING_DEFAULT_HOURS ? memory : stored;
};

export function useDrivingHours(): [number, (h: number) => void] {
  return [useSyncExternalStore(subscribe, snapshot, () => DRIVING_DEFAULT_HOURS), writeDrivingHours];
}
