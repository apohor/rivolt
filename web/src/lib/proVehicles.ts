// A free account with several vehicles shows one (its primary); the rest are listed with pro_locked. Nothing about them is deleted or
// stops: they keep recording and alerting, and stay in the export.

import type { Vehicle } from "./api";

export const isLocked = (v: Pick<Vehicle, "pro_locked"> | undefined | null): boolean => !!v?.pro_locked;

// The vehicle to open on: the stored choice when it is open, else the first open one, else the first (a locked vehicle is never the
// default, so a free account lands on the vehicle it can see).
export function defaultVehicleID(list: Vehicle[], stored: string | null): string | undefined {
  if (list.length === 0) return undefined;
  const open = list.filter((v) => !v.pro_locked);
  if (stored && open.some((v) => v.id === stored)) return stored;
  return (open[0] ?? list[0]).id;
}

// "Name · Pro" for a locked vehicle in a picker.
export const pickerLabel = (name: string, v: Pick<Vehicle, "pro_locked">): string => (v.pro_locked ? `${name} · Pro` : name);
