import { drawRoute, routePaths } from "./shareCard";
import type { ReceiptModel } from "./tripReceipt";

// Paints the trip receipt (a till receipt on a dark ground, as tall as it needs to be). System fonts only: nothing is fetched, and the PNG
// canvas.toBlob makes carries no metadata text.
const W = 1080;
const PAD = 80;
const EDGE = 36;
const MONO = "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace";
const SANS = "system-ui, -apple-system, Segoe UI, sans-serif";
const PAPER = "#f5f2ea";
const INK = "#171717";
const MUTED = "#6b6b62";

function clip(g: CanvasRenderingContext2D, text: string, max: number): string {
  if (g.measureText(text).width <= max) return text;
  let t = text;
  while (t.length > 1 && g.measureText(t + "…").width > max) t = t.slice(0, -1);
  return t + "…";
}

// wrap breaks text at spaces into lines no wider than max, so a long place name moves to the next line instead of being cut off.
function wrap(g: CanvasRenderingContext2D, text: string, max: number): string[] {
  const out: string[] = [];
  let line = "";
  for (const word of text.split(" ")) {
    const next = line ? `${line} ${word}` : word;
    if (line && g.measureText(next).width > max) {
      out.push(line);
      line = word;
    } else line = next;
  }
  if (line) out.push(line);
  return out.length > 0 ? out : [""];
}

function rule(g: CanvasRenderingContext2D, y: number, dotted = false) {
  g.save();
  g.strokeStyle = "#a8a395";
  g.lineWidth = 3;
  g.setLineDash(dotted ? [2, 8] : [14, 10]);
  g.beginPath();
  g.moveTo(EDGE + PAD, y);
  g.lineTo(W - EDGE - PAD, y);
  g.stroke();
  g.restore();
}

// draw lays the receipt out from the top and returns its height; with g null-sized it is also how the height is found.
function draw(g: CanvasRenderingContext2D, m: ReceiptModel): number {
  const L = EDGE + PAD;
  const R = W - EDGE - PAD;
  const inner = R - L;
  g.textBaseline = "alphabetic";
  g.fillStyle = "#0a0a0a";
  g.fillRect(0, 0, W, 6000);
  let y = EDGE + 100;
  g.fillStyle = PAPER;
  g.fillRect(EDGE, EDGE, W - 2 * EDGE, 6000);
  g.fillStyle = INK;
  g.font = `700 30px ${MONO}`;
  g.textAlign = "left";
  g.fillText(m.head[0], L, y);
  g.textAlign = "right";
  g.fillText(m.head[1], R, y);
  g.textAlign = "left";
  y += 90;
  let size = 64;
  g.font = `800 ${size}px ${SANS}`;
  while (size > 34 && g.measureText(m.title).width > inner) {
    size -= 4;
    g.font = `800 ${size}px ${SANS}`;
  }
  g.fillText(m.title, L, y);
  y += 50;
  g.fillStyle = MUTED;
  g.font = `400 28px ${MONO}`;
  g.fillText(clip(g, m.sub, inner), L, y);
  y += 40;
  const paths = m.sketch ? routePaths(m.sketch) : [];
  if (paths.length > 0) {
    drawRoute(g, paths, L, y + 10, inner, 200, INK);
    y += 240;
  } else y += 20;
  rule(g, y);
  y += 60;
  for (const l of m.lines) {
    g.fillStyle = INK;
    g.font = `700 32px ${MONO}`;
    g.textAlign = "right";
    g.fillText(l.right, R, y);
    const rw = g.measureText(l.right).width;
    g.textAlign = "left";
    g.fillText(clip(g, l.title, inner - rw - 24), L, y);
    y += 40;
    if (l.sub || l.subRight) {
      g.fillStyle = MUTED;
      g.font = `400 27px ${MONO}`;
      g.textAlign = "right";
      g.fillText(l.subRight, R, y);
      const sw = g.measureText(l.subRight).width;
      g.textAlign = "left";
      const lines = wrap(g, l.sub, inner - sw - 24);
      lines.forEach((t, i) => {
        g.fillText(clip(g, t, inner - sw - 24), L, y + i * 36);
      });
      y += 40 + (lines.length - 1) * 36;
    }
    y += 22;
  }
  rule(g, y - 6);
  y += 50;
  g.font = `400 32px ${MONO}`;
  for (const t of m.totals) {
    g.fillStyle = INK;
    g.textAlign = "left";
    g.fillText(t.label, L, y);
    const lw = g.measureText(t.label).width;
    g.textAlign = "right";
    g.font = `700 32px ${MONO}`;
    g.fillText(t.value, R, y);
    const vw = g.measureText(t.value).width;
    g.font = `400 32px ${MONO}`;
    rule(g, y - 8, true);
    // The dotted leader sits between label and value: paper over the parts that touch the words.
    g.fillStyle = PAPER;
    g.fillRect(L, y - 40, lw + 12, 50);
    g.fillRect(R - vw - 12, y - 40, vw + 12, 50);
    g.fillStyle = INK;
    g.textAlign = "left";
    g.fillText(t.label, L, y);
    g.textAlign = "right";
    g.font = `700 32px ${MONO}`;
    g.fillText(t.value, R, y);
    g.font = `400 32px ${MONO}`;
    y += 52;
  }
  if (m.cost) {
    g.textAlign = "left";
    g.fillStyle = INK;
    g.fillText(m.cost.label, L, y);
    g.textAlign = "right";
    g.font = `700 32px ${MONO}`;
    g.fillText(m.cost.value, R, y);
    y += 52;
  }
  y += 4;
  rule(g, y);
  y += 90;
  g.fillStyle = INK;
  g.font = `800 68px ${SANS}`;
  g.textAlign = "left";
  g.fillText(m.big[0], L, y);
  g.textAlign = "right";
  g.fillText(m.big[1], R, y);
  y += 24;
  rule(g, y + 10);
  y += 80;
  g.fillStyle = MUTED;
  g.font = `400 26px ${MONO}`;
  g.textAlign = "center";
  for (const line of m.footer) {
    g.fillText(line, W / 2, y);
    y += 38;
  }
  g.textAlign = "left";
  return y + 20;
}

// paintReceipt sizes the canvas to the receipt and draws it, with a scalloped bottom edge.
export function paintReceipt(canvas: HTMLCanvasElement, m: ReceiptModel): boolean {
  const probe = document.createElement("canvas");
  probe.width = W;
  probe.height = 6000;
  const pg = probe.getContext("2d");
  const g = canvas.getContext("2d");
  if (!pg || !g) return false;
  const paperH = draw(pg, m);
  const H = paperH + EDGE;
  canvas.width = W;
  canvas.height = H;
  draw(g, m);
  g.fillStyle = "#0a0a0a";
  g.fillRect(0, paperH, W, EDGE + 20);
  for (let x = EDGE + 18; x < W - EDGE; x += 36) {
    g.beginPath();
    g.arc(x, paperH, 14, 0, Math.PI * 2);
    g.fill();
  }
  return true;
}

export async function receiptBlob(m: ReceiptModel): Promise<Blob | null> {
  const canvas = document.createElement("canvas");
  if (!paintReceipt(canvas, m)) return null;
  return new Promise((res) => canvas.toBlob((b) => res(b), "image/png"));
}
