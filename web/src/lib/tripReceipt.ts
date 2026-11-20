import type { TripReceipt, TripCostEstimate } from "./api";
import type { DistanceUnit } from "./preferences";

// The receipt image of a saved trip (Share this trip, design key trip_receipt). The model is the privacy guarantee, like shareCard's: it is built
// from the server's receipt (areas and rounded figures, never a stored label or a coordinate) and carries display text only, so nothing else can
// reach the painter. Drawn on the device (receiptCard.ts); nothing is uploaded.

export type ReceiptLine = { title: string; right: string; sub: string; subRight: string };
export type ReceiptModel = {
  head: [string, string];
  title: string;
  sub: string;
  sketch: string[] | null;
  lines: ReceiptLine[];
  totals: { label: string; value: string }[];
  cost: { label: string; value: string } | null;
  big: [string, string];
  footer: string[];
};

// What the sheet says next to its buttons, in plain words (DA R4).
export const RECEIPT_SAYS = "The image shows the start and destination areas, your departure day and time, and the charging stops. Stops you added show only as an area. Distances and times near the start, the destination and your stops are rounded.";
export const RECEIPT_SKETCH_SAYS = "A line only. The start, the destination and any stops you added are cut off.";
// No place, date or name: the file name says the kind and nothing else (DA R9).
export const RECEIPT_FILE = "rivolt-trip-plan.png";
export const RECEIPT_FOOTER = ["Planned with Rivolt · rivolt.dev", "An estimate; real conditions change it.", "Unofficial, not affiliated with Rivian."];

const KM = 1000;
const MI = 1609.344;
const dist = (m: number, unit: DistanceUnit) => {
  const v = unit === "km" ? m / KM : m / MI;
  return `${Math.round(v).toLocaleString()} ${unit}`;
};
// Whole minutes, like the pages: "1 h 27 min", "18 min".
export const hmin = (s: number) => {
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")} min`;
};
const pct = (v: number) => `${Math.round(v)}%`;

// The clock in a zone, always with the zone's short name: the leave time is in the viewer's zone and the arrival in the destination's, so a
// reader can reconcile the two with the total time.
function clock(iso: string, zone: string): string {
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return "";
  try {
    return new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit", timeZone: zone, timeZoneName: "short" }).format(d);
  } catch {
    return new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit", timeZoneName: "short" }).format(d);
  }
}

function leaveLine(r: TripReceipt, zone: string): string {
  const start = `start with ${pct(r.start_soc)}`;
  const d = r.depart_at ? new Date(r.depart_at) : null;
  if (!d || !Number.isFinite(d.getTime())) return start[0].toUpperCase() + start.slice(1);
  const day = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric", timeZone: zone }).format(d);
  return `Leave ${day} · ${clock(r.depart_at!, zone)} · ${start}`;
}

const money = (n: number, cur: string) => {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: cur, maximumFractionDigits: 0 }).format(n);
  } catch {
    return `${Math.round(n)} ${cur}`;
  }
};

// buildReceiptModel: the sketch is in the model only when the user asked for it and the server sent segments (it sends none when the cut leaves
// nothing); the cost line only when the user asked for it and there is an estimate.
export function buildReceiptModel(r: TripReceipt, o: { model: string; unit: DistanceUnit; includeSketch: boolean; includeCost: boolean; cost?: TripCostEstimate | null; viewerTZ?: string }): ReceiptModel {
  const lines: ReceiptLine[] = r.rows.map((row) => {
    const tilde = row.rounded ? "~" : "";
    if (row.kind === "drive") {
      return {
        title: `DRIVE · ${tilde}${dist(row.dist_m ?? 0, o.unit)}`,
        right: row.dur_s ? `${tilde}${hmin(row.dur_s)}` : "",
        sub: `${row.from ?? ""} → ${row.to ?? ""}`,
        subRight: `${pct(row.from_soc)} → ${pct(row.to_soc)}`,
      };
    }
    if (row.kind === "charge") {
      return { title: `CHARGE${row.max_kw ? ` · ${Math.round(row.max_kw)} kW` : ""}`, right: hmin(row.dur_s ?? 0), sub: row.name ?? "", subRight: `${pct(row.from_soc)} → ${pct(row.to_soc)}` };
    }
    // An added stop: how long it lasts when the plan says so, and the battery only when it changes.
    return { title: row.name ?? "", right: row.dur_s ? hmin(row.dur_s) : "", sub: "", subRight: Math.round(row.from_soc) === Math.round(row.to_soc) ? "" : `${pct(row.from_soc)} → ${pct(row.to_soc)}` };
  });
  const t = r.totals;
  const viewerTZ = o.viewerTZ ?? Intl.DateTimeFormat().resolvedOptions().timeZone;
  const totals = [
    { label: "Driving", value: `~${hmin(t.drive_s)}` },
    { label: `Charging · ${t.stops} ${t.stops === 1 ? "stop" : "stops"}`, value: hmin(t.charge_s) },
  ];
  if (t.arrive_at) totals.push({ label: "Arrive", value: `~${clock(t.arrive_at, r.tz || viewerTZ)} · ${pct(t.arrival_soc)}` });
  let cost: ReceiptModel["cost"] = null;
  const c = o.cost;
  if (o.includeCost && c) {
    const lo = c.dcfc_spend_low ?? c.dcfc_spend;
    const hi = c.dcfc_spend_high ?? c.dcfc_spend;
    if (lo != null && hi != null && hi > 0) cost = { label: "Charging cost (estimate)", value: Math.round(lo) === Math.round(hi) ? money(hi, c.currency) : `${money(lo, c.currency)}–${money(hi, c.currency)}` };
  }
  return {
    head: ["RIVOLT · TRIP PLAN", o.model],
    title: `${r.start} → ${r.end}`,
    sub: leaveLine(r, viewerTZ),
    sketch: o.includeSketch && r.sketch && r.sketch.length > 0 ? r.sketch : null,
    lines,
    totals,
    cost,
    big: [`~${dist(t.dist_m, o.unit)}`, `~${hmin(t.drive_s + t.charge_s)}`],
    footer: RECEIPT_FOOTER,
  };
}

// receiptText is every word the card says, for tests and the preview's accessible label.
export function receiptText(m: ReceiptModel): string {
  return [m.head.join(" "), m.title, m.sub, ...m.lines.flatMap((l) => [l.title, l.right, l.sub, l.subRight]), ...m.totals.flatMap((x) => [x.label, x.value]), m.cost ? `${m.cost.label} ${m.cost.value}` : "", m.big.join(" "), ...m.footer].join("\n");
}
