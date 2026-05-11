// Aggregation helpers for drives/charges. Pure functions — easy to
// unit-test later and keep UI components thin.

import type { Drive, Charge } from "./api";

export type WindowKey = "7d" | "30d" | "90d" | "365d" | "all";

export const WINDOW_OPTIONS: { key: WindowKey; label: string }[] = [
  { key: "7d", label: "7 days" },
  { key: "30d", label: "30 days" },
  { key: "90d", label: "90 days" },
  { key: "365d", label: "1 year" },
  { key: "all", label: "All" },
];

export function windowStart(key: WindowKey, now = new Date()): Date | null {
  if (key === "all") return null;
  const days = key === "7d" ? 7 : key === "30d" ? 30 : key === "90d" ? 90 : 365;
  const d = new Date(now);
  d.setDate(d.getDate() - days);
  return d;
}

export function filterByWindow<T extends { StartedAt: string }>(
  items: T[],
  key: WindowKey,
): T[] {
  const since = windowStart(key);
  if (!since) return items;
  const ms = since.getTime();
  return items.filter((it) => new Date(it.StartedAt).getTime() >= ms);
}

export type DriveStats = {
  count: number;
  miles: number;
  socUsedPct: number; // sum of (start - end) across drives; coarse
  milesPerPct: number; // rough efficiency proxy
  // Pack-side energy (sum of drive.EnergyUsedKWh) across drives with a
  // known value. Drives without EnergyUsedKWh are excluded from both
  // this sum and milesForEnergy, so miPerKWh reflects only drives we
  // can trust.
  energyUsedKWh: number;
  milesForEnergy: number;
  miPerKWh: number;
  avgTripMi: number;
  longestMi: number;
  maxSpeedMph: number;
  // Sum of per-drive estimated costs (rate × energy). Currency is
  // the dominant code by drive count — single-currency setups
  // collapse to one value, mixed-currency users get the most
  // common one with a small inaccuracy on the minority. Zero when
  // no drive in the window has an estimate.
  cost: number;
  currency: string;
};

export function driveStats(drives: Drive[]): DriveStats {
  const count = drives.length;
  const miles = sum(drives.map((d) => d.DistanceMi || 0));
  const socUsedPct = sum(
    drives.map((d) => Math.max(0, (d.StartSoCPct || 0) - (d.EndSoCPct || 0))),
  );
  const milesPerPct = socUsedPct > 0 ? miles / socUsedPct : 0;
  const withEnergy = drives.filter(
    (d) => (d.EnergyUsedKWh || 0) > 0 && (d.DistanceMi || 0) > 0,
  );
  const energyUsedKWh = sum(withEnergy.map((d) => d.EnergyUsedKWh));
  const milesForEnergy = sum(withEnergy.map((d) => d.DistanceMi));
  const miPerKWh = energyUsedKWh > 0 ? milesForEnergy / energyUsedKWh : 0;
  const avgTripMi = count > 0 ? miles / count : 0;
  const longestMi = drives.reduce((m, d) => Math.max(m, d.DistanceMi || 0), 0);
  const maxSpeedMph = drives.reduce((m, d) => Math.max(m, d.MaxSpeedMph || 0), 0);
  let cost = 0;
  const curCount: Record<string, number> = {};
  for (const d of drives) {
    if (d.estimated_cost && d.estimated_cost > 0) {
      cost += d.estimated_cost;
      const c = d.estimated_currency || "";
      curCount[c] = (curCount[c] || 0) + 1;
    }
  }
  const currency = pickDominantCurrency(curCount);
  return {
    count,
    miles,
    socUsedPct,
    milesPerPct,
    energyUsedKWh,
    milesForEnergy,
    miPerKWh,
    avgTripMi,
    longestMi,
    maxSpeedMph,
    cost,
    currency,
  };
}

export type ChargeStats = {
  count: number;
  energyKWh: number;
  addedPct: number;
  avgDurationMin: number;
  maxPowerKW: number;
  // Sum of charge cost (persisted Cost when present, else
  // estimated_cost from the home-rate fallback). Currency picked by
  // dominant cost share so the headline number is meaningful.
  cost: number;
  currency: string;
};

