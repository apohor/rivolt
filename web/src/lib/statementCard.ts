// Shareable monthly statement card (U3). The model is built only from
// the server's aggregate totals (contract §10/§11): no receipts, no
// places, no coordinates, no addresses, no vehicle or charge ids. That
// is the privacy guarantee, so it lives in the model, not the painter.

import {
  estimatedShare,
  formatMoney,
  monthLabel,
  type LedgerMonth,
} from "./ledger";

export type StatementCardModel = {
  title: string;
  total: string;
  subtitle: string;
  partial: boolean;
  estimatedPct: number | null;
  buckets: { label: string; value: number; display: string; color: string }[];
  footnote: string;
};

const COLORS = { home: "#10b981", public: "#38bdf8", dcfc: "#f59e0b", unknown: "#737373" };

export function buildStatementCardModel(m: LedgerMonth): StatementCardModel {
  const cur = m.currency;
  const e = m.energy;
  const pk = e.by_place_kind;
  const raw: { label: string; cost: number; color: string }[] = pk
    ? [
        { label: "Home", cost: pk.home.ac.cost, color: COLORS.home },
        { label: "Public", cost: pk.away.ac.cost, color: COLORS.public },
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
    total: formatMoney(m.spend.total, cur),
    subtitle: parts.join(" · "),
    partial: m.is_current_month,
    estimatedPct: est > 0 ? Math.round(est * 100) : null,
    buckets: raw.map((b) => ({ label: b.label, color: b.color, value: Math.round(b.cost * 100) / 100, display: formatMoney(b.cost, cur) })),
    footnote: "Charging cost, tracked with Rivolt",
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
  g.fillText(c.title + (c.partial ? " · month to date" : ""), 80, 140);
  g.fillStyle = "#fafafa";
  g.font = `700 200px ${font}`;
  g.fillText(c.total, 80, 360);
  g.fillStyle = "#a3a3a3";
  g.font = `400 44px ${font}`;
  g.fillText(c.subtitle, 80, 440);
  if (c.estimatedPct != null) {
    g.font = `400 34px ${font}`;
    g.fillText(`Includes ${c.estimatedPct}% estimated from network rates`, 80, 500);
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
