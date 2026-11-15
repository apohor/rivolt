// Shareable summary cards (month, trip, milestone). The model is the privacy guarantee: it carries only display
// text (no place, id, coordinate or VIN field, so none can reach the painter), and it is painted in the browser
// from figures the page already has. Nothing is uploaded, posted or tracked. Cost is opt-in on every card.
// The one exception is the trip card's opt-in map: the server's trimmed postcard route (GET /api/drives/{id}/postcard,
// both ends and the saved home already cut off), drawn as a bare line with no label, marker, date or time.

import { driveDurationSeconds } from "./drives";
import { estimatedShare, formatMoney, monthLabel, type LedgerMonth } from "./ledger";
import { buildStatementCardModel } from "./statementCard";
import { shareFooterLine } from "./appLink";
import { getConfig } from "./config";
import { decodePolyline } from "./polyline";
import type { Drive, MilestoneItem } from "./api";
import type { DistanceUnit } from "./preferences";

export type ShareKind = "month" | "trip" | "milestone" | "year";
export type ShareStat = { label: string; value: string };
export type ShareCardModel = {
  kind: ShareKind;
  eyebrow: string;
  title: string;
  subtitle: string;
  stats: ShareStat[];
  notes: string[];
  sample: boolean;
  // The trimmed route as polyline6 segments, only when the user turned "Include map" on.
  map?: { segments: string[] };
};

export const SHARE_FOOTER = ["Made with Rivolt · rivolt.dev", "Unofficial. Not affiliated with Rivian."] as const;

// The footer as drawn: the first line gains "· iPhone app on the App Store" once the app is listed on the App Store (never TestFlight).
export const shareFooter = (): readonly string[] => [shareFooterLine(getConfig().features.iosAppUrl), SHARE_FOOTER[1]];
const SAMPLE_LINE = "Sample truck · generated data";