export function chargeStats(charges: Charge[]): ChargeStats {
  const count = charges.length;
  const energyKWh = sum(charges.map((c) => c.EnergyAddedKWh || 0));
  const addedPct = sum(
    charges.map((c) => Math.max(0, (c.EndSoCPct || 0) - (c.StartSoCPct || 0))),
  );
  const durations = charges.map(
    (c) => (new Date(c.EndedAt).getTime() - new Date(c.StartedAt).getTime()) / 60000,
  );
  const avgDurationMin =
    durations.length > 0 ? sum(durations) / durations.length : 0;
  const maxPowerKW = charges.reduce((m, c) => Math.max(m, c.MaxPowerKW || 0), 0);
  let cost = 0;
  const curCount: Record<string, number> = {};
  for (const c of charges) {
    if (c.Cost > 0) {
      cost += c.Cost;
      const cur = c.Currency || "";
      curCount[cur] = (curCount[cur] || 0) + 1;
    } else if (c.estimated_cost && c.estimated_cost > 0) {
      cost += c.estimated_cost;
      const cur = c.estimated_currency || "";
      curCount[cur] = (curCount[cur] || 0) + 1;
    }
  }
  const currency = pickDominantCurrency(curCount);
  return { count, energyKWh, addedPct, avgDurationMin, maxPowerKW, cost, currency };
}

// pickDominantCurrency picks the currency code most rows used.
// Empty / all-zero buckets return "". Ties break alphabetically so
// the result is deterministic across renders.
function pickDominantCurrency(counts: Record<string, number>): string {
  let best = "";
  let bestN = 0;
  for (const [cur, n] of Object.entries(counts)) {
    if (n > bestN || (n === bestN && cur < best)) {
      best = cur;
      bestN = n;
    }
  }
  return best;
}

// Bucket drives by local calendar day. Days with no drives show 0.
export function milesPerDay(
  drives: Drive[],
  days: number,
  now = new Date(),
): { label: string; value: number; x: number }[] {
  const buckets = new Map<string, number>();
  for (const d of drives) {
    const t = new Date(d.StartedAt);
    const key = ymd(t);
    buckets.set(key, (buckets.get(key) ?? 0) + (d.DistanceMi || 0));
  }
  const out: { label: string; value: number; x: number }[] = [];
  for (let i = days - 1; i >= 0; i--) {
    const t = new Date(now);
    t.setDate(t.getDate() - i);
    const key = ymd(t);
    out.push({
      label: key,
      value: buckets.get(key) ?? 0,
      x: t.getTime(),
    });
  }
  return out;
}

// Drive endpoints as SoC trend. Each drive contributes two points:
// (StartedAt, StartSoCPct) and (EndedAt, EndSoCPct). Plus charge
// endpoints. Sorted by time.
export function socTrend(
  drives: Drive[],
  charges: Charge[],
): { x: number; y: number }[] {
  const pts: { x: number; y: number }[] = [];
  for (const d of drives) {
    if (Number.isFinite(d.StartSoCPct) && d.StartSoCPct > 0) {
      pts.push({ x: new Date(d.StartedAt).getTime(), y: d.StartSoCPct });
    }
    if (Number.isFinite(d.EndSoCPct) && d.EndSoCPct > 0) {
      pts.push({ x: new Date(d.EndedAt).getTime(), y: d.EndSoCPct });
    }
  }
  for (const c of charges) {
    if (Number.isFinite(c.StartSoCPct) && c.StartSoCPct > 0) {
      pts.push({ x: new Date(c.StartedAt).getTime(), y: c.StartSoCPct });
    }
    if (Number.isFinite(c.EndSoCPct) && c.EndSoCPct > 0) {
      pts.push({ x: new Date(c.EndedAt).getTime(), y: c.EndSoCPct });
    }
  }
  pts.sort((a, b) => a.x - b.x);
  return pts;
}

function sum(xs: number[]): number {
  let s = 0;
  for (const x of xs) s += x;
  return s;
}

function ymd(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function pad(n: number): string {
  return n < 10 ? `0${n}` : `${n}`;
}
