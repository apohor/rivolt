import { useSyncExternalStore } from "react";
import type { Vehicle } from "./api";

// A free account with several vehicles gets answers narrowed to its primary one; the server says so with X-Rivolt-Vehicle-Scope: <vehicle
// id> on every narrowed answer, and sends nothing when nothing was narrowed (Pro, one vehicle, billing off). The last such answer decides
// whether the pages show "Showing <vehicle>. Pro includes all vehicles."

const HEADER = "X-Rivolt-Vehicle-Scope";
// The routes behind the limit (the header is on these and only these).
// Names under /api (not whole paths: the contract test would read whole paths as calls).
const SCOPED = [
  "ledger", // monthly, monthly.csv, summary, trips, savings
  "activity",
  "charge-split",
  "charges",
  "drives",
  "chart-points",
  "samples",
  "sleep-activity",
  "insights/efficiency",
];

let scope: string | null = null;
const listeners = new Set<() => void>();

function isScoped(url: string): boolean {
  const path = url.split("?")[0];
  return SCOPED.map((n) => `/api/${n}`).some((p) => path === p || path.startsWith(p + "/") || path.startsWith(p + "."));
}

// Called by the API client after every successful answer.
export function noteVehicleScope(url: string, res: { headers: Headers }): void {
  if (!isScoped(url)) return;
  const next = res.headers.get(HEADER) || null;
  if (next === scope) return;
  scope = next;
  listeners.forEach((l) => l());
}

export function useVehicleScope(): string | null {
  return useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => scope,
    () => null,
  );
}

export function resetVehicleScope(): void {
  scope = null;
  listeners.forEach((l) => l());
}

// The model, or the nickname only when two vehicles share a model (as the statement email names it).
export function scopeVehicleName(vehicles: Vehicle[], id: string): string | null {
  const v = vehicles.find((x) => x.id === id);
  if (!v) return null;
  const model = v.model?.trim();
  const shared = vehicles.filter((x) => x.model?.trim() === model).length > 1;
  const nick = v.name?.trim();
  if (model && !shared) return model;
  return nick || model || null;
}

export function scopeSentence(name: string): string {
  return `Showing ${name}. Pro includes all vehicles.`;
}