const n0 = (x: number) => Math.round(x).toLocaleString();
// The card says the distance, speed and efficiency in the user's unit, like the pages do (the figures arrive in miles).
const KM = 1.609344;
const kOf = (unit: DistanceUnit) => (unit === "km" ? KM : 1);
// Whole minutes, cut not rounded: the drive page's Duration (formatDuration) cuts too, so the card and the page say the same time.
const hm = (min: number) => {
  const m = Math.floor(min);
  return m >= 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m} min`;
};

// offpeakSharePct: the share of home charging (by energy) that ran in off-peak hours, only when the user turns that line on. A
// percentage alone: no rate, no dollars, no window times.
export function buildMonthShareModel(m: LedgerMonth, o: { includeCost: boolean; sample: boolean; partialFrom?: string | null; offpeakSharePct?: number | null; unit?: DistanceUnit }): ShareCardModel {
  const unit = o.unit ?? "mi";
  const k = kOf(unit);
  const miles = m.driving.miles * k;
  const kwhUsed = m.driving.kwh_used_known;
  const stats: ShareStat[] = [
    { label: "Driven", value: `${n0(miles)} ${unit}` },
    { label: "Energy added", value: `${m.energy.includes_estimated ? "~" : ""}${n0(m.energy.kwh_added)} kWh` },
  ];
  if (kwhUsed > 0 && m.driving.miles_with_energy > 0) stats.push({ label: "Efficiency", value: `${((m.driving.miles_with_energy * k) / kwhUsed).toFixed(2)} ${unit}/kWh` });
  const notes: string[] = [];
  let title = `${n0(miles)} ${unit}`;
  let subtitle = `${m.driving.drives} ${m.driving.drives === 1 ? "drive" : "drives"} · ${m.energy.sessions} ${m.energy.sessions === 1 ? "charge" : "charges"}`;
  if (o.includeCost) {
    // The statement card's figures, behind the toggle.
    const s = buildStatementCardModel(m, { partialFrom: o.partialFrom ?? null, sample: o.sample });
    title = s.total;
    subtitle = s.subtitle;
    stats.length = 0;
    for (const b of s.buckets) stats.push({ label: b.label, value: b.display });
    const est = estimatedShare(m);
    if (est > 0) notes.push(`Includes ${Math.round(est * 100)}% estimated from network rates`);
    if (s.notIncluded) notes.push(s.notIncluded);
  }
  if (o.offpeakSharePct != null && Number.isFinite(o.offpeakSharePct)) {
    stats.push({ label: "Home charging off-peak", value: `${Math.round(Math.min(100, Math.max(0, o.offpeakSharePct)))}%` });
  }
  return { kind: "month", eyebrow: `${monthLabel(m.period)}${m.is_current_month ? " so far" : ""}`, title, subtitle, stats, notes, sample: o.sample };
}

export function buildTripShareModel(d: Drive, o: { includeCost: boolean; sample: boolean; mapSegments?: string[] | null; unit?: DistanceUnit }): ShareCardModel {
  const unit = o.unit ?? "mi";
  const k = kOf(unit);
  const start = new Date(d.StartedAt);
  const end = new Date(d.EndedAt);
  // Driving time: on a merged round trip the wall span also holds the parked gaps between legs.
  const min = Number.isFinite(start.getTime()) && Number.isFinite(end.getTime()) ? Math.max(0, driveDurationSeconds(d) / 60) : 0;
  const stats: ShareStat[] = [];
  if (d.EnergyUsedKWh > 0 && d.DistanceMi > 0) stats.push({ label: "Efficiency", value: `${((d.DistanceMi * k) / d.EnergyUsedKWh).toFixed(2)} ${unit}/kWh` });
  if (d.EnergyUsedKWh > 0) stats.push({ label: "Energy used", value: `${d.EnergyUsedKWh.toFixed(1)} kWh` });
  if (d.StartSoCPct > 0 || d.EndSoCPct > 0) stats.push({ label: "Battery", value: `${Math.round(d.StartSoCPct)}% → ${Math.round(d.EndSoCPct)}%` });
  if (d.AvgSpeedMph > 0) stats.push({ label: "Average speed", value: `${Math.round(d.AvgSpeedMph * k)} ${unit === "km" ? "km/h" : "mph"}` });
  if (o.includeCost && d.estimated_cost && d.estimated_cost > 0) {
    stats.push({ label: "Charging cost (estimate)", value: `~${formatMoney(d.estimated_cost, d.estimated_currency || "USD")}` });
  }
  // The day only, never the time of day.
  const day = Number.isFinite(start.getTime()) ? start.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long" }) : "";
  // A merged round trip says so and counts its real legs (a zero-distance stub is not one); the totals above are the sum.
  const legs = d.round_trip_legs ?? 1;
  return { kind: "trip", eyebrow: legs > 1 ? "Round trip" : "Trip", title: `${n0(d.DistanceMi * k)} ${unit}${min > 0 ? ` · ${hm(min)}` : ""}`, subtitle: day, stats, notes: legs > 1 ? [`${legs} legs`] : [], sample: o.sample, ...(o.mapSegments && o.mapSegments.length > 0 ? { map: { segments: o.mapSegments } } : {}) };
}

const hasMoney = (s: string) => /[$€£]\s?\d|\d\s?(USD|EUR|GBP|CAD)\b/i.test(s);

export function buildMilestoneShareModel(m: MilestoneItem, o: { sample: boolean }): ShareCardModel {
  const when = new Date(m.achieved_at);
  const month = Number.isFinite(when.getTime()) ? when.toLocaleDateString(undefined, { month: "long", year: "numeric" }) : "";
  // The server's words as given, but never a dollar figure: cost is opt-in on every card.
  const body = hasMoney(m.body) ? "" : m.body;
  return { kind: "milestone", eyebrow: "Milestone", title: m.title, subtitle: [body, month].filter(Boolean).join(" · "), stats: [], notes: [], sample: o.sample };
}

// A generic name: the kind and the month, never a drive, vehicle or place.
export function shareFileName(kind: ShareKind, iso: string): string {
  const d = new Date(iso);
  const ym = Number.isFinite(d.getTime()) ? `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}` : "card";
  return `rivolt-${kind}-${ym}.png`;
}

const W = 1080;
const H = 1080;
const FONT = "system-ui, -apple-system, Segoe UI, sans-serif";

function fit(g: CanvasRenderingContext2D, text: string, start: number, min: number, weight = 700): number {
  let size = start;
  g.font = `${weight} ${size}px ${FONT}`;
  while (size > min && g.measureText(text).width > W - 160) {
    size -= 8;
    g.font = `${weight} ${size}px ${FONT}`;
  }
  return size;
}

// Wraps text to the column; returns the y after the last line.
function wrap(g: CanvasRenderingContext2D, text: string, x: number, y: number, lineH: number, maxLines: number): number {
  const words = text.split(/\s+/);
  let line = "";
  let lines = 0;
  for (const w of words) {
    const test = line ? `${line} ${w}` : w;
    if (g.measureText(test).width > W - 160 && line) {
      g.fillText(line, x, y);
      y += lineH;
      lines++;
      line = w;
      if (lines >= maxLines) return y;
    } else line = test;
  }
  if (line) {
    g.fillText(line, x, y);
    y += lineH;
  }
  return y;
}

// The server's segments decoded, each kept apart so the cut-off stretches are never joined by a line.
function routePaths(segments: string[]): [number, number][][] {
  return segments.map((s) => decodePolyline(s, 6)).filter((p) => p.length >= 2);
}

// Draws the paths fitted into the box (equirectangular, so a mile north is as long as a mile east), as a bare line.
function drawRoute(g: CanvasRenderingContext2D, paths: [number, number][][], x: number, y: number, w: number, h: number) {
  let minLat = Infinity;
  let maxLat = -Infinity;
  let minLon = Infinity;
  let maxLon = -Infinity;
  for (const p of paths) {
    for (const [lat, lon] of p) {
      minLat = Math.min(minLat, lat);
      maxLat = Math.max(maxLat, lat);
      minLon = Math.min(minLon, lon);
      maxLon = Math.max(maxLon, lon);
    }
  }
  const kx = Math.cos((((minLat + maxLat) / 2) * Math.PI) / 180);
  const spanX = Math.max((maxLon - minLon) * kx, 1e-6);
  const spanY = Math.max(maxLat - minLat, 1e-6);
  const k = Math.min(w / spanX, h / spanY);
  const ox = x + (w - spanX * k) / 2;
  const oy = y + (h - spanY * k) / 2;
  g.strokeStyle = "#e5e5e5";
  g.lineWidth = 7;
  g.lineJoin = "round";
  g.lineCap = "round";
  for (const p of paths) {
    g.beginPath();
    p.forEach(([lat, lon], i) => {
      const px = ox + (lon - minLon) * kx * k;
      const py = oy + (maxLat - lat) * k;
      if (i === 0) g.moveTo(px, py);
      else g.lineTo(px, py);
    });
    g.stroke();
  }
}

// Paints the model to a canvas, fixed 1080x1080 so it posts anywhere. Fonts are the system's: nothing is fetched.
export function paintShareCard(canvas: HTMLCanvasElement, c: ShareCardModel): boolean {
  const g = canvas.getContext("2d");
  if (!g) return false;
  canvas.width = W;
  canvas.height = H;
  g.fillStyle = "#0a0a0a";
  g.fillRect(0, 0, W, H);
  g.textBaseline = "alphabetic";
  g.textAlign = "left";
  g.fillStyle = "#a3a3a3";
  g.font = `500 44px ${FONT}`;
  g.fillText(c.eyebrow, 80, 140);
  // With a map the header and rows tighten so the line gets a band of its own between the subtitle and the stats.
  const paths = c.map ? routePaths(c.map.segments) : [];
  const withMap = c.kind === "trip" && paths.length > 0;
  g.fillStyle = "#fafafa";
  fit(g, c.title, c.kind === "milestone" ? 120 : withMap ? 130 : 190, 64);
  let y = withMap ? 270 : 330;
  if (c.kind === "milestone") y = wrap(g, c.title, 80, 300, 130, 2) - 40;
  else g.fillText(c.title, 80, y);
  g.fillStyle = "#a3a3a3";
  g.font = `400 44px ${FONT}`;
  y = wrap(g, c.subtitle, 80, y + (withMap ? 70 : 80), 56, 3);
  y += withMap ? 0 : 30;
  const rows = c.stats.slice(0, 6);
  const step = withMap ? 52 : c.kind === "year" ? 66 : 78;
  if (withMap) {
    // The band ends above the rows, the notes and the footer (with its sample line).
    const footerTop = H - 70 - (shareFooter().length - 1) * 44 - 36 - (c.sample ? 52 : 0);
    const below = rows.length * step + c.notes.length * 44 + 24;
    const bandH = Math.min(300, footerTop - below - y - 20);
    if (bandH >= 120) {
      drawRoute(g, paths, 80, y, W - 160, bandH);
      y += bandH + 50;
    } else y += 30;
  }
  // A year card has six longer rows: a little smaller so the value never meets its label.
  g.font = `500 ${c.kind === "year" ? 40 : withMap ? 38 : 44}px ${FONT}`;
  for (const s of rows) {
    g.fillStyle = "#a3a3a3";
    g.textAlign = "left";
    g.fillText(s.label, 80, y);
    g.fillStyle = "#e5e5e5";
    g.textAlign = "right";
    g.fillText(s.value, W - 80, y);
    y += step;
  }
  g.textAlign = "left";
  g.font = `400 32px ${FONT}`;
  g.fillStyle = "#fbbf24";
  for (const note of c.notes) {
    g.fillText(note, 80, y);
    y += 44;
  }
  g.fillStyle = "#737373";
  g.font = `400 32px ${FONT}`;
  const footer = shareFooter();
  let fy = H - 70 - (footer.length - 1) * 44;
  if (c.sample) {
    g.fillText(SAMPLE_LINE, 80, fy - 52);
  }
  for (const line of footer) {
    g.fillText(line, 80, fy);
    fy += 44;
  }
  return true;
}

export async function shareCardBlob(c: ShareCardModel): Promise<Blob | null> {
  const canvas = document.createElement("canvas");
  if (!paintShareCard(canvas, c)) return null;
  return new Promise((res) => canvas.toBlob((b) => res(b), "image/png"));
}
