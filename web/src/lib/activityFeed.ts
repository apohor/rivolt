// Pure builder for the Activity feed (ux-report S3, v1 without trips):
// drives and charges merged into day groups, short errands folded into
// one expandable row. No coordinates leave this module: places are
// "Home" or nothing.

import type { Charge, Drive } from "./api";
import { isActiveCharge } from "./format";

export const ERRAND_MAX_MI = 3;
export const HOME_RADIUS_M = 250;

export type Filter = "all" | "drives" | "charges";

export type DriveItem = { kind: "drive"; at: number; drive: Drive; from: string | null; to: string | null };
export type ChargeItem = { kind: "charge"; at: number; charge: Charge; atHome: boolean; fast: boolean };
export type ErrandItem = {
  kind: "errands";
  at: number;
  drives: DriveItem[];
  miles: number;
  // null when any leg lacks a cost (never a partial sum).
  cost: number | null;
  currency: string;
};
export type FeedItem = DriveItem | ChargeItem | ErrandItem;
export type FeedDay = { day: string; items: FeedItem[] };

type Pt = { latitude: number; longitude: number } | null;

function metersBetween(a: { latitude: number; longitude: number }, lat: number, lon: number): number {
  const R = 6371000;
  const rad = (d: number) => (d * Math.PI) / 180;
  const dLat = rad(lat - a.latitude);
  const dLon = rad(lon - a.longitude);
  const h = Math.sin(dLat / 2) ** 2 + Math.cos(rad(a.latitude)) * Math.cos(rad(lat)) * Math.sin(dLon / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(h));
}

function isHome(home: Pt, lat: number, lon: number): boolean {
  if (!home || (lat === 0 && lon === 0)) return false;
  return metersBetween(home, lat, lon) <= HOME_RADIUS_M;
}

export function dayKey(ms: number, tz?: string): string {
  // en-CA gives YYYY-MM-DD, sortable and timezone-correct.
  return new Date(ms).toLocaleDateString("en-CA", { timeZone: tz });
}

export function isErrand(d: Drive): boolean {
  return d.DistanceMi > 0 && d.DistanceMi <= ERRAND_MAX_MI;
}

export function buildFeed(
  drives: Drive[],
  charges: Charge[],
  opts: { home: Pt; filter: Filter; tz?: string; collapseErrands?: boolean },
): FeedDay[] {
  const items: (DriveItem | ChargeItem)[] = [];
  if (opts.filter !== "charges") {
    for (const d of drives) {
      items.push({
        kind: "drive",
        at: new Date(d.StartedAt).getTime(),
        drive: d,
        from: isHome(opts.home, d.StartLat, d.StartLon) ? "Home" : null,
        to: isHome(opts.home, d.EndLat, d.EndLon) ? "Home" : null,
      });
    }
  }
  if (opts.filter !== "drives") {
    for (const c of charges) {
      items.push({
        kind: "charge",
        at: new Date(c.StartedAt).getTime(),
        charge: c,
        atHome: isHome(opts.home, c.Lat, c.Lon),
        fast: c.MaxPowerKW >= 50,
      });
    }
  }
  items.sort((a, b) => b.at - a.at);

  const byDay = new Map<string, FeedItem[]>();
  for (const it of items) {
    const k = dayKey(it.at, opts.tz);
    const arr = byDay.get(k) ?? [];
    arr.push(it);
    byDay.set(k, arr);
  }
  const days: FeedDay[] = [];
  for (const [day, list] of byDay) {
    days.push({ day, items: opts.collapseErrands === false ? list : foldErrands(list) });
  }
  days.sort((a, b) => (a.day < b.day ? 1 : -1));
  return days;
}

// Runs of >= 2 adjacent short drives (no charge between them) fold
// into one row. A lone short drive stays a normal card.
function foldErrands(list: FeedItem[]): FeedItem[] {
  const out: FeedItem[] = [];
  let run: DriveItem[] = [];
  const flush = () => {
    if (run.length >= 2) {
      const miles = run.reduce((a, r) => a + r.drive.DistanceMi, 0);
      const costs = run.map((r) => r.drive.estimated_cost);
      const cost = costs.every((c) => c != null) ? (costs as number[]).reduce((a, c) => a + c, 0) : null;
      out.push({
        kind: "errands",
        at: run[0].at,
        drives: run,
        miles,
        cost,
        currency: run[0].drive.estimated_currency || "",
      });
    } else out.push(...run);
    run = [];
  };
  for (const it of list) {
    if (it.kind === "drive" && isErrand(it.drive)) run.push(it);
    else {
      flush();
      out.push(it);
    }
  }
  flush();
  return out;
}

export type CostLine = { text: string; tone: "ok" | "est" | "missing" };

export function chargeCostLine(c: Charge): CostLine {
  if (isActiveCharge(c)) return { text: "Charging now", tone: "ok" };
  if (c.CostSource === "user" && c.Cost === 0) return { text: "Free", tone: "ok" };
  if (c.Cost > 0) return { text: `${c.Cost.toFixed(2)} ${c.Currency || ""}`.trim(), tone: "ok" };
  if (c.estimated_cost && c.estimated_cost > 0) {
    return { text: `~${c.estimated_cost.toFixed(2)} ${c.estimated_currency || ""}`.trim(), tone: "est" };
  }
  return { text: "Unpriced", tone: "missing" };
}

// A drive that used battery but has no energy: shown, flagged, and
// never given a made-up cost.
export function driveMissingEnergy(d: Drive): boolean {
  return d.DistanceMi > 0.1 && !(d.EnergyUsedKWh > 0);
}
