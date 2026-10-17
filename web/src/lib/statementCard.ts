// Shareable monthly statement card (U3). The model is built only from
// the server's aggregate totals (contract §10/§11): no receipts, no
// places, no coordinates, no addresses, no vehicle or charge ids. That
// is the privacy guarantee, so it lives in the model, not the painter.

import {
  estimatedShare,
  formatMoney,
  monthLabel,
  type LedgerMonth,
  statementTotal,
} from "./ledger";

export type StatementCardModel = {
  title: string;
  total: string;
  subtitle: string;
  partial: boolean;
  // ISO date the data starts, when the month is partial (MQ8).
  partialFrom: string | null;
  // Sessions the total excludes, so the card never overstates it.
  notIncluded: string | null;
  estimatedPct: number | null;
  buckets: { label: string; value: number; display: string; color: string }[];
  footnote: string;
};

function notIncluded(m: LedgerMonth): string | null {
  const unpriced = m.spend.unpriced_sessions ?? 0;
  const pending = m.spend.estimate_pending_sessions ?? 0;
  const n = unpriced + pending;
  if (n <= 0) return null;
  return `+ ${n} ${n === 1 ? "session" : "sessions"} not yet priced`;
}

const COLORS = { home: "#10b981", public: "#38bdf8", dcfc: "#f59e0b", unknown: "#737373" };

export function buildStatementCardModel(
  m: LedgerMonth,
  opts: { partialFrom?: string | null; sample?: boolean } = {},
): StatementCardModel {
  const cur = m.currency;
  const e = m.energy;
  const pk = e.by_place_kind;
  const raw: { label: string; cost: number; color: string }[] = pk
    ? [
        { label: "Home", cost: pk.home.ac.cost, color: COLORS.home },
        { label: "Away", cost: pk.away.ac.cost, color: COLORS.public },
        { label: "Fast charging", cost: e.by_kind.dcfc.cost, color: COLORS.dcfc },
        ...(pk.unknown.ac.sessions > 0 ? [{ label: "Other", cost: pk.unknown.ac.cost, color: COLORS.unknown }] : []),
      ]
    : [
        { label: "AC", cost: e.by_kind.ac.cost, color: COLORS.home },
        { label: "Fast charging", cost: e.by_kind.dcfc.cost, color: COLORS.dcfc },
      ];
  const est = estimatedShare(m);
  const parts = [`${e.kwh_added.toFixed(0)} kWh`];
  if (m.driving.miles > 0) parts.push(`${m.driving.miles.toFixed(0)} mi`);
  if (m.driving.cost_per_mile != null) parts.push(`${formatMoney(m.driving.cost_per_mile, cur, 3)}/mi`);
  return {
    title: monthLabel(m.period),
    total: formatMoney(statementTotal(m), cur),
    subtitle: parts.join(" · "),
    partial: m.is_current_month,
    partialFrom: opts.partialFrom ?? null,
    notIncluded: notIncluded(m),
    estimatedPct: est > 0 ? Math.round(est * 100) : null,
    buckets: raw.map((b) => ({ label: b.label, color: b.color, value: Math.round(b.cost * 100) / 100, display: formatMoney(b.cost, cur) })),
    footnote: opts.sample ? "Sample truck · generated data" : "Charging cost, tracked with Rivolt",
  };
}

const W = 1080;
const H = 1080;

// Paints the model to a canvas. Fixed 1080×1080 so it posts anywhere.
export function paintStatementCard(canvas: HTMLCanvasElement, c: StatementCardModel): boolean {
  const g = canvas.getContext("2d");
  if (!g) return false;
  canvas.width = W;
  canvas.height = H;
  g.fillStyle = "#0a0a0a";
  g.fillRect(0, 0, W, H);
  const font = "system-ui, -apple-system, Segoe UI, sans-serif";
  g.textBaseline = "alphabetic";
  g.fillStyle = "#a3a3a3";
  g.font = `500 44px ${font}`;
  let head = c.title;
  if (c.partialFrom) {
    const d = new Date(c.partialFrom);
    if (!Number.isNaN(d.getTime())) head += ` · from ${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })}`;
  } else if (c.partial) head += " · month to date";
  g.fillText(head, 80, 140);
  g.fillStyle = "#fafafa";
  // Shrink the total to fit the 920px column ("$1,000.00" and longer).
  let size = 200;
  g.font = `700 ${size}px ${font}`;
  while (size > 80 && g.measureText(c.total).width > W - 160) {
    size -= 8;
    g.font = `700 ${size}px ${font}`;
  }
  g.fillText(c.total, 80, 360);
  g.fillStyle = "#a3a3a3";
  g.font = `400 44px ${font}`;
  g.fillText(c.subtitle, 80, 440);
  g.font = `400 34px ${font}`;
  let noteY = 500;
  if (c.estimatedPct != null) {
    g.fillText(`Includes ${c.estimatedPct}% estimated from network rates`, 80, noteY);
    noteY += 48;
  }
  if (c.notIncluded) {
    g.fillStyle = "#fbbf24";
    g.fillText(c.notIncluded, 80, noteY);
    g.fillStyle = "#a3a3a3";
  }
  const sum = c.buckets.reduce((a, b) => a + Math.max(0, b.value), 0);
  let x = 80;
  const barW = W - 160;
  g.fillStyle = "#262626";
  g.fillRect(80, 580, barW, 36);
  if (sum > 0) {
    for (const b of c.buckets) {
      const w = (Math.max(0, b.value) / sum) * barW;
      g.fillStyle = b.color;
      g.fillRect(x, 580, w, 36);
      x += w;
    }
  }
  let y = 700;
  g.font = `500 44px ${font}`;
  for (const b of c.buckets) {
    g.fillStyle = b.color;
    g.fillRect(80, y - 30, 30, 30);
    g.fillStyle = "#e5e5e5";
    g.textAlign = "left";
    g.fillText(b.label, 130, y);
    g.textAlign = "right";
    g.fillText(b.display, W - 80, y);
    y += 80;
  }
  g.textAlign = "left";
  g.fillStyle = "#737373";
  g.font = `400 34px ${font}`;
  g.fillText(c.footnote, 80, H - 70);
  return true;
}

export async function statementCardBlob(c: StatementCardModel): Promise<Blob | null> {
  const canvas = document.createElement("canvas");
  if (!paintStatementCard(canvas, c)) return null;
  return new Promise((res) => canvas.toBlob((b) => res(b), "image/png"));
}
