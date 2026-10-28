// Shareable summary cards (month, trip, milestone). The model is the privacy guarantee: it carries only display
// text (no place, id, coordinate, VIN or map field, so none can reach the painter), and it is painted in the browser
// from figures the page already has. Nothing is uploaded, posted or tracked. Cost is opt-in on every card.

import { estimatedShare, formatMoney, monthLabel, type LedgerMonth } from "./ledger";
import { buildStatementCardModel } from "./statementCard";
import { shareFooterLine } from "./appLink";
import { getConfig } from "./config";
import type { Drive, MilestoneItem } from "./api";

export type ShareKind = "month" | "trip" | "milestone";
export type ShareStat = { label: string; value: string };
export type ShareCardModel = {
  kind: ShareKind;
  eyebrow: string;
  title: string;
  subtitle: string;
  stats: ShareStat[];
  notes: string[];
  sample: boolean;
};

export const SHARE_FOOTER = ["Made with Rivolt · rivolt.dev", "Unofficial. Not affiliated with Rivian."] as const;

// The footer as drawn: the first line gains "· iPhone app on the App Store" once the app is listed on the App Store (never TestFlight).
export const shareFooter = (): readonly string[] => [shareFooterLine(getConfig().features.iosAppUrl), SHARE_FOOTER[1]];
const SAMPLE_LINE = "Sample truck · generated data";

const n0 = (x: number) => Math.round(x).toLocaleString();
const hm = (min: number) => (min >= 60 ? `${Math.floor(min / 60)} h ${Math.round(min % 60)} min` : `${Math.round(min)} min`);

// offpeakSharePct: the share of home charging (by energy) that ran in off-peak hours, only when the user turns that line on. A
// percentage alone: no rate, no dollars, no window times.
export function buildMonthShareModel(m: LedgerMonth, o: { includeCost: boolean; sample: boolean; partialFrom?: string | null; offpeakSharePct?: number | null }): ShareCardModel {
  const miles = m.driving.miles;
  const kwhUsed = m.driving.kwh_used_known;
  const stats: ShareStat[] = [
    { label: "Driven", value: `${n0(miles)} mi` },
    { label: "Energy added", value: `${m.energy.includes_estimated ? "~" : ""}${n0(m.energy.kwh_added)} kWh` },
  ];
  if (kwhUsed > 0 && m.driving.miles_with_energy > 0) stats.push({ label: "Efficiency", value: `${(m.driving.miles_with_energy / kwhUsed).toFixed(2)} mi/kWh` });
  const notes: string[] = [];
  let title = `${n0(miles)} mi`;
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

export function buildTripShareModel(d: Drive, o: { includeCost: boolean; sample: boolean }): ShareCardModel {
  const start = new Date(d.StartedAt);
  const end = new Date(d.EndedAt);
  const min = Number.isFinite(start.getTime()) && Number.isFinite(end.getTime()) ? Math.max(0, (end.getTime() - start.getTime()) / 60_000) : 0;
  const stats: ShareStat[] = [];
  if (d.EnergyUsedKWh > 0 && d.DistanceMi > 0) stats.push({ label: "Efficiency", value: `${(d.DistanceMi / d.EnergyUsedKWh).toFixed(2)} mi/kWh` });
  if (d.EnergyUsedKWh > 0) stats.push({ label: "Energy used", value: `${d.EnergyUsedKWh.toFixed(1)} kWh` });
  if (d.StartSoCPct > 0 || d.EndSoCPct > 0) stats.push({ label: "Battery", value: `${Math.round(d.StartSoCPct)}% → ${Math.round(d.EndSoCPct)}%` });
  if (d.AvgSpeedMph > 0) stats.push({ label: "Average speed", value: `${Math.round(d.AvgSpeedMph)} mph` });
  if (o.includeCost && d.estimated_cost && d.estimated_cost > 0) {
    stats.push({ label: "Charging cost (estimate)", value: `~${formatMoney(d.estimated_cost, d.estimated_currency || "USD")}` });
  }
  // The day only, never the time of day.
  const day = Number.isFinite(start.getTime()) ? start.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long" }) : "";
  return { kind: "trip", eyebrow: "Trip", title: `${n0(d.DistanceMi)} mi${min > 0 ? ` · ${hm(min)}` : ""}`, subtitle: day, stats, notes: [], sample: o.sample };
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
  g.fillStyle = "#fafafa";
  fit(g, c.title, c.kind === "milestone" ? 120 : 190, 64);
  let y = 330;
  if (c.kind === "milestone") y = wrap(g, c.title, 80, 300, 130, 2) - 40;
  else g.fillText(c.title, 80, y);
  g.fillStyle = "#a3a3a3";
  g.font = `400 44px ${FONT}`;
  y = wrap(g, c.subtitle, 80, y + 80, 56, 3);
  y += 30;
  g.font = `500 44px ${FONT}`;
  for (const s of c.stats.slice(0, 6)) {
    g.fillStyle = "#a3a3a3";
    g.textAlign = "left";
    g.fillText(s.label, 80, y);
    g.fillStyle = "#e5e5e5";
    g.textAlign = "right";
    g.fillText(s.value, W - 80, y);
    y += 78;
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
